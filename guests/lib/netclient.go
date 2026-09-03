package kern

import (
	"errors"
)

// NetClient — guest-side helper for the "net" socket service
// (services/net, wire contract in services/ABI-NOTES.md §10). Mirrors
// the server ops OPEN/CONNECT/SEND/RECV/CLOSE over the v1.1 canonical
// header. Payload integers are LE; IP addresses are raw 4-byte arrays.

const (
	NetOpOpen  uint16 = 1
	NetOpConn  uint16 = 2
	NetOpSend  uint16 = 3
	NetOpRecv  uint16 = 4
	NetOpClose uint16 = 5
	NetOpPing   uint16 = 6
	NetOpStatus uint16 = 7

	NetKindTCP uint16 = 0
	NetKindUDP uint16 = 1

	// Phase 16: NetOpStatus query selectors.
	NetStIP    uint16 = 0
	NetStStats uint16 = 1
	NetStSocks uint16 = 2

	netStOK   = int32(0)
	netStNoSk = int32(-1)
	netStBad  = int32(-2)
	netStSt   = int32(-3)
)

var (
	ErrNetNoSuchSock = errors.New("net: no such socket")
	ErrNetBadOp      = errors.New("net: bad request")
	ErrNetState      = errors.New("net: socket state")
)

// NetClient talks to the "net" service over Inbox mode.
type NetClient struct {
	c *Client
	h Handle
}

// BindNet binds the net endpoint and prepares the reply channel.
func BindNet(k Kernel, roleTag string) (*NetClient, error) {
	h := k.PortBind(NameNet)
	if h == InvalidHandle {
		return nil, ErrBadHandle
	}
	c, err := NewInboxClient(k, roleTag)
	if err != nil {
		return nil, err
	}
	return &NetClient{c: c, h: h}, nil
}

// SetBudget overrides the reply poll budget (tests).
func (n *NetClient) SetBudget(b int) { n.c.Budget = b }

// Yield cooperatively reschedules (delegates to the kernel).
func (n *NetClient) Yield() { n.c.k.Yield() }

func (n *NetClient) call(op uint16, payload []byte) ([]byte, error) {
	rep, err := n.c.InboxRequest(n.h, op, payload)
	if err != nil {
		return nil, err
	}
	if len(rep) < 28 {
		return nil, ErrShort
	}
	return rep, nil
}

func statusOf(rep []byte) error {
	switch st := int32(Get32(rep[24:28])); st {
	case netStOK:
		return nil
	case netStNoSk:
		return ErrNetNoSuchSock
	case netStBad:
		return ErrNetBadOp
	default:
		return ErrNetState
	}
}

// OpenTCPListen opens a listening TCP socket on port.
func (n *NetClient) OpenTCPListen(port uint16) (uint16, error) {
	return n.open(NetKindTCP, port)
}

// OpenUDP binds a UDP socket on port (0 = send-only until Connect).
func (n *NetClient) OpenUDP(port uint16) (uint16, error) {
	return n.open(NetKindUDP, port)
}

// OpenTCPOutbound allocates an outbound TCP socket (Connect next).
func (n *NetClient) OpenTCPOutbound() (uint16, error) {
	return n.open(NetKindTCP, 0)
}

func (n *NetClient) open(kind, port uint16) (uint16, error) {
	pl := make([]byte, 4)
	Put16(pl[0:2], kind)
	Put16(pl[2:4], port)
	rep, err := n.call(NetOpOpen, pl)
	if err != nil {
		return 0, err
	}
	if err := statusOf(rep); err != nil {
		return 0, err
	}
	if len(rep) < 30 {
		return 0, ErrShort
	}
	return Get16(rep[28:30]), nil
}

// Connect dials raddr:rport from an outbound TCP socket or sets the UDP
// default peer. ip is raw 4 bytes (no endianness).
func (n *NetClient) Connect(sock uint16, ip [4]byte, rport uint16) error {
	pl := make([]byte, 8)
	Put16(pl[0:2], sock)
	copy(pl[2:6], ip[:])
	Put16(pl[6:8], rport)
	rep, err := n.call(NetOpConn, pl)
	if err != nil {
		return err
	}
	return statusOf(rep)
}

// Send writes data through the socket (chunked to fit one datagram).
func (n *NetClient) Send(sock uint16, data []byte) (int, error) {
	sent := 0
	for sent < len(data) || len(data) == 0 && sent == 0 {
		chunk := len(data) - sent
		if max := MaxMsg - CanonicalHeaderLen - 4; chunk > max {
			chunk = max
		}
		if chunk > 0xffff {
			chunk = 0xffff
		}
		pl := make([]byte, 4, 4+chunk)
		Put16(pl[0:2], sock)
		Put16(pl[2:4], uint16(chunk))
		pl = append(pl, data[sent:sent+chunk]...)

		rep, err := n.call(NetOpSend, pl)
		if err != nil {
			return sent, err
		}
		if err := statusOf(rep); err != nil {
			return sent, err
		}
		sent += chunk
		if len(data) == 0 {
			break // zero-length send: single validation round
		}
	}
	return sent, nil
}

// Recv reads up to len(buf) bytes from the socket; returns copied count.
// got=0 with nil error means "nothing buffered yet".
func (n *NetClient) Recv(sock uint16, buf []byte) (int, error) {
	cnt := len(buf)
	if cnt == 0 {
		return 0, nil
	}
	if cnt > 0xffff {
		cnt = 0xffff
	}
	pl := make([]byte, 4)
	Put16(pl[0:2], sock)
	Put16(pl[2:4], uint16(cnt))
	rep, err := n.call(NetOpRecv, pl)
	if err != nil {
		return 0, err
	}
	if err := statusOf(rep); err != nil {
		return 0, err
	}
	if len(rep) < 30 {
		return 0, ErrShort
	}
	got := int(Get16(rep[28:30]))
	if got > len(buf) {
		got = len(buf)
	}
	return copy(buf[:got], rep[30:30+got]), nil
}

// Close tears the socket down.
func (n *NetClient) Close(sock uint16) error {
	pl := make([]byte, 2)
	Put16(pl, sock)
	rep, err := n.call(NetOpClose, pl)
	if err != nil {
		return err
	}
	return statusOf(rep)
}

// Ping sends an ICMP echo request to dst with id/seq and payload; returns
// the reply's RTT in ms and payload bytes. Uses the kernel clock when
// available (host tests: returns 0).
func (n *NetClient) Ping(dst [4]byte, id, seq uint16, payload []byte) (uint16, []byte, error) {
	pl := make([]byte, 6, 6+len(payload))
	Put16(pl[0:2], id)
	Put16(pl[2:4], seq)
	Put16(pl[4:6], uint16(len(payload)))
	pl = append(pl, payload...)
	rep, err := n.call(NetOpPing, pl)
	if err != nil {
		return 0, nil, err
	}
	if err := statusOf(rep); err != nil {
		return 0, nil, err
	}
	if len(rep) < 30 {
		return 0, nil, ErrShort
	}
	rtt := Get16(rep[28:30])
	data := append([]byte(nil), rep[30:]...)
	return rtt, data, nil
}

// StackIP returns the stack's own IPv4 address (4 raw bytes).
func (n *NetClient) StackIP() ([4]byte, error) {
	pl := make([]byte, 2)
	Put16(pl, NetStIP)
	rep, err := n.call(NetOpStatus, pl)
	if err != nil {
		return [4]byte{}, err
	}
	if err := statusOf(rep); err != nil {
		return [4]byte{}, err
	}
	if len(rep) < 32 {
		return [4]byte{}, ErrShort
	}
	var ip [4]byte
	copy(ip[:], rep[28:32])
	return ip, nil
}

// StackStats returns (eth_rx, arp_rx, ipv4_rx, icmp_rx).
func (n *NetClient) StackStats() (uint64, uint64, uint64, uint64, error) {
	pl := make([]byte, 2)
	Put16(pl, NetStStats)
	rep, err := n.call(NetOpStatus, pl)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if err := statusOf(rep); err != nil {
		return 0, 0, 0, 0, err
	}
	if len(rep) < 60 {
		return 0, 0, 0, 0, ErrShort
	}
	return Get64(rep[28:36]), Get64(rep[36:44]), Get64(rep[44:52]), Get64(rep[52:60]), nil
}

// ActiveSockets returns the list of open socket ids on the stack.
func (n *NetClient) ActiveSockets() ([]uint16, error) {
	pl := make([]byte, 2)
	Put16(pl, NetStSocks)
	rep, err := n.call(NetOpStatus, pl)
	if err != nil {
		return nil, err
	}
	if err := statusOf(rep); err != nil {
		return nil, err
	}
	if len(rep) < 28 {
		return nil, ErrShort
	}
	body := rep[28:]
	out := make([]uint16, len(body)/2)
	for i := range out {
		out[i] = Get16(body[i*2 : i*2+2])
	}
	return out, nil
}

// NetConn is a stream-oriented wrapper over a TCP socket id, providing
// io.ReadWriteCloser semantics for guest code. It is NOT safe for
// concurrent use; guests that need concurrency should serialize.
type NetConn struct {
	nc   *NetClient
	sock uint16
}

// DialTCP opens a TCP connection to ip:rport and returns a NetConn.
func DialTCP(nc *NetClient, ip [4]byte, rport uint16) (*NetConn, error) {
	s, err := nc.OpenTCPOutbound()
	if err != nil {
		return nil, err
	}
	if err := nc.Connect(s, ip, rport); err != nil {
		_ = nc.Close(s)
		return nil, err
	}
	return &NetConn{nc: nc, sock: s}, nil
}

// ListenTCP binds a TCP listener on port and returns a NetListener.
func ListenTCP(nc *NetClient, port uint16) (*NetListener, error) {
	s, err := nc.OpenTCPListen(port)
	if err != nil {
		return nil, err
	}
	return &NetListener{nc: nc, sock: s}, nil
}

// Read implements io.Reader.
func (c *NetConn) Read(p []byte) (int, error) {
	return c.nc.Recv(c.sock, p)
}

// Write implements io.Writer.
func (c *NetConn) Write(p []byte) (int, error) {
	return c.nc.Send(c.sock, p)
}

// Close tears down the underlying socket.
func (c *NetConn) Close() error {
	return c.nc.Close(c.sock)
}

// NetListener accepts inbound TCP connections on a bound port.
type NetListener struct {
	nc   *NetClient
	sock uint16
}

// Accept blocks until a handshake completes; returns a NetConn over the
// same socket id (the server routes subsequent recvs to the accepted
// child). Polls with zero-byte RECVs; the caller must ensure the
// NetClient has budget for the reply.
func (l *NetListener) Accept() (*NetConn, error) {
	for {
		// A zero-byte RECV triggers the server to pull the accepted
		// child; status=errState means none ready yet.
		_, err := l.nc.Recv(l.sock, make([]byte, 0))
		if err == nil {
			return &NetConn{nc: l.nc, sock: l.sock}, nil
		}
		if err != ErrNetState {
			return nil, err
		}
		l.nc.Yield()
	}
}
