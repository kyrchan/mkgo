package main

import (
	"bytes"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// netClient drives a NetServer through port requests.
type netClient struct {
	c *lib.Client
	h lib.Handle
}

func newNetClient(t *testing.T, k lib.Kernel) *netClient {
	t.Helper()
	h := k.PortBind(lib.NameNet)
	if h == lib.InvalidHandle {
		t.Fatal("bind net failed")
	}
	c, err := lib.NewInboxClient(k, "net")
	if err != nil {
		t.Fatal(err)
	}
	c.Budget = 40000
	return &netClient{c: c, h: h}
}

func (n *netClient) req(op uint16, payload []byte) ([]byte, error) {
	return n.c.InboxRequest(n.h, op, payload)
}

func (n *netClient) open(kind, port uint16) (uint16, int32, error) {
	pl := make([]byte, 4)
	lib.Put16(pl[0:2], kind) // payload ints are LE per the guest ABI
	lib.Put16(pl[2:4], port)
	rep, err := n.req(NetOpOpen, pl)
	if err != nil {
		return 0, -99, err
	}
	st := int32(lib.Get32(rep[24:]))
	var id uint16
	if len(rep) >= 30 {
		id = lib.Get16(rep[28:30])
	}
	return id, st, nil
}

func TestNetServerTCPRoundTrip(t *testing.T) {
	fk := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)

	seg := NewSegment()
	stackSrv := NewStack(mustMAC(t, "02:00:00:00:00:02"), MustIP("10.0.0.2"), seg.Attach())
	stackCli := NewStack(mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), seg.Attach())

	go func() { // wire pump for BOTH stacks
		for {
			select {
			case <-stop:
				return
			default:
			}
			stackCli.pump()
			stackSrv.pump()
			time.Sleep(50 * time.Microsecond)
		}
	}()

	go ServeNet(fk, stackSrv, stop)
	waitForCond(t, func() bool { return fk.HasPort(lib.NameNet) }, "net port missing")

	cli := newNetClient(t, fk)

	// server side: listening socket on :9000
	lsnID, st, err := cli.open(NetKindTCP, 9000)
	if err != nil || st != netStatusOK {
		t.Fatalf("open listen st=%d err=%v", st, err)
	}

	// client side socket + connect
	outID, st, err := cli.open(NetKindTCP, 0)
	if err != nil || st != netStatusOK {
		t.Fatalf("open outbound st=%d err=%v", st, err)
	}
	pl := make([]byte, 8)
	lib.Put16(pl[0:2], outID)
	copy(pl[2:6], []byte{10, 0, 0, 2}) // raw 4-byte IP (no endianness)
	lib.Put16(pl[6:8], 9000)
	if rep, err := cli.req(NetOpConn, pl); err != nil || int32(lib.Get32(rep[24:])) != netStatusOK {
		t.Fatalf("connect st/err: %v %v", rep, err)
	}

	// client speaks first through the outbound socket
	hello := []byte("hello from client")
	pl = make([]byte, 4+len(hello))
	lib.Put16(pl[0:2], outID)
	lib.Put16(pl[2:4], uint16(len(hello)))
	copy(pl[4:], hello)
	// handshake completes asynchronously; retry SEND until established
	waitForCond(t, func() bool {
		rep, err := cli.req(NetOpSend, pl)
		return err == nil && int32(lib.Get32(rep[24:])) == netStatusOK
	}, "client send never succeeded")

	// pull the accepted child out of the listening socket via RECV poll
	srvSock := uint16(0)
	waitForCond(t, func() bool {
		pl := make([]byte, 4)
		lib.Put16(pl[0:2], lsnID)
		lib.Put16(pl[2:4], 512)
		rep, err := cli.req(NetOpRecv, pl)
		if err != nil || len(rep) < 30 {
			return false
		}
		st := int32(lib.Get32(rep[24:]))
		got := int(lib.Get16(rep[28:30]))
		if st == netStatusOK && got > 0 {
			srvSock = lsnID // accepted child replaced the listener view
			return bytes.Equal(rep[30:30+got], hello)
		}
		return false
	}, "accepted stream never delivered")

	// server echoes back over the accepted child
	payload := bytes.Repeat([]byte("E"), 700) // multi-segment
	pl = make([]byte, 4+len(payload))
	lib.Put16(pl[0:2], srvSock)
	lib.Put16(pl[2:4], uint16(len(payload)))
	copy(pl[4:], payload)
	if rep, err := cli.req(NetOpSend, pl); err != nil || int32(lib.Get32(rep[24:])) != netStatusOK {
		t.Fatalf("send st=%v err=%v", rep, err)
	}

	var acc []byte
	waitForCond(t, func() bool {
		pl := make([]byte, 4)
		lib.Put16(pl[0:2], outID)
		lib.Put16(pl[2:4], 1024)
		rep, err := cli.req(NetOpRecv, pl)
		if err != nil || len(rep) < 30 {
			return false
		}
		if int32(lib.Get32(rep[24:])) == netStatusOK {
			got := int(lib.Get16(rep[28:30]))
			acc = append(acc, rep[30:30+got]...)
		}
		return bytes.Equal(acc, payload)
	}, "echo not received")

	// teardown both ends
	if _, err := cli.req(NetOpClose, []byte{byte(outID >> 8), byte(outID)}); err != nil {
		t.Fatal(err)
	}
}

func TestNetServerUDPDatagram(t *testing.T) {
	fk := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)

	seg := NewSegment()
	stackA := NewStack(mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), seg.Attach())
	stackB := NewStack(mustMAC(t, "02:00:00:00:00:02"), MustIP("10.0.0.2"), seg.Attach())
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			stackA.pump()
			stackB.pump()
			time.Sleep(50 * time.Microsecond)
		}
	}()
	go ServeNet(fk, stackB, stop)
	waitForCond(t, func() bool { return fk.HasPort(lib.NameNet) }, "net port missing")

	cli := newNetClient(t, fk)
	rxID, st, err := cli.open(NetKindUDP, 5000)
	if err != nil || st != netStatusOK {
		t.Fatalf("udp open st=%d err=%v", st, err)
	}

	// raw sender on the segment
	if !stackA.sink.Send(buildUDPFrame(mustMAC(t, "02:00:00:00:00:02"),
		mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), MustIP("10.0.0.2"), 4000, 5000, []byte("datagram!"))) {
		t.Fatal("raw udp send failed")
	}

	waitForCond(t, func() bool {
		pl := make([]byte, 4)
		lib.Put16(pl[0:2], rxID)
		lib.Put16(pl[2:4], 128)
		rep, err := cli.req(NetOpRecv, pl)
		if err != nil || len(rep) < 30 {
			return false // status-only reply (e.g. transient) or short
		}
		got := int(lib.Get16(rep[28:30]))
		return got == 9 && string(rep[30:30+got]) == "datagram!"
	}, "udp datagram lost")
}

// buildUDPFrame assembles a full ethernet+ip+udp frame for raw injection.
func buildUDPFrame(dstMAC, srcMAC MAC, srcIP, dstIP IP4, sp, dp uint16, data []byte) []byte {
	udp := &UDPDatagram{SrcPort: sp, DstPort: dp, Data: data}
	ip, err := (&IP4Packet{Src: srcIP, Dst: dstIP, Proto: IP4ProtoUDP,
		Payload: udp.Build()}).Build()
	if err != nil {
		panic(err)
	}
	return BuildEth(dstMAC, srcMAC, EthTypeIPv4, ip)
}
