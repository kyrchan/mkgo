package main

import (
	"errors"
	"runtime"
)

// DHCPv4 message layer (RFC 951 + RFC 2131), the part of a client that is
// pure logic: framing, the option TLVs, and the four messages a client
// exchanges with a server. Deliberately transport-free -- it neither sends
// nor receives, so all of it is host-testable against a byte corpus.
//
// The wire plumbing a client needs (source 0.0.0.0 before it has an
// address, and a broadcast destination that must NOT be ARP-resolved) is
// not something the stack can do yet: Stack.sendIPv4 hardcodes Src: s.IP
// and resolves the destination with ARP. That gap is why this file exists
// on its own -- the message layer can be correct and tested before the
// stack grows the two capabilities the transport needs.

// Parse errors. Distinct values so a caller can tell "these bytes are not
// a DHCP message" (short/bad magic/wrong hardware) from "this message is
// truncated" -- the first means I did not receive a DHCP packet, the
// second means I received half of one.
var (
	errShort     = errors.New("dhcp: shorter than the fixed header")
	errMagic     = errors.New("dhcp: bad magic cookie")
	errHType     = errors.New("dhcp: not an Ethernet client")
	errTruncated = errors.New("dhcp: truncated option")
	errOptLen    = errors.New("dhcp: option has a fixed length")
	errNoEnd     = errors.New("dhcp: options are not terminated")
)

const (
	dhcpFixedLen = 236 // op..file, without the magic cookie
	dhcpMinLen   = dhcpFixedLen + 4
	dhcpMagic    = 0x63825363

	dhcpOpBootRequest = 1
	dhcpOpBootReply   = 2
	dhcpHTypeEthernet = 1

	// Option codes used by a client.
	dhcpOptPad           = 0
	dhcpOptSubnetMask    = 1
	dhcpOptRouter        = 3
	dhcpOptDNS           = 6
	dhcpOptRequestedIP   = 50
	dhcpOptLeaseTime     = 51
	dhcpOptMessageType   = 53
	dhcpOptServerID      = 54
	dhcpOptParamReqList  = 55
	dhcpOptHostname      = 12
	dhcpOptBroadcastFlag = 1 << 15 // in 'flags', not an option

	// DHCP message types (option 53).
	DHCPDiscover = 1
	DHCPOffer    = 2
	DHCPRequest  = 3
	DHCPDecline  = 4
	DHCPACK      = 5
	DHCPNAK      = 6
	DHCPRelease  = 7

	// A BOOTP 'flags' field with the broadcast bit set: the client has no
	// address yet, so replies must come back to 255.255.255.255.
	dhcpFlagBroadcast = 0x8000
)

// DHCPMessage is a decoded DHCPv4 message: the fixed header fields a
// client acts on, plus the options it understands. Unknown options are
// skipped by the parser rather than rejected, because RFC 2131 requires a
// client to ignore what it does not recognise.
type DHCPMessage struct {
	Op      uint8
	Xid     uint32
	Flags   uint16
	CIAddr  IP4
	YIAddr  IP4
	SIAddr  IP4
	GIAddr  IP4
	CHAddr  [6]byte
	MsgType uint8

	SubnetMask IP4
	Router     IP4
	DNS        []IP4
	LeaseTime  uint32
	ServerID   IP4
	HasMask    bool
	HasRouter  bool
	HasLease   bool
	HasServer  bool
}

// IsZero reports the unspecified address. The DHCP exchange is the first
// code here that needs to test an address for "unset"; IP4 is a [4]byte,
// so that is a comparison.
func (ip IP4) IsZero() bool { return ip == IP4{} }

// ip4From reads a 4-byte address off the wire. DHCP addresses are always
// 4 bytes and are never compressed, so no length check belongs here.
func ip4From(b []byte) IP4 { return IP4{b[0], b[1], b[2], b[3]} }

// dhcpPut16/32 write big-endian fields. DHCP predates and ignores this
// system's little-endian port convention, so it is packed by hand.
func dhcpPut16(b []byte, v uint16) { b[0] = byte(v >> 8); b[1] = byte(v) }
func dhcpPut32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func dhcpGet16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func dhcpGet32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// dhcpHeader writes the 240-byte fixed part shared by every message.
func dhcpHeader(op uint8, xid uint32, mac [6]byte, flags uint16) []byte {
	b := make([]byte, dhcpMinLen)
	b[0] = op
	b[1] = dhcpHTypeEthernet
	b[2] = 6 // hlen
	b[3] = 0 // hops
	dhcpPut32(b[4:8], xid)
	dhcpPut16(b[8:10], flags)
	// ciaddr/yiaddr/siaddr/giaddr stay zero: the client has no address.
	copy(b[28:34], mac[:]) // chaddr, 16 bytes, zero-padded
	dhcpPut32(b[236:240], dhcpMagic)
	return b
}

func dhcpOpt(b []byte, code uint8, val []byte) []byte {
	if len(val) == 0 {
		return append(b, code, 0)
	}
	b = append(b, code, byte(len(val)))
	return append(b, val...)
}

func dhcpOptIP(b []byte, code uint8, ip IP4) []byte {
	v := make([]byte, 4)
	copy(v, ip[:])
	return dhcpOpt(b, code, v)
}

func dhcpOptU32(b []byte, code uint8, v uint32) []byte {
	val := make([]byte, 4)
	dhcpPut32(val, v)
	return dhcpOpt(b, code, val)
}

// BuildDiscover builds DHCPDISCOVER. params is the option-55 parameter
// request list, in preference order; the server may answer with any
// subset, and a client that asked for nothing gets nothing.
func BuildDiscover(mac [6]byte, xid uint32, hostname string, params []uint8) []byte {
	b := dhcpHeader(dhcpOpBootRequest, xid, mac, dhcpFlagBroadcast)
	b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPDiscover})
	if hostname != "" {
		if len(hostname) > 63 {
			hostname = hostname[:63]
		}
		b = dhcpOpt(b, dhcpOptHostname, []byte(hostname))
	}
	if len(params) > 0 {
		if len(params) > 255 {
			params = params[:255]
		}
		b = dhcpOpt(b, dhcpOptParamReqList, params)
	}
	return append(b, 255) // end
}

// BuildRequest builds the REQUEST of the DORA exchange. In SELECTING state
// the requested-IP option carries the address just offered and server-id
// names the server that offered it; ciaddr stays zero because the client
// is not yet using the address.
func BuildRequest(mac [6]byte, xid uint32, offered IP4, serverID IP4, hostname string, params []uint8) []byte {
	b := dhcpHeader(dhcpOpBootRequest, xid, mac, dhcpFlagBroadcast)
	b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPRequest})
	b = dhcpOptIP(b, dhcpOptRequestedIP, offered)
	if !serverID.IsZero() {
		b = dhcpOptIP(b, dhcpOptServerID, serverID)
	}
	if hostname != "" {
		if len(hostname) > 63 {
			hostname = hostname[:63]
		}
		b = dhcpOpt(b, dhcpOptHostname, []byte(hostname))
	}
	if len(params) > 0 {
		if len(params) > 255 {
			params = params[:255]
		}
		b = dhcpOpt(b, dhcpOptParamReqList, params)
	}
	return append(b, 255)
}

// BuildRelease builds a DHCPRELEASE message. The client sends this when it
// is abandoning a lease (shut-down, explicit release). ciaddr carries the
// address being released so the server can reclaim it.
func BuildRelease(mac [6]byte, xid uint32, addr IP4, serverID IP4) []byte {
	b := dhcpHeader(dhcpOpBootRequest, xid, mac, 0)
	b = dhcpOpt(b, dhcpOptMessageType, []byte{DHCPRelease})
	b = dhcpOptIP(b, dhcpOptServerID, serverID)
	return append(b, 255)
}

// ParseDHCP decodes a message. It is strict about the envelope (a short
// buffer or a wrong magic cookie is an error, since that means the bytes
// are not a DHCP message at all) and tolerant about options it does not
// use (RFC 2131: ignore what you do not recognise).
func ParseDHCP(b []byte) (*DHCPMessage, error) {
	if len(b) < dhcpMinLen {
		return nil, errShort
	}
	if dhcpGet32(b[236:240]) != dhcpMagic {
		return nil, errMagic
	}
	m := &DHCPMessage{
		Op:     b[0],
		Xid:    dhcpGet32(b[4:8]),
		Flags:  dhcpGet16(b[8:10]),
		CIAddr: ip4From(b[12:16]),
		YIAddr: ip4From(b[16:20]),
		SIAddr: ip4From(b[20:24]),
		GIAddr: ip4From(b[24:28]),
	}
	copy(m.CHAddr[:], b[28:34])
	if b[1] != dhcpHTypeEthernet || b[2] != 6 {
		return nil, errHType
	}

	for off := dhcpMinLen; off < len(b); {
		code := b[off]
		switch code {
		case dhcpOptPad: // zero-fill, no length byte
			off++
			continue
		case 255: // end
			return m, nil
		}
		if off+1 >= len(b) {
			return nil, errTruncated // option code with no length byte
		}
		l := int(b[off+1])
		if off+2+l > len(b) {
			return nil, errTruncated // option claims more than remains
		}
		val := b[off+2 : off+2+l]
		switch code {
		case dhcpOptMessageType:
			if l != 1 {
				return nil, errOptLen
			}
			m.MsgType = val[0]
		case dhcpOptSubnetMask:
			if l != 4 {
				return nil, errOptLen
			}
			m.SubnetMask = ip4From(val)
			m.HasMask = true
		case dhcpOptRouter:
			if l >= 4 {
				m.Router = ip4From(val[:4])
				m.HasRouter = true
			}
		case dhcpOptDNS:
			for i := 0; i+4 <= l; i += 4 {
				m.DNS = append(m.DNS, ip4From(val[i:i+4]))
			}
		case dhcpOptLeaseTime:
			if l == 4 {
				m.LeaseTime = dhcpGet32(val)
				m.HasLease = true
			}
		case dhcpOptServerID:
			if l == 4 {
				m.ServerID = ip4From(val)
				m.HasServer = true
			}
		}
		off += 2 + l
	}
	// Falling out here means the buffer ended on an option boundary with no
	// end option (255). Accepting that would let a message truncated
	// mid-options pass as a complete lease, so a client would configure
	// from a reply it never fully received.
	return nil, errNoEnd
}

// Offer is the subset of a server's reply a client acts on.
type Offer struct {
	YIAddr    IP4
	Netmask   IP4
	Router    IP4
	DNS       []IP4
	LeaseTime uint32
	ServerID  IP4
}

// OfferFrom extracts an OFFER or ACK. A NAK is reported as ok=false: the
// client must fall back, not treat the packet as a lease.
func (m *DHCPMessage) OfferFrom() (Offer, bool) {
	if m.MsgType != DHCPOffer && m.MsgType != DHCPACK {
		return Offer{}, false
	}
	if m.YIAddr.IsZero() {
		return Offer{}, false
	}
	return Offer{
		YIAddr:    m.YIAddr,
		Netmask:   m.SubnetMask,
		Router:    m.Router,
		DNS:       m.DNS,
		LeaseTime: m.LeaseTime,
		ServerID:  m.ServerID,
	}, true
}

// DORA: the client half of a lease acquisition. Placed on Stack because it
// needs the two capabilities the stack previously lacked -- an explicit
// source address and a broadcast send that skips ARP.
const (
	dhcpClientPort = 68
	dhcpServerPort = 67
)

// dhcpZeroSource is the source a client must claim before it has a lease.
// A var, not a const: an array-typed value cannot be a constant.
var dhcpZeroSource = IP4{0, 0, 0, 0}

// DHCPOption is what the caller wants the server to tell it. The default
// set is what a minimal host needs to reach the network: its own mask, the
// router, and resolvers.
func DefaultDHCPOptions() []uint8 {
	return []uint8{dhcpOptSubnetMask, dhcpOptRouter, dhcpOptDNS, dhcpOptLeaseTime}
}

// NewXID draws a DHCP transaction id from the supplied entropy.
//
// Entropy is a parameter rather than a call to a package-level generator so
// that (a) it is host-testable and (b) no caller can quietly fall back to a
// constant. A fixed xid is not a cosmetic weakness: two clients booting on
// the same segment can generate the same one, and a server will then answer
// the wrong transaction -- which is why this exists rather than a literal.
func NewXID(read func([]byte) error) (uint32, error) {
	var b [4]byte
	if err := read(b[:]); err != nil {
		return 0, err
	}
	return dhcpGet32(b[:]), nil
}

// sendDHCP emits a client message from 0.0.0.0:68 to 255.255.255.255:67.
func (s *Stack) sendDHCP(msg []byte) error {
	dg := &UDPDatagram{SrcPort: dhcpClientPort, DstPort: dhcpServerPort, Data: msg}
	dg.checksumSrc = dhcpZeroSource
	dg.checksumDst = broadcastIP
	return s.sendIPv4From(dhcpZeroSource, broadcastIP, IP4ProtoUDP, dg.Build())
}

// sendDHCPRelease emits a DHCPRELEASE from the client's current address to
// the server that granted the lease. ciaddr carries the address being
// released; the server uses it to reclaim the lease immediately.
//
// xid is derived from the source address so the function is testable
// without the wasip1-only entropy source.
func (s *Stack) sendDHCPRelease(serverID IP4) error {
	xid := uint32(s.IP[0])<<24 | uint32(s.IP[1])<<16 | uint32(s.IP[2])<<8 | uint32(s.IP[3])
	msg := BuildRelease(s.MAC, xid, s.IP, serverID)
	dg := &UDPDatagram{SrcPort: dhcpClientPort, DstPort: dhcpServerPort, Data: msg}
	dg.checksumSrc = s.IP
	dg.checksumDst = serverID
	return s.sendIPv4From(s.IP, serverID, IP4ProtoUDP, dg.Build())
}

// awaitDHCP polls the client port for one datagram matching xid, bounded
// by budget iterations. UDPQueue.Recv is non-blocking, so the bound is
// what makes an unanswered server a bounded failure rather than a hung
// session -- the same reason Stack.Ping takes a budget.
func (s *Stack) awaitDHCP(q *UDPQueue, xid uint32, budget int) (*DHCPMessage, error) {
	for i := 0; i < budget; i++ {
		for {
			raw, ok := q.Recv()
			if !ok {
				break
			}
			m, err := ParseDHCP(raw)
			if err != nil {
				continue // not a DHCP message: ignore and keep listening
			}
			// Match on xid: another client's traffic, or a late reply to
			// an earlier attempt, must not be mistaken for ours.
			if m.Xid != xid {
				continue
			}
			return m, nil
		}
		runtime.Gosched()
		// Pull whatever the wire delivered and run it through the inbound
		// path, so the demux can hand port 68 its datagram. Without the
		// pump the queue is never fed and the wait is unbounded in
		// practice while still looking bounded.
		s.pump()
	}
	return nil, errNoReply
}

// DHCPAcquire runs DISCOVER/OFFER/REQUEST/ACK and returns the lease. It
// retransmits each request up to tries times before giving up: a client
// that sends DISCOVER once is not a client, because a single UDP datagram
// is lost often enough to matter.
//
// It does NOT apply the lease. The caller decides, because adopting an
// address is a policy act (and the whole point of this being host-tested
// is that the transport works independently of that decision).
func (s *Stack) DHCPAcquire(xid uint32, hostname string, params []uint8, tries, budget int) (Offer, error) {
	q, _ := s.udp.Bind(dhcpClientPort)
	defer s.udp.Unbind(dhcpClientPort)

	discover := BuildDiscover(s.MAC, xid, hostname, params)
	var offer Offer
	for i := 0; i < tries; i++ {
		if err := s.sendDHCP(discover); err != nil {
			continue
		}
		m, err := s.awaitDHCP(q, xid, budget)
		if err != nil {
			continue // no offer yet: retransmit
		}
		if m.MsgType != DHCPOffer {
			continue
		}
		o, ok := m.OfferFrom()
		if !ok {
			continue
		}
		offer = o
		break
	}
	if offer.YIAddr.IsZero() {
		return Offer{}, errNoOffer
	}

	// A server may answer a retransmitted DISCOVER with a NAK if it has
	// withdrawn the lease; treat that as failure, not as a lease.
	request := BuildRequest(s.MAC, xid, offer.YIAddr, offer.ServerID, hostname, params)
	for i := 0; i < tries; i++ {
		if err := s.sendDHCP(request); err != nil {
			continue
		}
		m, err := s.awaitDHCP(q, xid, budget)
		if err != nil {
			continue
		}
		if m.MsgType == DHCPACK {
			if a, ok := m.OfferFrom(); ok {
				a.ServerID = offer.ServerID
				return a, nil
			}
		}
	}
	return Offer{}, errNoAck
}

// errNoReply/errNoOffer/errNoAck distinguish "the wire is fine but the
// server never answered" from "the wire itself failed", because only the
// second is a defect in this stack.
var (
	errNoReply = errors.New("dhcp: no reply from the server")
	errNoOffer = errors.New("dhcp: no offer after retransmits")
	errNoAck   = errors.New("dhcp: lease not acknowledged")
)

// Lease tracks an acquired lease and answers when it needs attention.
//
// RFC 2131 timing, as fractions of the granted lease time:
//
//	T1 = 50%   renew (DHCPREQUEST to the same server)
//	T2 = 87.5% if the server does not answer, fall back to DISCOVER
//
// The clock is injected rather than read, because the host tests must be
// deterministic and the kernel's only time source is a millisecond counter
// that may not exist at all (HasClock). No wall-clock, no globals.
type Lease struct {
	Offer     Offer
	GrantedMs uint64
	clockMs   func() uint64

	// Renewal backoff state. Consecutive failures double the wait up to
	// renewBackoffMax; a successful renew resets to zero.
	renewFailures  uint64
	renewNextMs    uint64
	renewBackoffMs uint64
}

// renewBackoff defaults.
const (
	renewBackoffInitialMs = 1000
	renewBackoffMaxMs     = 60000
)

// NewLease wraps an acquired lease. clockMs may be nil, in which case the
// lease never becomes due -- a host with no clock cannot renew, and saying
// so is better than inventing a deadline.
func NewLease(o Offer, clockMs func() uint64) *Lease {
	l := &Lease{Offer: o, clockMs: clockMs}
	if o.LeaseTime > 0 {
		l.GrantedMs = uint64(o.LeaseTime) * 1000
	}
	return l
}

// T1Ms / T2Ms are the renew and rebind deadlines in milliseconds.
func (l *Lease) T1Ms() uint64 { return l.GrantedMs / 2 }
func (l *Lease) T2Ms() uint64 { return l.GrantedMs * 7 / 8 }

// BackoffMs returns the current backoff delay. Zero means "try now".
func (l *Lease) BackoffMs() uint64 {
	if l.renewNextMs == 0 {
		return 0
	}
	if l.clockMs == nil {
		return 0
	}
	now := l.clockMs()
	if now < l.renewNextMs {
		return l.renewNextMs - now
	}
	return 0
}

// RecordRenewResult updates backoff state after a renew attempt.
func (l *Lease) RecordRenewResult(ok bool) {
	if ok {
		l.renewFailures = 0
		l.renewBackoffMs = 0
		l.renewNextMs = 0
		return
	}
	l.renewFailures++
	if l.renewBackoffMs == 0 {
		l.renewBackoffMs = renewBackoffInitialMs
	} else {
		l.renewBackoffMs *= 2
		if l.renewBackoffMs > renewBackoffMaxMs {
			l.renewBackoffMs = renewBackoffMaxMs
		}
	}
	if l.clockMs != nil {
		l.renewNextMs = l.clockMs() + l.renewBackoffMs
	}
}

// Due reports whether the lease should be renewed now: past T1, or already
// past T2 in which case the caller must re-DISCOVER rather than re-REQUEST.
// Due also returns false while a backoff is in effect.
func (l *Lease) Due(nowMs uint64) (renew bool, rebind bool) {
	if l.GrantedMs == 0 || l.clockMs == nil {
		return false, false
	}
	if l.BackoffMs() > 0 {
		return false, false
	}
	if nowMs >= l.T2Ms() {
		return false, true
	}
	return nowMs >= l.T1Ms(), false
}

// Expired reports whether the lease is past T2 and should be considered
// unusable even if renewal is still in flight.
func (l *Lease) Expired(nowMs uint64) bool {
	if l.GrantedMs == 0 || l.clockMs == nil {
		return false
	}
	return nowMs >= l.T2Ms()
}

// Renewal defaults. Kept as constants so the guest and the tests cannot
// drift apart on how long a renewal is allowed to take.
const (
	renewHostname = "net"
	renewTries    = 3
	renewBudget   = 20000
)

// NewRenewer returns the renewal step that ServeNetRenewing calls.
//
// It is a named constructor rather than a closure written inside main so
// that the *production* path is host-testable: fresh transaction id, DORA,
// then adopt the new address and its routing. Writing it inline in main put
// all of that behind a wasip1 build tag, which meant the renewal glue could
// only be imitated by tests, never run by them.
func NewRenewer(s *Stack, entropy func([]byte) error) func() (Offer, error) {
	return func() (Offer, error) {
		// A transaction id belongs to one exchange. Reusing the startup id
		// across a renewal invites the server to conflate the two.
		xid, err := NewXID(entropy)
		if err != nil {
			return Offer{}, err
		}
		o, err := s.DHCPAcquire(xid, renewHostname, DefaultDHCPOptions(), renewTries, renewBudget)
		if err != nil {
			return Offer{}, err
		}
		s.IP = o.YIAddr
		s.Netmask = o.Netmask
		s.Gateway = o.Router
		return o, nil
	}
}
