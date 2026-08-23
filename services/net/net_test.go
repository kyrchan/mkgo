package main

import (
	"bytes"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// ---- helpers ----

func mustMAC(t *testing.T, s string) MAC {
	t.Helper()
	m, err := ParseMAC(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// pair builds two stacks on a shared segment and returns them plus the
// segment ports for direct wire-level assertions.
func pair(t *testing.T) (a, b *Stack) {
	t.Helper()
	seg := NewSegment()
	pa, pb := seg.Attach(), seg.Attach()
	a = NewStack(mustMAC(t, "02:00:00:00:00:01"), MustIP("10.0.0.1"), pa)
	b = NewStack(mustMAC(t, "02:00:00:00:00:02"), MustIP("10.0.0.2"), pb)
	return a, b
}

// pumpUntil drives both stacks until cond() or deadline.
func pumpUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // generous: F43 parallel -race load
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// ---- Ethernet layer ----

// TestWindowRingCorruptLenClamp pins the wasm32-safe clamp: a slot with
// a hostile len (≥2^31, negative after int() on 32-bit) must yield a
// clamped frame, not panic.
func TestWindowRingCorruptLenClamp(t *testing.T) {
	mem := make([]byte, RingSize)
	ring, err := NewWindowRing(mem)
	if err != nil {
		t.Fatal(err)
	}
	lib.Put32(mem[4:], 1) // tail=1, head=0: one pending slot
	lib.Put32(mem[RingHeaderLen:], 0xFFFFFFFF)
	for i := RingHeaderLen + 4; i < RingHeaderLen+4+16; i++ {
		mem[i] = byte(i)
	}
	f, ok := ring.Recv()
	if !ok {
		t.Fatal("slot lost")
	}
	if len(f) != SlotDataLen {
		t.Fatalf("len=%d want %d", len(f), SlotDataLen)
	}
	if got := lib.Get32(mem[0:]); got != 1 {
		t.Fatalf("head=%d want 1", got)
	}
}

// TestICMPInBounded pins flood hardening: unmatched echo replies must
// never grow icmpIn beyond its cap.
func TestICMPInBounded(t *testing.T) {
	a, b := pair(t)
	for i := 0; i < icmpInCap+25; i++ {
		reply := (&ICMPPacket{Type: ICMPEchoReply, ID: 9, Seq: uint16(i),
			Data: []byte("flood")}).Build()
		a.handleIPv4(&EthFrame{Dst: a.MAC, Src: b.MAC, Type: EthTypeIPv4,
			Payload: mustIPv4(t, b.IP, a.IP, IP4ProtoICMP, reply)})
	}
	a.mu.Lock()
	n := len(a.icmpIn)
	a.mu.Unlock()
	if n > icmpInCap {
		t.Fatalf("icmpIn grew to %d (cap %d)", n, icmpInCap)
	}
}

// mustIPv4 builds an IPv4 datagram with checksum for raw injection.
func mustIPv4(t *testing.T, src, dst IP4, proto uint8, payload []byte) []byte {
	t.Helper()
	dg, err := (&IP4Packet{Src: src, Dst: dst, Proto: proto, Payload: payload}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return dg
}

func TestEthRoundTripAndPadding(t *testing.T) {
	src := mustMAC(t, "02:00:00:00:00:01")
	dst := mustMAC(t, "ff:ff:ff:ff:ff:ff")
	payload := []byte("tiny")
	frame := BuildEth(dst, src, EthTypeARP, payload)

	f, err := ParseEth(frame)
	if err != nil {
		t.Fatal(err)
	}
	if f.Dst != dst || f.Src != src || f.Type != EthTypeARP ||
		!bytes.HasPrefix(f.Payload, payload) {
		t.Fatalf("roundtrip mismatch %+v", f)
	}
	if len(frame) < EthMinLen {
		t.Fatalf("no padding to 60B minimum: %d", len(frame))
	}

	if _, err := ParseEth(frame[:10]); err != ErrShortFrame {
		t.Fatalf("short frame err=%v", err)
	}
	if _, err := ParseMAC("nope"); err == nil {
		t.Fatal("bad mac accepted")
	}
	ip := MustIP("192.168.7.7")
	if ip.String() != "192.168.7.7" {
		t.Fatalf("ip roundtrip %s", ip.String())
	}
	if _, err := ParseIP("999.1.1.1"); err == nil {
		t.Fatal("bad ip accepted")
	}
}

// ---- §6 window ring semantics ----

func TestWindowRingSemantics(t *testing.T) {
	mem := make([]byte, RingSize)
	ring, err := NewWindowRing(mem)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ring.Recv(); ok {
		t.Fatal("empty ring yielded frame")
	}
	msg := bytes.Repeat([]byte{0xAB}, 1000)
	if !ring.Send(msg) {
		t.Fatal("send failed on empty ring")
	}
	got, ok := ring.Recv()
	if !ok || !bytes.Equal(got, msg) {
		t.Fatal("ring roundtrip mismatch")
	}
	if !ring.Send(make([]byte, SlotDataLen)) {
		t.Fatal("full-slot send rejected")
	}
	if _, ok := ring.Recv(); !ok {
		t.Fatal("probe frame lost")
	}
	if ring.Send(make([]byte, SlotDataLen+1)) {
		t.Fatal("oversize slot accepted")
	}
	if _, err := NewWindowRing(make([]byte, 64)); err == nil {
		t.Fatal("small window accepted")
	}
	// fill to capacity then observe drop policy
	for i := 0; i < WinSlots; i++ {
		if !ring.Send([]byte{byte(i)}) {
			t.Fatalf("rejected at %d before capacity", i)
		}
	}
	if ring.Send([]byte{0}) {
		t.Fatal("overflow accepted")
	}
	drained := 0
	for {
		_, ok := ring.Recv()
		if !ok {
			break
		}
		drained++
	}
	if drained != WinSlots {
		t.Fatalf("drained %d of %d", drained, WinSlots)
	}
	var _ = lib.StatusOK
}

// ---- ARP + ICMP over two live stacks ----

func TestARPResolveThenPing(t *testing.T) {
	a, b := pair(t)

	stopB := make(chan struct{})
	defer close(stopB)
	go func() {
		for {
			select {
			case <-stopB:
				return
			default:
			}
			b.pump()
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// first ping exercises ARP request → reply → cached MAC → echo
	rep, err := a.Ping(b.IP, 0x1234, 1, []byte("payload-1"), 5000)
	if err != nil {
		t.Fatalf("ping #1: %v", err)
	}
	if rep.Type != ICMPEchoReply || !bytes.Equal(rep.Data, []byte("payload-1")) {
		t.Fatalf("bad reply %+v", rep)
	}

	// second ping hits the ARP cache (no new ARP traffic)
	before := b.RxARP
	rep2, err := a.Ping(b.IP, 0x1234, 2, []byte("p2"), 5000)
	if err != nil || !bytes.Equal(rep2.Data, []byte("p2")) {
		t.Fatalf("ping #2: %v", err)
	}
	if b.RxARP != before {
		t.Fatalf("cache miss caused extra ARP (%d)", b.RxARP-before)
	}

	if _, _, v4, icmp := a.Stats(); v4 == 0 || icmp == 0 {
		t.Fatalf("a counters v4=%d icmp=%d", v4, icmp)
	}
	if _, _, _, icmp := b.Stats(); icmp == 0 {
		t.Fatal("b saw no icmp")
	}
}

func TestARPTimeoutWhenPeerAbsent(t *testing.T) {
	seg := NewSegment()
	lonely := NewStack(mustMAC(t, "02:00:00:aa:00:01"), MustIP("10.9.9.1"), seg.Attach())
	if _, err := lonely.Ping(MustIP("10.9.9.99"), 1, 1, nil, 300); err == nil {
		t.Fatal("ping to nobody succeeded")
	}
}

func TestChecksumAndIPv4Rejects(t *testing.T) {
	// known vector: all-zero header checksums to 0xffff
	var hdr [20]byte
	if Checksum(hdr[:]) != 0xffff {
		t.Fatalf("zero checksum=%x", Checksum(hdr[:]))
	}
	hdr[0] = 0x45
	c := Checksum(hdr[:])
	if Checksum(hdr[:]) != c {
		t.Fatal("checksum not deterministic")
	}
	// inserting valid checksum yields zero sum
	BePut16(hdr[10:12], c)
	if Checksum(hdr[:]) != 0 {
		t.Fatal("checksum verification failed")
	}

	pkt := &IP4Packet{Src: MustIP("1.2.3.4"), Dst: MustIP("5.6.7.8"),
		Proto: IP4ProtoUDP, Payload: []byte("hi")}
	raw, err := pkt.Build()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseIP4(raw)
	if err != nil || back.Src != pkt.Src || back.Dst != pkt.Dst ||
		back.Proto != IP4ProtoUDP || !bytes.Equal(back.Payload, pkt.Payload) {
		t.Fatalf("ipv4 roundtrip %+v err=%v", back, err)
	}

	// corrupt checksum → reject
	raw[10] ^= 0xff
	if _, err := ParseIP4(raw); err == nil {
		t.Fatal("corrupt ipv4 accepted")
	}

	// oversize without fragmentation → ErrTooBig
	big := &IP4Packet{Payload: make([]byte, EthPayloadM)}
	if _, err := big.Build(); err != ErrTooBig {
		t.Fatalf("oversize err=%v", err)
	}
}

// ---- UDP ----

func TestUDPDemuxAndSend(t *testing.T) {
	a, b := pair(t)
	stopAB := make(chan struct{})
	defer close(stopAB)
	go func() {
		for {
			select {
			case <-stopAB:
				return
			default:
			}
			a.pump()
			b.pump()
			time.Sleep(100 * time.Microsecond)
		}
	}()

	qb := b.udp.Bind(8080)
	qa := a.udp.Bind(9090)

	if err := a.udp.SendTo(9090, b.IP, 8080, []byte("hello udp")); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, func() bool { _, ok := qb.Recv(); return ok }, "udp never delivered")

	// demux isolation: wrong port gets nothing
	qOther := b.udp.Bind(9999)
	if err := a.udp.SendTo(9090, b.IP, 8080, []byte("second")); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, func() bool { _, ok := qb.Recv(); return ok }, "second dgram lost")
	if _, ok := qOther.Recv(); ok {
		t.Fatal("demux leaked across ports")
	}

	// reverse direction
	if err := b.udp.SendTo(8080, a.IP, 9090, []byte("pong")); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, func() bool { d, _ := qa.Recv(); return string(d) == "pong" },
		"reverse udp lost")

	_ = UDPHdrLen // silence if constants unused in future refactors
}

func waitForCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second) // generous: F43 parallel -race load
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}
