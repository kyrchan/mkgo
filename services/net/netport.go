package main

import (
	"runtime"
	"os"
	"strconv"
	"sync"

	lib "kernel.lane/guests/lib"
)

// "net" well-known port server (AGENTS.md Phase 9): socket API over §1
// datagrams using the v1.1 canonical header. Wire contract (lane-local,
// services/ABI-NOTES.md §10) — payload after the 24-byte header:
//
//	OPEN   {u16 kind(0=tcp,1=udp), u16 port}  -> {i32 st, u16 sock}
//	       port=0 → outbound socket (CONNECT later); nonzero → listen/bind
//	CONNECT{u16 sock, u32 ipBE, u16 port}     -> {i32 st}
//	SEND   {u16 sock, u16 len, data[len]}     -> {i32 st}
//	RECV   {u16 sock, u16 max}                -> {i32 st, u16 got, data[got]}
//	CLOSE  {u16 sock}                         -> {i32 st}
//
// status: 0 ok; <0 error. RECV with an empty buffer is status 0/got=0.
// Replies go to the requester's rname port under the canonical header.

const (
	NetOpOpen  uint16 = 1
	NetOpConn  uint16 = 2
	NetOpSend  uint16 = 3
	NetOpRecv  uint16 = 4
	NetOpClose uint16 = 5

	// Phase 16: ICMP ping + stack status queries over the same port.
	NetOpPing   uint16 = 6 // {u16 id, u16 seq, u16 datalen, data[datalen]} -> {i32 st, u16 rtt_ms}
	NetOpStatus uint16 = 7 // {u16 what} -> {i32 st, body}
	// what values for NetOpStatus
	NetStIP    uint16 = 0 // body: 4 bytes IP
	NetStStats uint16 = 1 // body: 4*u64 (eth/arp/ipv4/icmp rx)
	NetStSocks uint16 = 2 // body: nsocks*u16 + nconns*u16

	NetKindTCP uint16 = 0
	NetKindUDP uint16 = 1

	netStatusOK = int32(0)

	errNoSuchSock = int32(-1)
	errBadOp      = int32(-2)
	errState      = int32(-3)

	// Phase 16: reply body sizes (after 28-byte canonical header).
	pingBodyLen   = 2 + 256 // rtt_ms + echo payload (max 256)
)

type netSocket struct {
	kind     uint16
	tcp      *TCPConn
	listen   *TCPListener
	udpQ     *UDPQueue
	udpPort  uint16
	peerIP   IP4
	peerPort uint16
}

// NetServer serves the "net" port over one Stack.
type NetServer struct {
	k     lib.Kernel
	stack *Stack

	mu      sync.Mutex
	sockets map[uint16]*netSocket
	nextID  uint16
}

func ServeNet(k lib.Kernel, s *Stack, stop <-chan struct{}) {
	ns := &NetServer{k: k, stack: s, sockets: make(map[uint16]*netSocket)}

	h := k.PortCreate(lib.NameNet)
	for h == lib.InvalidHandle {
		h = k.PortBind(lib.NameNet)
		if h != lib.InvalidHandle {
			break
		}
		if stoppedNet(stop) {
			return
		}
		k.Yield()
	}

	buf := make([]byte, lib.MaxMsg)
	replies := lib.NewReplyBook(k)
	for {
		s.pump() // process rx frames from §6 window EVERY iteration

		n := k.PortRecv(h, buf)
		if n >= lib.CanonicalHeaderLen+2 {
			hdr, _ := lib.ParseHeader(buf[:int(n)])
			if hdr.RNam != "" {
				rep := ns.dispatch(hdr.Op, hdr.Seq,
					buf[lib.CanonicalHeaderLen:int(n)])
				if rep != nil {
					if rh, err := replies.Bind(hdr.RNam); err == nil {
						k.PortSend(rh, rep)
					}
				}
			}
			continue // datagram handled: loop hot path
		}
		// CRITICAL on wasip1: two-level yielding is required here.
		// k.Yield() surrenders THIS session's kernel quantum but returns
		// to the SAME goroutine -- it never unblocks the wire-pump
		// goroutine, which starves forever on Go's cooperative
		// single-threaded runtime. runtime.Gosched() hands control to
		// that goroutine; both levels are needed every idle pass.
		runtime.Gosched()
		k.Yield()
	}
}

func stoppedNet(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (ns *NetServer) sock(id uint16) *netSocket {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return ns.sockets[id]
}

// dispatch handles one request; reply carries the canonical header.
func (ns *NetServer) dispatch(op, seq uint16, payload []byte) []byte {
	var status int32
	var body []byte

	switch op {
	case NetOpOpen:
		if len(payload) < 4 {
			status = errBadOp
			break
		}
		kind := lib.Get16(payload[0:2])
		port := lib.Get16(payload[2:4])
		sk := &netSocket{kind: kind}
		switch kind {
		case NetKindTCP:
			if port != 0 {
				ln, err := ns.stack.tcp.Listen(port)
				if err != nil {
					status = errState
					break
				}
				sk.listen = ln
			}
		case NetKindUDP:
			sk.udpQ, sk.udpPort = ns.stack.udp.Bind(port)
		default:
			status = errBadOp
		}
		if status == netStatusOK {
			ns.mu.Lock()
			ns.nextID++
			id := ns.nextID
			ns.sockets[id] = sk
			ns.mu.Unlock()
			body = openBody(netStatusOK, id)
		}

	case NetOpConn:
		if len(payload) < 8 {
			status = errBadOp
			break
		}
		sk := ns.sock(lib.Get16(payload[0:2]))
		if sk == nil {
			status = errNoSuchSock
			break
		}
		var dip IP4
		copy(dip[:], payload[2:6])
		dport := lib.Get16(payload[6:8])
		sk.peerIP, sk.peerPort = dip, dport
		if sk.kind == NetKindTCP {
			conn, err := ns.stack.tcp.Dial(ns.stack.IP, dip, dport)
			if err != nil {
				status = errState
				break
			}
			sk.tcp = conn
		}
		status = netStatusOK

	case NetOpSend:
		if len(payload) < 4 {
			status = errBadOp
			break
		}
		sk := ns.sock(lib.Get16(payload[0:2]))
		n := int(lib.Get16(payload[2:4]))
		if sk == nil || 4+n > len(payload) {
			status = errNoSuchSock
			break
		}
		data := payload[4 : 4+n]
		switch sk.kind {
		case NetKindUDP:
			if err := ns.stack.udp.SendTo(sk.udpPort, sk.peerIP, sk.peerPort, data); err != nil {
				os.Stdout.WriteString("[net] sendto err: " + err.Error() +
					" port=" + strconv.Itoa(int(sk.udpPort)) + "\n")
				status = errState
			}
		default:
			if sk.tcp == nil {
				status = errState
				break
			}
			if _, err := sk.tcp.Write(data); err != nil {
				status = errState
			}
		}

	case NetOpRecv:
		if len(payload) < 4 {
			status = errBadOp
			break
		}
		sk := ns.sock(lib.Get16(payload[0:2]))
		max := int(lib.Get16(payload[2:4]))
		if sk == nil || max == 0 && sk.kind == NetKindUDP {
			status = errNoSuchSock
			break
		}
		buf := make([]byte, max)
		got := 0
		switch sk.kind {
		case NetKindUDP:
			if d, ok := sk.udpQ.Recv(); ok {
				got = copy(buf, d)
			}
		default:
			if sk.listen != nil { // pull accepted children lazily
				if c, err := sk.listen.Accept(); err == nil {
					sk.tcp = c
				}
			}
			if sk.tcp == nil {
				status = errState
				break
			}
			got = sk.tcp.Recv(buf)
		}
		body = make([]byte, 2, 2+got)
		lib.Put16(body, uint16(got))
		body = append(body, buf[:got]...)
		status = netStatusOK

	case NetOpClose:
		if len(payload) < 2 {
			status = errBadOp
			break
		}
		id := lib.Get16(payload[0:2])
		ns.mu.Lock()
		sk := ns.sockets[id]
		delete(ns.sockets, id)
		ns.mu.Unlock()
		if sk == nil {
			status = errNoSuchSock
			break
		}
		if sk.kind == NetKindTCP && sk.tcp != nil {
			_ = sk.tcp.Close()
		}
		status = netStatusOK

	case NetOpPing:
		if len(payload) < 8 {
			status = errBadOp
			break
		}
		id := lib.Get16(payload[0:2])
		seq := lib.Get16(payload[2:4])
		dlen := int(lib.Get16(payload[4:6]))
		if 6+dlen > len(payload) {
			status = errBadOp
			break
		}
		data := payload[6 : 6+dlen]
		// Use a fixed TTL=64 and the stack's own IP as source.
		// Ping is synchronous: send echo request, wait for reply.
		start := uint64(0)
		if ns.k.HasClock() {
			start = ns.k.ClockMs()
		}
		// The stack handles ARP + ICMP echo/reply internally.
		r, err := ns.stack.Ping(ns.stack.IP, id, seq, data, 4000)
		if err != nil {
			status = errState
			break
		}
		var rtt uint16 = 0
		if ns.k.HasClock() {
			rtt = uint16(ns.k.ClockMs() - start)
		}
		body = make([]byte, 2+len(r.Data))
		lib.Put16(body, rtt)
		copy(body[2:], r.Data)
		status = netStatusOK

	case NetOpStatus:
		if len(payload) < 2 {
			status = errBadOp
			break
		}
		what := lib.Get16(payload[0:2])
		switch what {
		case NetStIP:
			body = append([]byte(nil), ns.stack.IP[:]...)
		case NetStStats:
			e, a, vi, ic := ns.stack.Stats()
			body = make([]byte, 32)
			lib.Put64(body[0:8], e)
			lib.Put64(body[8:16], a)
			lib.Put64(body[16:24], vi)
			lib.Put64(body[24:32], ic)
		case NetStSocks:
			ns.mu.Lock()
			body = make([]byte, len(ns.sockets)*2)
			i := 0
			for k := range ns.sockets {
				lib.Put16(body[i*2:i*2+2], k)
				i++
			}
			ns.mu.Unlock()
		default:
			status = errBadOp
		}

	default:
		return nil // unknown op: silent per §7 convention
	}

	rep := make([]byte, 28, 28+len(body))
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)
	lib.Put32(rep[24:], uint32(status))
	return append(rep, body...)
}

// openBody encodes the socket id LE (payload ints are guest-ABI LE).
func openBody(status int32, id uint16) []byte {
	b := make([]byte, 2)
	lib.Put16(b, id)
	return b
}

func eStr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
