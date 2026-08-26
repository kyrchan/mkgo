package main

import (
	"errors"
	"sync"
)

// TCP (RFC 793) — v1 scope per extended mandate: full handshake +
// teardown state machine, cumulative ACKs, sliding send window with a
// conservative cap (start: 1 segment in flight; grows only if tests
// stay green). No congestion control, no SACK, no header options.

const (
	TCPHdrLen = 20

	TCPFin uint8 = 1 << 0
	TCPSyn uint8 = 1 << 1
	TCPRst uint8 = 1 << 2
	TCPPsh uint8 = 1 << 3
	TCPAck uint8 = 1 << 4

	// v1 in-flight cap: one segment's worth of unacked bytes.
	tcpWindowCap = 1024

	tcpMaxSeg = 512 // payload cap per outbound segment
)

var ErrTCP = errors.New("net: malformed tcp")

type TCPSegment struct {
	SrcPort, DstPort uint16
	Seq, Ack         uint32
	Flags            uint8
	Window           uint16
	Payload          []byte
}

func ParseTCP(p []byte) (*TCPSegment, error) {
	if len(p) < TCPHdrLen {
		return nil, ErrShortFrame
	}
	off := int(p[12]>>4) * 4
	if off < TCPHdrLen || off > len(p) {
		return nil, ErrTCP
	}
	return &TCPSegment{
		SrcPort: BeGet16(p[0:2]),
		DstPort: BeGet16(p[2:4]),
		Seq:     BeGet32(p[4:8]),
		Ack:     BeGet32(p[8:12]),
		Flags:   p[13] & 0x1f,
		Window:  BeGet16(p[14:16]),
		Payload: p[off:],
	}, nil
}

func (t *TCPSegment) Build() []byte {
	out := make([]byte, TCPHdrLen+len(t.Payload))
	BePut16(out[0:2], t.SrcPort)
	BePut16(out[2:4], t.DstPort)
	BePut32(out[4:8], t.Seq)
	BePut32(out[8:12], t.Ack)
	out[12] = 5 << 4 // data offset 5, no options
	out[13] = t.Flags & 0x1f
	BePut16(out[14:16], t.Window)
	copy(out[TCPHdrLen:], t.Payload)
	return out
}

// seqGT reports a > b in modular sequence arithmetic.
func seqGT(a, b uint32) bool { return int32(a-b) > 0 }

func seqGE(a, b uint32) bool { return a == b || seqGT(a, b) }

// ---- states ----

type tcpState int

const (
	stateClosed tcpState = iota
	stateListen
	stateSynSent
	stateSynRcvd
	stateEstablished
	stateFinWait
	stateCloseWait
	stateLastAck
)

func (s tcpState) String() string {
	switch s {
	case stateListen:
		return "LISTEN"
	case stateSynSent:
		return "SYN-SENT"
	case stateSynRcvd:
		return "SYN-RCVD"
	case stateEstablished:
		return "ESTABLISHED"
	case stateFinWait:
		return "FIN-WAIT"
	case stateCloseWait:
		return "CLOSE-WAIT"
	case stateLastAck:
		return "LAST-ACK"
	default:
		return "CLOSED"
	}
}

var ErrConnReset = errors.New("net: connection reset")
var ErrRemoteClosed = errors.New("net: remote closed")
var ErrClosed = errors.New("net: closed")

// TCPConn is one connection endpoint owned by a TCPStack.
type TCPConn struct {
	stack      *TCPStack
	LocalIP    IP4
	LocalPort  uint16
	RemoteIP   IP4
	RemotePort uint16

	mu               sync.Mutex
	state            tcpState
	sndUNA           uint32 // oldest unacked byte
	sndNXT           uint32 // next seq to assign
	rcvNXT           uint32 // next expected remote byte
	finSeq           uint32 // seq of our FIN (valid when finPending/finSent)
	finSent          bool
	finPending       bool   // Close() called; FIN deferred until queue drain
	sndBuf           []byte // queued bytes not yet segmented
	rcvBuf           []byte // app-visible receive stream
	inFlight         []byte // unacked payload bytes (v1 window)
	finAckedTheirFin bool
	l                *TCPListener // listeners only: backref for accept queue
	err              error
}

// Recv pops up to len(buf) stream bytes; returns copied count.
func (c *TCPConn) Recv(buf []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := copy(buf, c.rcvBuf)
	c.rcvBuf = c.rcvBuf[n:]
	return n
}

// State returns the current state name.
func (c *TCPConn) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.String()
}

// rcvNXTUnderRace reads the expected seq (tests forge segments).
func (c *TCPConn) rcvNXTUnderRace() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rcvNXT
}

// sndNXTUnderRace reads our next-to-send seq (tests forge peer segments).
func (c *TCPConn) sndNXTUnderRace() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sndNXT
}

// Err surfaces terminal conditions (reset / remote closed / closed).
func (c *TCPConn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rcvBuf) > 0 && (c.err == ErrRemoteClosed || c.err == ErrClosed) {
		return nil // still draining buffered data
	}
	return c.err
}

func (c *TCPConn) sendLocked(flags uint8, payload []byte) {
	seq := c.sndNXT
	seg := &TCPSegment{
		SrcPort: c.LocalPort,
		DstPort: c.RemotePort,
		Seq:     seq,
		Ack:     c.rcvNXT,
		Flags:   flags,
		Window:  uint16(maxInt(0, tcpWindowCap-len(c.inFlight))),
		Payload: payload,
	}
	if flags&TCPAck != 0 || c.state != stateSynSent {
		seg.Flags |= TCPAck
	}
	c.sndNXT += uint32(len(payload))
	if flags&(TCPSyn|TCPFin) != 0 {
		c.sndNXT++ // SYN/FIN consume one sequence number
	}
	if flags&TCPFin != 0 {
		c.finSeq = seq
		c.finSent = true
	}
	if len(payload) > 0 {
		cp := make([]byte, len(payload))
		copy(cp, payload)
		c.inFlight = append(c.inFlight, cp...)
	}
	c.stack.s.SendTCPSegment(c.RemoteIP, seg.Build())
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Write queues stream bytes; segments what the window allows now.
func (c *TCPConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != stateEstablished || c.finPending {
		return 0, ErrClosed
	}
	c.sndBuf = append(c.sndBuf, data...)
	c.flushLocked()
	return len(data), nil
}

// flushLocked segments sndBuf while the in-flight window has room.
func (c *TCPConn) flushLocked() {
	for c.state == stateEstablished && len(c.sndBuf) > 0 &&
		len(c.inFlight) < tcpWindowCap {
		n := minInt(tcpMaxSeg, len(c.sndBuf))
		room := tcpWindowCap - len(c.inFlight)
		if n > room {
			n = room
		}
		payload := c.sndBuf[:n]
		c.sndBuf = c.sndBuf[n:]
		c.sendLocked(0, payload)
		if n == 0 {
			break
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Close performs an orderly teardown from either established side.
// The FIN is deferred until every queued/unacked byte has drained —
// closing after a window-limited Write must not abandon stream tail.
func (c *TCPConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case stateEstablished:
		c.flushLocked()
		c.finPending = true
		c.trySendFinLocked()
	case stateCloseWait:
		c.finPending = true
		c.trySendFinLocked()
	case stateClosed:
		return nil
	default:
		return ErrClosed
	}
	return nil
}

// trySendFinLocked emits the deferred FIN once sndBuf+inFlight are empty.
func (c *TCPConn) trySendFinLocked() {
	if !c.finPending || len(c.sndBuf) > 0 || len(c.inFlight) > 0 {
		return
	}
	switch c.state {
	case stateEstablished:
		c.state = stateFinWait
		c.sendLocked(TCPFin, nil)
	case stateCloseWait:
		c.state = stateLastAck
		c.sendLocked(TCPFin, nil)
	}
}

// handle processes one inbound segment (stack pump goroutine).
func (c *TCPConn) handle(seg *TCPSegment) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if seg.Flags&TCPRst != 0 && c.state != stateClosed && c.state != stateListen {
		c.state = stateClosed
		c.err = ErrConnReset
		return
	}

	// cumulative ACK processing
	if seg.Flags&TCPAck != 0 && seqGE(seg.Ack, c.sndUNA) && seqGT(seg.Ack, c.sndUNA) &&
		seqGE(c.sndNXT, seg.Ack) {
		acked := int(seg.Ack - c.sndUNA)
		if acked > 0 {
			if acked >= len(c.inFlight) {
				c.inFlight = nil
			} else {
				c.inFlight = c.inFlight[acked:]
			}
			c.sndUNA = seg.Ack
			if c.state == stateEstablished {
				c.flushLocked() // window opened: push queued bytes
			}
			c.trySendFinLocked() // drain may have completed
		}
	}

	switch c.state {
	case stateListen:
		// passive open: SYN (no ACK) creates the half-open connection;
		// this conn IS the child (one connection per port in v1). The
		// stack set RemoteIP/RemotePort before dispatching here.
		if seg.Flags&TCPSyn != 0 && seg.Flags&TCPAck == 0 {
			c.rcvNXT = seg.Seq + 1
			c.state = stateSynRcvd
			c.sendLocked(TCPSyn|TCPAck, nil)
		}
	case stateSynSent:
		if seg.Flags&TCPSyn != 0 && seg.Flags&TCPAck != 0 {
			c.rcvNXT = seg.Seq + 1
			c.sndUNA = seg.Ack
			c.inFlight = nil
			c.state = stateEstablished
			c.sendLocked(TCPAck, nil)
			c.flushLocked()
		}
	case stateSynRcvd:
		if seg.Flags&TCPAck != 0 {
			c.state = stateEstablished
			if c.l != nil {
				select {
				case c.l.q <- c:
				default:
				}
			}
		}
	case stateEstablished:
		// in-order payload first — a FIN carrying data must not drop it
		if len(seg.Payload) > 0 && seg.Seq == c.rcvNXT {
			c.rcvBuf = append(c.rcvBuf, seg.Payload...)
			c.rcvNXT += uint32(len(seg.Payload))
			c.sendLocked(TCPAck, nil)
			}
		if len(seg.Payload) > 0 && seg.Seq != c.rcvNXT {
			}
		if seg.Flags&TCPFin != 0 && seg.Seq+uint32(len(seg.Payload)) == c.rcvNXT {
			c.rcvNXT++
			c.sendLocked(TCPAck, nil)
			c.state = stateCloseWait
			c.err = ErrRemoteClosed
		}
	case stateFinWait:
		// late payload while we drain our own side: deliver it too
		if len(seg.Payload) > 0 && seg.Seq == c.rcvNXT {
			c.rcvBuf = append(c.rcvBuf, seg.Payload...)
			c.rcvNXT += uint32(len(seg.Payload))
			c.sendLocked(TCPAck, nil)
			}
		if len(seg.Payload) > 0 && seg.Seq != c.rcvNXT {
			}
		if seg.Flags&TCPFin != 0 && seg.Seq+uint32(len(seg.Payload)) == c.rcvNXT {
			c.rcvNXT++
			c.sendLocked(TCPAck, nil)
		}
		// close completes when our own FIN is acknowledged AND we've
		// ACKed theirs (simultaneous close collapses here too).
		finAcked := !c.finSent || seqGE(c.sndUNA, c.finSeq+1)
		theirFinSeen := seg.Flags&TCPFin != 0 || c.finAckedTheirFin
		if seg.Flags&TCPFin != 0 {
			c.finAckedTheirFin = true
		}
		if finAcked && theirFinSeen {
			c.state = stateClosed
			c.err = ErrClosed
		}
	case stateLastAck:
		if seg.Flags&TCPAck != 0 && c.finSent && seqGE(c.sndUNA, c.finSeq+1) {
			c.state = stateClosed
			c.err = ErrClosed
		}
	}

	// A segment completing the handshake (SYN-RCVD→ESTABLISHED above)
	// may itself carry payload: deliver here too. Idempotent — if the
	// switch already consumed it, rcvNXT has moved past seg.Seq.
	if c.state == stateEstablished && len(seg.Payload) > 0 && seg.Seq == c.rcvNXT {
		c.rcvBuf = append(c.rcvBuf, seg.Payload...)
		c.rcvNXT += uint32(len(seg.Payload))
		c.sendLocked(TCPAck, nil)
	}
}

// ---- stack ----

// TCPStack owns all connections for one Stack.
type TCPStack struct {
	s *Stack

	mu            sync.Mutex
	conns         map[uint16]*TCPConn // by local port (listeners + conns)
	nextEphemeral uint16
}

func NewTCPStack(s *Stack) *TCPStack {
	return &TCPStack{s: s, conns: make(map[uint16]*TCPConn), nextEphemeral: 49152}
}

func (t *TCPStack) handle(pkt *IP4Packet) {
	seg, err := ParseTCP(pkt.Payload)
	if err != nil {
		return
	}
	t.mu.Lock()
	c, ok := t.conns[seg.DstPort]
	t.mu.Unlock()
	if !ok {
		return
	}
	if c.State() == "LISTEN" && seg.Flags&TCPSyn != 0 && seg.Flags&TCPAck == 0 {
		// bind the half-open conn to this peer before handshake continues
		c.mu.Lock()
		c.RemoteIP, c.RemotePort = pkt.Src, seg.SrcPort
		c.mu.Unlock()
	}
	c.handle(seg)
}

// Listen binds port and returns the connection queue.
func (t *TCPStack) Listen(port uint16) (*TCPListener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.conns[port]; exists {
		return nil, errors.New("net: port in use")
	}
	l := &TCPListener{stack: t, port: port, q: make(chan *TCPConn, 16)}
	t.conns[port] = &TCPConn{stack: t, LocalPort: port, state: stateListen, l: l}
	return l, nil
}

func (t *TCPStack) ephemeral() uint16 {
	for {
		p := t.nextEphemeral
		t.nextEphemeral++
		if _, used := t.conns[p]; !used {
			return p
		}
	}
}

// Dial opens an active connection to raddr:rport from this host.
func (t *TCPStack) Dial(laddr IP4, raddr IP4, rport uint16) (*TCPConn, error) {
	t.mu.Lock()
	local := t.ephemeral()
	conn := &TCPConn{
		stack: t, LocalIP: laddr, LocalPort: local,
		RemoteIP: raddr, RemotePort: rport,
		state:  stateSynSent,
		sndUNA: 1000, sndNXT: 1000, rcvNXT: 0, // ISS convention for v1
	}
	t.conns[local] = conn
	t.mu.Unlock()

	conn.mu.Lock()
	conn.sendLocked(TCPSyn, nil)
	conn.mu.Unlock()
	return conn, nil
}

// ---- listener ----

// TCPListener accepts inbound connections on one port.
type TCPListener struct {
	stack *TCPStack
	port  uint16
	q     chan *TCPConn
}

// Accept blocks until a handshake completes (driver must pump both
// stacks; tests use waitFor helpers). Returns the established conn.
func (l *TCPListener) Accept() (*TCPConn, error) {
	select {
	case c := <-l.q:
		return c, nil
	default:
		return nil, errors.New("net: no pending connection")
	}
}

// Pending reports whether a completed handshake awaits Accept (peek —
// never consumes the queue entry).
func (l *TCPListener) Pending() bool {
	return len(l.q) > 0
}
