package main

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"time"
)

// routedPair wires two stacks through spy feeds and pumps both, so ARP
// between them resolves for real. `a` is the stack under test.
func routedPair(t *testing.T, netmask, gateway IP4) (a, b *Stack, fa, fb *spyFeed) {
	t.Helper()
	fa, fb = &spyFeed{}, &spyFeed{}
	macA := MAC{0x02, 0, 0, 0, 0, 0x01}
	macB := MAC{0x02, 0, 0, 0, 0, 0x02}
	a = NewStack(macA, MustIP("10.0.0.1"), fa)
	b = NewStack(macB, MustIP("10.0.0.2"), fb)
	a.Netmask = netmask
	a.Gateway = gateway
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			for {
				fr, ok := fa.pop()
				if !ok {
					break
				}
				fb.push(fr)
			}
			for {
				fr, ok := fb.pop()
				if !ok {
					break
				}
				fa.push(fr)
			}
			a.pump()
			b.pump()
			runtime.Gosched()
		}
	}()
	t.Cleanup(func() { once.Do(func() { close(stop) }) })
	return a, b, fa, fb
}

// lastDstMAC returns the destination MAC of the most recent frame a sent.
func lastDstMAC(t *testing.T, f *spyFeed) (MAC, *EthFrame, bool) {
	t.Helper()
	all := f.sentAll()
	if len(all) == 0 {
		return MAC{}, nil, false
	}
	frame := all[len(all)-1]
	ef, err := ParseEth(frame)
	if err != nil {
		t.Fatalf("parse eth: %v", err)
	}
	return ef.Dst, ef, true
}

func TestRouteOnLinkUsesDestinationMAC(t *testing.T) {
	a, _, fa, _ := routedPair(t, MustIP("255.255.255.0"), IP4{})
	if err := a.udp.SendTo(1234, MustIP("10.0.0.2"), 5678, []byte("hi")); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFrames(t, fa, 1)
	// Wait for the ARP exchange to finish, then look at the IP frame.
	deadline := time.Now().Add(10 * time.Second)
	var got MAC
	for time.Now().Before(deadline) {
		if mac, ef, ok := lastDstMAC(t, fa); ok && ef.Type == EthTypeIPv4 {
			got = mac
			if mac == (MAC{0x02, 0, 0, 0, 0, 0x02}) {
				return // addressed straight at the peer
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("on-link frame went to %v, want the destination's own MAC 02:00:00:00:00:02", got)
}

func TestRouteOffLinkUsesGatewayMAC(t *testing.T) {
	// 8.8.8.8 is outside 10.0.0.0/24, so the frame must be addressed to
	// the gateway (the peer here), not to 8.8.8.8 itself. Addressing an
	// off-subnet destination directly only ever worked because SLIRP
	// answers ARP for anything.
	a, _, fa, _ := routedPair(t, MustIP("255.255.255.0"), MustIP("10.0.0.2"))
	if err := a.udp.SendTo(1234, MustIP("8.8.8.8"), 53, []byte("q")); err != nil {
		t.Fatalf("send: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var got MAC
	for time.Now().Before(deadline) {
		if mac, ef, ok := lastDstMAC(t, fa); ok && ef.Type == EthTypeIPv4 {
			got = mac
			if mac == (MAC{0x02, 0, 0, 0, 0, 0x0f}) {
				t.Fatalf("frame was ARP-resolved to 8.8.8.8 directly: routing did not engage")
			}
			if mac == (MAC{0x02, 0, 0, 0, 0, 0x02}) {
				return // addressed to the gateway
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("off-link frame went to %v, want the gateway's MAC 02:00:00:00:00:02", got)
}

func TestRouteOffLinkWithoutGatewayFails(t *testing.T) {
	a, _, _, _ := routedPair(t, MustIP("255.255.255.0"), IP4{})
	err := a.udp.SendTo(1234, MustIP("8.8.8.8"), 53, []byte("q"))
	if err != ErrNoRoute {
		t.Fatalf("got %v, want ErrNoRoute: no gateway means no route, not a hang", err)
	}
}

func TestNoMaskKeepsEverythingOnLink(t *testing.T) {
	// The back-compat default: with no mask, an arbitrary destination counts
	// as on-link and is resolved DIRECTLY. Note what that means -- the send
	// below can only fail with an ARP timeout, because nothing answers an
	// ARP for 9.9.9.9. That failure is the whole reason routing was added;
	// it worked before only because SLIRP answers ARP for everything. The
	// assertion is that the stack TRIES to resolve the destination itself,
	// which is the legacy behaviour a host build depends on.
	a, _, fa, _ := routedPair(t, IP4{}, IP4{})
	if !a.onLink(MustIP("9.9.9.9")) {
		t.Fatal("with no mask every destination must count as on-link")
	}
	go func() { _ = a.udp.SendTo(1234, MustIP("9.9.9.9"), 53, []byte("q")) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, frame := range fa.sentAll() {
			ef, err := ParseEth(frame)
			if err != nil {
				continue
			}
			if ef.Type == EthTypeARP && bytes.Contains(ef.Payload, []byte{9, 9, 9, 9}) {
				return // asked the wire for 9.9.9.9 directly: on-link
			}
		}
		runtime.Gosched()
	}
	t.Fatal("no ARP request for the destination itself: routing leaked into the no-mask path")
}

func waitFrames(t *testing.T, f *spyFeed, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.sentAll()) >= n {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("only %d frames, want %d", len(f.sentAll()), n)
}
