//go:build !wasip1

package main

import (
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// runTwoStacks starts a net service backed by a Stack and a peer stack
// on a shared segment, pumping both for a bounded period.
func runTwoStacks(t *testing.T, stackIP string) (*lib.NetClient, *Stack, *Stack) {
	t.Helper()
	fk := lib.NewFakeKernel()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	seg := NewSegment()
	srv := NewStack(mustMAC(t, "02:00:00:00:00:02"), MustIP("10.0.0.2"), seg.Attach())
	peer := NewStack(mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), seg.Attach())
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			srv.pump()
			peer.pump()
			time.Sleep(50 * time.Microsecond)
		}
	}()
	go ServeNet(fk, srv, stop)
	pumpUntil(t, func() bool { return fk.HasPort(lib.NameNet) }, "net port missing")

	nc, err := lib.BindNet(fk, "test")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(20000)
	return nc, srv, peer
}

// TestPhase16PingRoundTrip exercises the new NetOpPing (op 6) end-to-end:
// the guest sends an ICMP echo request, the stack ARP-resolves, frames
// the ICMP packet on the wire, the peer receives it, and the reply
// arrives back at the guest with a valid RTT.
func TestPhase16PingRoundTrip(t *testing.T) {
	nc, _, peer := runTwoStacks(t, "10.0.0.2")
	// Peer must be ready to answer echo requests.
	go peer.pump() // already pumped in runTwoStacks
	rtt, data, err := nc.Ping([4]byte{10, 0, 0, 1}, 0x1234, 1, []byte("hello-ping"))
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if rtt > 5000 {
		t.Errorf("RTT %dms unreasonably high", rtt)
	}
	if len(data) != len("hello-ping") {
		t.Errorf("reply payload length: got %d want %d", len(data), len("hello-ping"))
	}
}

// TestPhase16StackIP verifies NetOpStatus(NetStIP) returns the server's
// own IPv4 address as raw 4 bytes.
func TestPhase16StackIP(t *testing.T) {
	nc, _, _ := runTwoStacks(t, "10.0.0.2")
	ip, err := nc.StackIP()
	if err != nil {
		t.Fatal(err)
	}
	want := [4]byte{10, 0, 0, 2}
	if ip != want {
		t.Errorf("StackIP: got %v want %v", ip, want)
	}
}

// TestPhase16StackStats verifies NetOpStatus(NetStStats) returns the
// four protocol-layer RX counters. After a successful ping, both the
// IPv4 and ICMP counters must be nonzero.
func TestPhase16StackStats(t *testing.T) {
	nc, _, _ := runTwoStacks(t, "10.0.0.2")
	// Trigger at least one ICMP frame so the counters move.
	nc.Ping([4]byte{10, 0, 0, 1}, 0x1234, 2, []byte("stats-ping"))
	// Give the pumper a moment to drain the wire.
	time.Sleep(20 * time.Millisecond)
	_, _, ipv4, icmp, err := nc.StackStats()
	if err != nil {
		t.Fatal(err)
	}
	if ipv4 == 0 {
		t.Error("ipv4_rx still 0 after ping")
	}
	if icmp == 0 {
		t.Error("icmp_rx still 0 after ping")
	}
}

// TestPhase16ActiveSockets verifies NetOpStatus(NetStSocks) returns the
// open socket ids after a successful TCP dial.
func TestPhase16ActiveSockets(t *testing.T) {
	nc, _, peer := runTwoStacks(t, "10.0.0.2")
	ln, err := peer.tcp.Listen(9090)
	if err != nil {
		t.Fatal(err)
	}
	sock, err := nc.OpenTCPOutbound()
	if err != nil {
		t.Fatal(err)
	}
	// The OPEN reply is sent only after the server inserts the socket
	// into its map, so by the time OpenTCPOutbound returns the socket
	// MUST be visible. Read ActiveSockets directly.
	ids, err := nc.ActiveSockets()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		if id == sock {
			found = true
		}
	}
	if !found {
		t.Fatalf("socket %d not in active list %v (after OPEN reply)", sock, ids)
	}
	if err := nc.Connect(sock, [4]byte{10, 0, 0, 1}, 9090); err != nil {
		t.Fatal(err)
	}
	pumpUntil(t, func() bool {
		c, err := ln.Accept()
		return err == nil && c != nil
	}, "peer never accepted")
	nc.Close(sock)
}
