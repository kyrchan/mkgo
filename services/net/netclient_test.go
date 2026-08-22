package main

import (
	"bytes"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// TestLibNetClientE2E drives the "net" service through the guest libc
// helper (deliverable 3): full TCP echo + UDP datagram round trips.
func TestLibNetClientE2E(t *testing.T) {
	fk := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)

	seg := NewSegment()
	stackSrv := NewStack(mustMAC(t, "02:00:00:00:00:02"), MustIP("10.0.0.2"), seg.Attach())
	stackPeer := NewStack(mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), seg.Attach())
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			stackSrv.pump()
			stackPeer.pump()
			time.Sleep(50 * time.Microsecond)
		}
	}()
	go ServeNet(fk, stackSrv, stop)
	waitForCond(t, func() bool { return fk.HasPort(lib.NameNet) }, "net port missing")

	// the remote peer is a plain TCP listener on stackPeer
	ln, err := stackPeer.tcp.Listen(7070)
	if err != nil {
		t.Fatal(err)
	}

	nc, err := lib.BindNet(fk, "app")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(40000)

	sock, err := nc.OpenTCPOutbound()
	if err != nil {
		t.Fatal(err)
	}

	var peerIP [4]byte = [4]byte{10, 0, 0, 1}
	if err := nc.Connect(sock, peerIP, 7070); err != nil {
		t.Fatal(err)
	}

	// wait for the peer-side handshake to complete (single Accept)
	var peerConn *TCPConn
	waitForCond(t, func() bool {
		if c, err := ln.Accept(); err == nil {
			peerConn = c
		}
		return peerConn != nil && peerConn.State() == "ESTABLISHED"
	}, "handshake never completed")

	msg := bytes.Repeat([]byte("N"), 1200) // multi-segment through window pacing
	if n, err := nc.Send(sock, msg); err != nil || n != len(msg) {
		t.Fatalf("send n=%d err=%v", n, err)
	}

	buf := make([]byte, len(msg)+64)
	total := 0
	waitForCond(t, func() bool {
		for {
			n := peerConn.Recv(buf[total:])
			if n == 0 {
				break
			}
			total += n
		}
		return total == len(msg)
	}, "peer never received stream")
	if !bytes.Equal(buf[:total], msg) {
		t.Fatal("stream corrupted")
	}

	// UDP path
	udpSock, err := nc.OpenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	q := stackPeer.udp.Bind(5555)
	if err := nc.Connect(udpSock, [4]byte{10, 0, 0, 1}, 5555); err != nil {
		t.Fatal(err)
	}
	if _, err := nc.Send(udpSock, []byte("dg")); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, func() bool {
		d, ok := q.Recv()
		return ok && string(d) == "dg"
	}, "udp via net client lost")

	if err := nc.Close(sock); err != nil {
		t.Fatal(err)
	}
}
