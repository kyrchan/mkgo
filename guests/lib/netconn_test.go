package kern

import (
	"bytes"
	"testing"
	"time"
)

// mockNetServer is a minimal in-process net endpoint for testing the
// NetConn/NetListener helpers without a real stack. It speaks the v0
// wire protocol: OPEN/CONNECT/SEND/RECV/COPEN/CLOSE on the "net" port.
type mockNetServer struct {
	b         *Bus
	h         Handle
	conns     map[uint16]*mockConn
	nextID    uint16
	acceptQ   chan uint16
}

type mockConn struct {
	sock     uint16
	peer     string
	recvBuf  []byte
	closed   bool
}

func newMockNetServer(b *Bus) *mockNetServer {
	h := b.PortCreate(NameNet)
	if h == InvalidHandle {
		panic("mockNetServer: failed to create net port")
	}
	s := &mockNetServer{
		b:       b,
		h:       h,
		conns:   make(map[uint16]*mockConn),
		nextID:  1,
		acceptQ: make(chan uint16, 16),
	}
	go s.serve()
	return s
}

func (s *mockNetServer) serve() {
	buf := make([]byte, MaxMsg)
	for {
		n := s.b.PortRecv(s.h, buf)
		if n <= 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		rep, rname := s.handle(req)
		if rep != nil && rname != "" {
			// reply to the client's inbox port
			rh := s.b.PortBind(rname)
			if rh == InvalidHandle {
				rh = s.b.PortCreate(rname)
			}
			if rh != InvalidHandle {
				s.b.PortSend(rh, rep)
			}
		}
	}
}

func (s *mockNetServer) handle(req []byte) ([]byte, string) {
	if len(req) < CanonicalHeaderLen+2 {
		return nil, ""
	}
	op := Get16(req[0:2])
	seq := Get16(req[2:4])
	rname := string(bytes.TrimRight(req[8:24], "\x00"))
	payload := req[CanonicalHeaderLen:]

	switch op {
	case NetOpOpen:
		if len(payload) < 4 {
			return nil, ""
		}
		kind := Get16(payload[0:2])
		id := s.nextID
		s.nextID++
		s.conns[id] = &mockConn{sock: id}
		if kind == NetKindTCP {
			// queue for accept
		}
		return openReply(seq, id), rname
	case NetOpConn:
		if len(payload) < 8 {
			return nil, ""
		}
		id := Get16(payload[0:2])
		if c, ok := s.conns[id]; ok {
			c.peer = "connected"
		}
		return statusReply(seq, 0), rname
	case NetOpSend:
		if len(payload) < 4 {
			return nil, ""
		}
		id := Get16(payload[0:2])
		n := int(Get16(payload[2:4]))
		if len(payload) >= 4+n {
			if c, ok := s.conns[id]; ok {
				c.recvBuf = append(c.recvBuf, payload[4:4+n]...)
			}
		}
		return statusReply(seq, 0), rname
	case NetOpRecv:
		if len(payload) < 4 {
			return nil, ""
		}
		id := Get16(payload[0:2])
		max := int(Get16(payload[2:4]))
		c, ok := s.conns[id]
		if !ok {
			return recvReply(seq, 0, nil), rname
		}
		if max == 0 {
			if c.peer == "" {
				return statusReply(seq, -3), rname
			}
			return recvReply(seq, 0, nil), rname
		}
		if len(c.recvBuf) == 0 {
			if c.closed {
				return statusReply(seq, -3), rname
			}
			return recvReply(seq, 0, nil), rname
		}
		got := max
		if got > len(c.recvBuf) {
			got = len(c.recvBuf)
		}
		data := c.recvBuf[:got]
		c.recvBuf = c.recvBuf[got:]
		return recvReply(seq, uint16(got), data), rname
	case NetOpClose:
		if len(payload) < 2 {
			return nil, ""
		}
		id := Get16(payload[0:2])
		if c, ok := s.conns[id]; ok {
			c.closed = true
		}
		return statusReply(seq, 0), rname
	}
	return nil, ""
}

func openReply(seq, id uint16) []byte {
	rep := make([]byte, 30)
	Put16(rep[2:], seq)
	Put32(rep[24:], 0)
	Put16(rep[28:], id)
	return rep
}

func statusReply(seq uint16, st int32) []byte {
	rep := make([]byte, 28)
	Put16(rep[2:], seq)
	Put32(rep[24:], uint32(st))
	return rep
}

func recvReply(seq, got uint16, data []byte) []byte {
	rep := make([]byte, 30+len(data))
	Put16(rep[2:], seq)
	Put32(rep[24:], 0)
	Put16(rep[28:], got)
	copy(rep[30:], data)
	return rep
}

// TestNetConnWrapper verifies the high-level NetConn/NetListener helpers
// against a mock net server.
func TestNetConnWrapper(t *testing.T) {
	b := NewBus()
	newMockNetServer(b)

	nc, err := BindNet(b, "test")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(1000)

	// DialTCP to a port — mock accepts any connect.
	ip := [4]byte{10, 0, 0, 1}
	conn, err := DialTCP(nc, ip, 7777)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}

	// Write through the conn.
	msg := []byte("hello")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The mock echoes sent data back into the recv buffer.
	buf := make([]byte, 16)
	var got []byte
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		got = append(got, buf[:n]...)
		if len(got) >= len(msg) {
			break
		}
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("expected %q, got %q", msg, got)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNetListenerWrapper verifies ListenTCP + Accept against the mock.
func TestNetListenerWrapper(t *testing.T) {
	b := NewBus()
	newMockNetServer(b)

	nc, err := BindNet(b, "listener")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(1000)

	ln, err := ListenTCP(nc, 8888)
	if err != nil {
		t.Fatalf("ListenTCP: %v", err)
	}
	if ln == nil {
		t.Fatal("nil listener")
	}

	// Accept with no pending connection should eventually time out
	// (budget exhausted). We just verify it doesn't panic or hang forever.
	done := make(chan struct{})
	go func() {
		_, _ = ln.Accept()
		close(done)
	}()

	select {
	case <-done:
		// Accept returned (with error or conn) — fine.
	case <-time.After(2 * time.Second):
		t.Fatal("Accept hung with no pending connection")
	}
}

var _ = bytes.Equal
