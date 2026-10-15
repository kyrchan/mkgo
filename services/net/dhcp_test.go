package main

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"time"
)

func testMAC() [6]byte { return [6]byte{0x02, 0, 0, 0, 0, 0x09} }

func mustParse(t *testing.T, b []byte) *DHCPMessage {
	t.Helper()
	m, err := ParseDHCP(b)
	if err != nil {
		t.Fatalf("ParseDHCP: %v", err)
	}
	return m
}

func TestDHCPRoundTrip(t *testing.T) {
	mac, xid := testMAC(), uint32(0xdeadbeef)
	params := []uint8{dhcpOptSubnetMask, dhcpOptRouter, dhcpOptDNS}
	d := BuildDiscover(mac, xid, "p9", params)

	// 240 fixed + msgtype(2+1) + hostname(2+2) + paramreq(2+3) + end(1)
	if want := dhcpMinLen + 3 + 4 + 2 + len(params) + 1; len(d) != want {
		t.Fatalf("discover length %d, want %d", len(d), want)
	}
	m := mustParse(t, d)
	if m.Op != dhcpOpBootRequest || m.Xid != xid {
		t.Fatalf("op=%d xid=%#x, want op=1 xid=%#x", m.Op, m.Xid, xid)
	}
	if m.MsgType != DHCPDiscover {
		t.Fatalf("msgtype %d, want DISCOVER", m.MsgType)
	}
	if m.CHAddr != mac {
		t.Fatalf("chaddr %v, want %v", m.CHAddr, mac)
	}
	if m.Flags&dhcpFlagBroadcast == 0 {
		t.Fatal("discover must set the broadcast flag: the client has no address")
	}
	if !m.CIAddr.IsZero() || !m.YIAddr.IsZero() {
		t.Fatal("ciaddr/yiaddr must be zero in a discover")
	}
}

// buildReply assembles a server message the way a real server would, so
// the client is only ever tested against framing it did not construct.
func buildReply(t testing.TB, msgType uint8, yi IP4, mask, router, server, dns IP4) []byte {
	t.Helper()
	b := dhcpHeader(dhcpOpBootReply, 0xdeadbeef, testMAC(), 0)
	b[16], b[17], b[18], b[19] = yi[0], yi[1], yi[2], yi[3]
	b = dhcpOpt(b, dhcpOptMessageType, []byte{msgType})
	if !mask.IsZero() {
		b = dhcpOptIP(b, dhcpOptSubnetMask, mask)
	}
	if !router.IsZero() {
		b = dhcpOptIP(b, dhcpOptRouter, router)
	}
	if !server.IsZero() {
		b = dhcpOptIP(b, dhcpOptServerID, server)
	}
	if !dns.IsZero() {
		// Twice on purpose: a server may hand back several resolvers, and
		// the client must keep every entry rather than the last one.
		b = dhcpOptIP(b, dhcpOptDNS, dns)
		b = dhcpOptIP(b, dhcpOptDNS, dns)
	}
	b = dhcpOptU32(b, dhcpOptLeaseTime, 3600)
	return append(b, 255)
}

func TestDHCPOfferAndACK(t *testing.T) {
	want := Offer{
		YIAddr:    MustIP("10.0.2.15"),
		Netmask:   MustIP("255.255.255.0"),
		Router:    MustIP("10.0.2.2"),
		DNS:       []IP4{MustIP("10.0.2.3"), MustIP("10.0.2.3")},
		LeaseTime: 3600,
		ServerID:  MustIP("10.0.2.2"),
	}
	for _, mt := range []uint8{DHCPOffer, DHCPACK} {
		msg := mustParse(t, buildReply(t, mt, want.YIAddr, want.Netmask,
			want.Router, want.ServerID, want.DNS[0]))
		got, ok := msg.OfferFrom()
		if !ok {
			t.Fatalf("msgtype %d: OfferFrom refused a valid message", mt)
		}
		if got.YIAddr != want.YIAddr || got.Netmask != want.Netmask ||
			got.Router != want.Router || got.ServerID != want.ServerID {
			t.Fatalf("msgtype %d: got %+v, want %+v", mt, got, want)
		}
		if got.LeaseTime != want.LeaseTime {
			t.Fatalf("lease %d, want %d", got.LeaseTime, want.LeaseTime)
		}
		if len(got.DNS) != 2 || got.DNS[0] != want.DNS[0] {
			t.Fatalf("dns %v, want two %v entries", got.DNS, want.DNS[0])
		}
	}
}

func TestDHCPNAKIsNotALease(t *testing.T) {
	// A NAK carries no yiaddr: the client must fall back, not configure.
	msg := mustParse(t, buildReply(t, DHCPNAK, IP4{}, IP4{}, IP4{}, IP4{}, IP4{}))
	if _, ok := msg.OfferFrom(); ok {
		t.Fatal("NAK must not be treated as a lease")
	}
}

func TestDHCPRequestCarriesOfferedAddress(t *testing.T) {
	offered, server := MustIP("10.0.2.15"), MustIP("10.0.2.2")
	r := mustParse(t, BuildRequest(testMAC(), 0xdeadbeef, offered, server, "p9", nil))
	if r.MsgType != DHCPRequest {
		t.Fatalf("msgtype %d, want REQUEST", r.MsgType)
	}
	if !r.HasServer || r.ServerID != server {
		t.Fatalf("server-id %v, want %v", r.ServerID, server)
	}
	if !r.CIAddr.IsZero() {
		t.Fatal("ciaddr must be zero in SELECTING: the client is not using the address yet")
	}
	// The requested address rides in option 50, not yiaddr, on a request.
	raw := BuildRequest(testMAC(), 0xdeadbeef, offered, server, "", nil)
	if !bytes.Contains(raw, []byte{dhcpOptRequestedIP, 4, offered[0], offered[1], offered[2], offered[3]}) {
		t.Fatalf("request is missing option 50 with %v", offered)
	}
}

func TestDHCPRejectsMalformed(t *testing.T) {
	good := BuildDiscover(testMAC(), 1, "p9", []uint8{dhcpOptDNS})
	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"shorter than the fixed header", good[:dhcpMinLen-1]},
		{"options never terminated", good[:dhcpMinLen]},
		{"wrong magic cookie", func() []byte {
			b := bytes.Clone(good)
			dhcpPut32(b[236:240], 0x12345678)
			return b
		}()},
		{"option length runs past the buffer", func() []byte {
			b := bytes.Clone(good)
			return append(b[:dhcpMinLen], 61, 200, 1, 2)
		}()},
		{"option code with no length byte", append(bytes.Clone(good[:dhcpMinLen]), 53)},
		{"wrong hardware type", func() []byte {
			b := bytes.Clone(good)
			b[1] = 6
			return b
		}()},
		{"wrong hardware length", func() []byte {
			b := bytes.Clone(good)
			b[2] = 8
			return b
		}()},
	}
	for _, c := range cases {
		if m, err := ParseDHCP(c.in); err == nil {
			t.Fatalf("%s: ParseDHCP accepted %+v, want an error", c.name, m)
		}
	}
}

func TestDHCPIgnoresUnknownOptions(t *testing.T) {
	// RFC 2131: a client must skip options it does not recognise, and
	// pad bytes may appear anywhere between them.
	b := dhcpHeader(dhcpOpBootReply, 7, testMAC(), 0)
	b[16], b[17], b[18], b[19] = 10, 0, 2, 15
	b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPOffer})
	b = append(b, 224, 4, 0xde, 0xad, 0xbe, 0xef) // unknown, must be skipped
	b = append(b, dhcpOptPad, dhcpOptPad, dhcpOptPad)
	b = dhcpOptIP(b, dhcpOptRouter, MustIP("10.0.2.2"))
	b = append(b, 255)
	m := mustParse(t, b)
	o, ok := m.OfferFrom()
	if !ok || o.Router != MustIP("10.0.2.2") {
		t.Fatalf("unknown options broke parsing: %+v ok=%v", o, ok)
	}
}

func TestDHCPHostnameIsClamped(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	m := mustParse(t, BuildDiscover(testMAC(), 1, long, nil))
	if m.MsgType != DHCPDiscover {
		t.Fatal("clamping the hostname corrupted the message")
	}
	// 63 is the DHCP limit; anything longer must not emit an over-long
	// option, which would desynchronise the server's option walk.
	if bytes.Contains(BuildDiscover(testMAC(), 1, long, nil), []byte{byte(len(long))}) {
		t.Fatal("hostname was not clamped to 63")
	}
}

func FuzzParseDHCP(f *testing.F) {
	f.Add(BuildDiscover(testMAC(), 1, "p9", []uint8{dhcpOptDNS}))
	f.Add(buildReply(f, DHCPOffer, MustIP("10.0.2.15"), MustIP("255.255.255.0"),
		MustIP("10.0.2.2"), MustIP("10.0.2.2"), MustIP("10.0.2.3")))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{255}, 300))
	f.Fuzz(func(t *testing.T, in []byte) {
		m, err := ParseDHCP(in)
		if err != nil {
			return
		}
		// Anything that parses must be self-consistent enough to act on.
		if o, ok := m.OfferFrom(); ok {
			if o.YIAddr.IsZero() {
				t.Fatal("OfferFrom returned a lease with a zero address")
			}
		}
	})
}

func TestNewXIDDrawsFromEntropy(t *testing.T) {
	// A deterministic reader proves the id comes from the source rather
	// than from a constant hidden in the code.
	got, err := NewXID(func(b []byte) error {
		copy(b, []byte{0xde, 0xad, 0xbe, 0xef})
		return nil
	})
	if err != nil {
		t.Fatalf("NewXID: %v", err)
	}
	if got != 0xdeadbeef {
		t.Fatalf("xid %#x, want 0xdeadbeef: it must come from the entropy source", got)
	}
}

func TestNewXIDIsNotConstant(t *testing.T) {
	seen := map[uint32]bool{}
	src := byte(0)
	for i := 0; i < 64; i++ {
		src++
		id, err := NewXID(func(b []byte) error {
			copy(b, []byte{src, src, src, src})
			return nil
		})
		if err != nil {
			t.Fatalf("NewXID: %v", err)
		}
		seen[id] = true
	}
	if len(seen) < 8 {
		t.Fatalf("only %d distinct ids from 64 draws: it looks constant", len(seen))
	}
}

func TestNewXIDPropagatesEntropyFailure(t *testing.T) {
	// A failed draw must not become a silent fixed id, which is the exact
	// bug this function exists to remove.
	if id, err := NewXID(func([]byte) error { return errShort }); err == nil {
		t.Fatalf("got xid %#x, want an error: a failed draw must not fake a lease", id)
	}
}

// dhcpServer scripts the server side of a DORA exchange. The client stack
// is built on a spy feed rather than a segment pair, so the exchange is
// driven step by step and deterministically -- no timing, and the test can
// inspect exactly what went on the wire.
type dhcpServer struct {
	t        *testing.T
	mac      MAC
	feed     *spyFeed
	offered  IP4
	netmask  IP4
	router   IP4
	dns      IP4
	consumed int
	nOffer   int
	nRequest int
	// leaseSeconds is what the scripted server grants; 0 means the
	// default 3600.
	leaseSeconds uint32
	// reply controls the server's answer; nil means the normal path.
	reply func(req *DHCPMessage, s *dhcpServer) []byte
}

// spyFeed is shared between the client stack (driven on the test's own
// goroutine by DHCPAcquire) and the scripted server goroutine, so every
// access is locked. The race detector is right: without the mutex the
// server appends to `in` while the client drains it.
type spyFeed struct {
	mu  sync.Mutex
	out [][]byte
	in  [][]byte
	// popped records every frame that left this feed, in order. Tests must
	// read it rather than `out`: a pump goroutine drains `out` as fast as it
	// can, so scanning `out` races the pump and the assertion becomes
	// timing-dependent.
	popped [][]byte
}

func (f *spyFeed) Send(frame []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out = append(f.out, bytes.Clone(frame))
	return true
}
func (f *spyFeed) Recv() ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.in) == 0 {
		return nil, false
	}
	fr := f.in[0]
	f.in = f.in[1:]
	return fr, true
}

func (f *spyFeed) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.out)
}

func (f *spyFeed) at(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out[i]
}

// pop atomically removes and returns the oldest queued frame.
func (f *spyFeed) pop() ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.out) == 0 {
		return nil, false
	}
	fr := f.out[0]
	f.out = f.out[1:]
	f.popped = append(f.popped, bytes.Clone(fr))
	return fr, true
}

func (f *spyFeed) sentAll() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.popped
}

func (f *spyFeed) drop(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out = append(f.out[:i], f.out[i+1:]...)
}

func (f *spyFeed) push(frame []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.in = append(f.in, frame)
}

func newDHCPServer(t *testing.T, clientIP IP4) (*Stack, *dhcpServer) {
	t.Helper()
	f := &spyFeed{}
	client := NewStack(MAC{0x02, 0, 0, 0, 0, 0x09}, clientIP, f)
	srv := &dhcpServer{
		t: t, mac: MAC{0x02, 0, 0, 0, 0, 0x02}, feed: f,
		offered: MustIP("10.0.2.15"), netmask: MustIP("255.255.255.0"),
		router: MustIP("10.0.2.2"), dns: MustIP("10.0.2.3"),
	}
	return client, srv
}

// deliver parses the client's last datagram and returns its DHCP message.
func (s *dhcpServer) deliver() *DHCPMessage {
	s.t.Helper()
	if s.feed.sent() == 0 {
		s.t.Fatal("client sent nothing")
	}
	// Consume frames in order. Re-reading the last one would re-answer the
	// same DISCOVER forever, and the REQUEST would never be acked.
	if s.consumed >= s.feed.sent() {
		s.t.Fatal("client sent nothing new")
	}
	frame := s.feed.at(s.consumed)
	s.consumed++
	ef, err := ParseEth(frame)
	if err != nil {
		s.t.Fatalf("client frame: %v", err)
	}
	pkt, err := ParseIP4(ef.Payload)
	if err != nil {
		s.t.Fatalf("client ip: %v", err)
	}
	dg, err := ParseUDP(pkt.Payload)
	if err != nil {
		s.t.Fatalf("client udp: %v", err)
	}
	if dg.SrcPort != dhcpClientPort || dg.DstPort != dhcpServerPort {
		s.t.Fatalf("client ports %d->%d, want %d->%d", dg.SrcPort, dg.DstPort,
			dhcpClientPort, dhcpServerPort)
	}
	if pkt.Src != (IP4{}) {
		s.t.Fatalf("client source %v: a client with no lease must send from 0.0.0.0", pkt.Src)
	}
	if pkt.Dst != broadcastIP {
		s.t.Fatalf("client dest %v, want the broadcast address", pkt.Dst)
	}
	m, err := ParseDHCP(dg.Data)
	if err != nil {
		s.t.Fatalf("client dhcp: %v", err)
	}
	return m
}

// replyTo builds a server frame and queues it for the client.
func (s *dhcpServer) replyTo(m *DHCPMessage, msgType uint8) {
	s.t.Helper()
	var b []byte
	if s.reply != nil {
		b = s.reply(m, s)
	} else {
		yi := s.offered
		if msgType == DHCPNAK || msgType == DHCPACK {
			yi = s.offered
		}
		b = dhcpHeader(dhcpOpBootReply, m.Xid, MAC{0x02, 0, 0, 0, 0, 0x09}, 0)
		b[16], b[17], b[18], b[19] = yi[0], yi[1], yi[2], yi[3]
		b = dhcpOpt(b, dhcpOptMessageType, []byte{msgType})
		// RFC 2131: the ACK carries the network configuration, not just a
		// yes. A client that only saw the config in the OFFER would be
		// relying on state the protocol never promised it.
		if msgType == DHCPOffer || msgType == DHCPACK {
			b = dhcpOptIP(b, dhcpOptSubnetMask, s.netmask)
			b = dhcpOptIP(b, dhcpOptRouter, s.router)
			b = dhcpOptIP(b, dhcpOptServerID, s.router)
			b = dhcpOptIP(b, dhcpOptDNS, s.dns)
			b = dhcpOptU32(b, dhcpOptLeaseTime, s.grantedSeconds())
		}
		b = append(b, 255)
	}
	dg := &UDPDatagram{SrcPort: dhcpServerPort, DstPort: dhcpClientPort, Data: b}
	dg.checksumSrc = s.router
	dg.checksumDst = broadcastIP
	dgp, err := (&IP4Packet{Src: s.router, Dst: broadcastIP, Proto: IP4ProtoUDP, Payload: dg.Build()}).Build()
	if err != nil {
		s.t.Fatalf("server ip build: %v", err)
	}
	s.feed.push(BuildEth(BroadcastMAC, MAC{0x02, 0, 0, 0, 0, 0x09}, EthTypeIPv4, dgp))
}

func TestDHCPDORA(t *testing.T) {
	client, srv := newDHCPServer(t, IP4{}) // no address yet: that is the point
	go func() {
		for {
			// Wait for the DISCOVER, answer it, wait for the REQUEST, ack.
			for srv.consumed >= srv.feed.sent() {
				runtime.Gosched()
			}
			disc := srv.deliver()
			if disc.MsgType == DHCPDiscover {
				srv.nOffer++
				srv.replyTo(disc, DHCPOffer)
			} else if disc.MsgType == DHCPRequest {
				srv.nRequest++
				srv.replyTo(disc, DHCPACK)
				return
			}
		}
	}()
	got, err := client.DHCPAcquire(0x1234, "p9", DefaultDHCPOptions(), 3, 5000)
	if err != nil {
		t.Fatalf("DHCPAcquire: %v", err)
	}
	if got.YIAddr != srv.offered {
		t.Fatalf("lease %v, want %v", got.YIAddr, srv.offered)
	}
	if got.Netmask != srv.netmask || got.Router != srv.router {
		t.Fatalf("config %+v, want mask %v router %v", got, srv.netmask, srv.router)
	}
	if len(got.DNS) != 1 || got.DNS[0] != srv.dns {
		t.Fatalf("dns %v, want [%v]", got.DNS, srv.dns)
	}
	if srv.nOffer != 1 || srv.nRequest != 1 {
		t.Fatalf("exchange was offer=%d request=%d, want one of each", srv.nOffer, srv.nRequest)
	}
}

func TestDHCPRetransmitsDiscover(t *testing.T) {
	client, srv := newDHCPServer(t, IP4{})
	// Stay silent: the client must give up on its own rather than block.
	start := time.Now()
	if _, err := client.DHCPAcquire(0x4321, "p9", nil, 3, 200); err != errNoOffer {
		t.Fatalf("got %v, want errNoOffer (bounded failure, not a hang)", err)
	}
	if el := time.Since(start); el > 20*time.Second {
		t.Fatalf("DHCPAcquire took %v, want a bounded failure", el)
	}
	if srv.feed.sent() < 3 {
		t.Fatalf("client sent %d datagrams, want at least one per attempt", srv.feed.sent())
	}
}

func TestDHCPNAKDuringAcquireFails(t *testing.T) {
	client, srv := newDHCPServer(t, IP4{})
	srv.reply = func(req *DHCPMessage, s *dhcpServer) []byte {
		if req.MsgType == DHCPDiscover {
			return s.builtNAK(req)
		}
		return nil
	}
	if _, err := client.DHCPAcquire(0x9999, "p9", nil, 2, 200); err != errNoOffer {
		t.Fatalf("got %v, want errNoOffer: a NAK is not an offer", err)
	}
}

// grantedSeconds is the lease this scripted server hands out.
func (s *dhcpServer) grantedSeconds() uint32 {
	if s.leaseSeconds == 0 {
		return 3600
	}
	return s.leaseSeconds
}

func (s *dhcpServer) builtNAK(req *DHCPMessage) []byte {
	b := dhcpHeader(dhcpOpBootReply, req.Xid, s.mac, 0)
	b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPNAK})
	return append(b, 255)
}

func TestDHCPIgnoresForeignXid(t *testing.T) {
	client, srv := newDHCPServer(t, IP4{})
	srv.reply = func(req *DHCPMessage, s *dhcpServer) []byte {
		if req.MsgType != DHCPDiscover {
			return nil
		}
		// Someone else's transaction: same server, wrong xid.
		b := dhcpHeader(dhcpOpBootReply, req.Xid+1, s.mac, 0)
		b[16], b[17], b[18], b[19] = 192, 168, 1, 50
		b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPOffer})
		b = dhcpOptIP(b, dhcpOptSubnetMask, MustIP("255.255.255.0"))
		return append(b, 255)
	}
	if _, err := client.DHCPAcquire(0xaaaa, "p9", nil, 2, 200); err != errNoOffer {
		t.Fatalf("client accepted another transaction's offer: %v", err)
	}
}

func TestLeaseRenewalSchedule(t *testing.T) {
	// 3600s lease: T1 at 1800s, T2 at 3150s.
	o := Offer{YIAddr: MustIP("10.0.2.15"), LeaseTime: 3600}
	now := uint64(0)
	l := NewLease(o, func() uint64 { return now })
	if l.T1Ms() != 1800_000 || l.T2Ms() != 3150_000 {
		t.Fatalf("T1=%d T2=%d, want 1800000/3150000", l.T1Ms(), l.T2Ms())
	}
	// Before T1: nothing to do.
	if renew, rebind := l.Due(0); renew || rebind {
		t.Fatalf("due at t=0: renew=%v rebind=%v", renew, rebind)
	}
	// Just before T1: still nothing.
	if renew, _ := l.Due(1799_999); renew {
		t.Fatal("due one ms before T1")
	}
	// At T1: renew, not rebind.
	renew, rebind := l.Due(1800_000)
	if !renew || rebind {
		t.Fatalf("at T1: renew=%v rebind=%v, want renew only", renew, rebind)
	}
	// Between T1 and T2: still renew, not rebind.
	if renew, rebind := l.Due(2000_000); !renew || rebind {
		t.Fatalf("mid-lease: renew=%v rebind=%v", renew, rebind)
	}
	// At T2: must re-DISCOVER, not re-REQUEST.
	renew, rebind = l.Due(3150_000)
	if renew || !rebind {
		t.Fatalf("at T2: renew=%v rebind=%v, want rebind", renew, rebind)
	}
	if !l.Expired(3150_000) {
		t.Fatal("Expired must be true at T2")
	}
	if l.Expired(3149_999) {
		t.Fatal("Expired must be false just before T2")
	}
}

func TestLeaseWithoutClockNeverRenews(t *testing.T) {
	// A host with no clock cannot renew. Inventing a deadline from a nil
	// source would be worse than admitting it: the lease would appear to
	// expire with no way to observe it.
	l := NewLease(Offer{LeaseTime: 3600}, nil)
	if renew, rebind := l.Due(999_999_999); renew || rebind {
		t.Fatal("a nil clock must never report the lease due")
	}
	if l.Expired(999_999_999) {
		t.Fatal("a nil clock must never report expiry")
	}
}

func TestLeaseWithNoGrantedTimeNeverRenews(t *testing.T) {
	// A server that granted no lease time leaves nothing to schedule
	// against; the address is still usable, just not renewable.
	l := NewLease(Offer{YIAddr: MustIP("10.0.2.15")}, func() uint64 { return 99999 })
	if l.GrantedMs != 0 {
		t.Fatalf("GrantedMs=%d, want 0", l.GrantedMs)
	}
	if renew, _ := l.Due(99999); renew {
		t.Fatal("a lease with no granted time must not schedule renewal")
	}
}

// TestLeaseRenewalEndToEnd exercises the real renewal path: a full DORA
// exchange, an injected clock advancing past T1, and a SECOND exchange
// driven by NewRenewer -- the same function net.wasm hands to
// ServeNetRenewing -- so the guest's renewal glue is executed here rather
// than imitated.
func TestLeaseRenewalEndToEnd(t *testing.T) {
	client, srv := newDHCPServer(t, IP4{})
	srv.leaseSeconds = 8 // T1 at 4s, T2 at 7s

	// The scripted server: answer every DISCOVER with an OFFER and every
	// REQUEST with an ACK, for as many exchanges as the client runs.
	go func() {
		for {
			for srv.consumed >= srv.feed.sent() {
				runtime.Gosched()
			}
			req := srv.deliver()
			switch req.MsgType {
			case DHCPDiscover:
				srv.nOffer++
				srv.replyTo(req, DHCPOffer)
			case DHCPRequest:
				srv.nRequest++
				srv.replyTo(req, DHCPACK)
			}
		}
	}()

	now := uint64(0)
	clock := func() uint64 { return now }
	// The PRODUCTION renewer, not a re-implementation: main.go calls this
	// exact function, so the guest's glue is what runs here.
	renewer := NewRenewer(client, func(b []byte) error {
		copy(b, []byte{byte(now >> 24), byte(now >> 16), byte(now >> 8), byte(now)})
		return nil
	})
	acquire := func() *Lease {
		o, err := renewer()
		if err != nil {
			t.Fatalf("renewer: %v", err)
		}
		return NewLease(o, clock)
	}

	lease := acquire()
	if lease.T1Ms() != 4000 || lease.T2Ms() != 7000 {
		t.Fatalf("T1=%d T2=%d, want 4000/7000 for an 8s lease", lease.T1Ms(), lease.T2Ms())
	}
	offersAfterFirst := srv.nOffer

	// Before T1 nothing happens.
	now = 3999
	if due, _ := lease.Due(clock()); due {
		t.Fatal("due one ms before T1")
	}

	// Past T1: due, and the renewal runs the real exchange.
	now = 4000
	due, rebind := lease.Due(clock())
	if !due || rebind {
		t.Fatalf("at T1: due=%v rebind=%v", due, rebind)
	}
	renewed := acquire()
	if srv.nOffer != offersAfterFirst+1 {
		t.Fatalf("server saw %d offers, want %d: renewal must be a real exchange",
			srv.nOffer, offersAfterFirst+1)
	}
	if renewed.Offer.YIAddr != lease.Offer.YIAddr {
		t.Fatalf("renewed to %v, want the same %v", renewed.Offer.YIAddr, lease.Offer.YIAddr)
	}
	// The renewed lease restarts the clock rather than inheriting the old
	// deadline -- otherwise a long-lived client would re-acquire forever.
	if renewed.GrantedMs != lease.GrantedMs {
		t.Fatalf("renewed GrantedMs=%d, want %d", renewed.GrantedMs, lease.GrantedMs)
	}

	// The client adopted the renewed address and the routing fields.
	if client.IP != srv.offered || client.Netmask != srv.netmask || client.Gateway != srv.router {
		t.Fatalf("client state ip=%v mask=%v gw=%v", client.IP, client.Netmask, client.Gateway)
	}

	// Past T2 on the ORIGINAL lease, the caller must re-DISCOVER.
	now = 7000
	if due, rebind := lease.Due(clock()); due || !rebind {
		t.Fatalf("at T2: due=%v rebind=%v, want rebind", due, rebind)
	}
}
