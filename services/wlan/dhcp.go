package main

import (
	"errors"
	"time"
)

// DHCP (RFC 2131) — minimal client state machine for wlan.wasm Phase 12.

const (
	DHCPOpBootRequest = 1
	DHCPOpBootReply   = 2

	DHCPMsgDiscover = 1
	DHCPMsgOffer    = 2
	DHCPMsgRequest  = 3
	DHCPMsgDecline  = 4
	DHCPMsgACK      = 5
	DHCPMsgNACK     = 6
	DHCPMsgRelease  = 7

	DHCPServerPort = 67
	DHCPClientPort = 68

	DHCPOptionLeaseTime   = 51
	DHCPOptionSubnet      = 1
	DHCPOptionRouter      = 3
	DHCPOptionDNSServer   = 6
	DHCPOptionClientID    = 61
	DHCPOptionDHCPType    = 53
	DHCPOptionDHCPServer  = 54
	DHCPOptionRequestedIP = 50
	DHCPOptionEnd         = 255

	dhcpDefaultLease = 2 * time.Hour
	maxRenewAttempts = 3
)

var (
	ErrDHCP        = errors.New("wlan: malformed dhcp")
	ErrDHCPTimeout = errors.New("wlan: dhcp timeout")
)

// DhcpPacket is a parsed DHCP message.
type DhcpPacket struct {
	Op     uint8
	XID    uint32
	CIAddr IP4
	YIAddr IP4
	SIAddr IP4
	GIAddr IP4
	CHAddr MAC
	MType  uint8 // DHCP Message Type option value
	Opts   map[uint8][]byte
}

// ParseDhcp parses a DHCP packet (the UDP payload of a DHCP frame).
func parseDhcp(p []byte) (*DhcpPacket, error) {
	if len(p) < 236 {
		return nil, ErrDHCP
	}
	if p[0] != DHCPOpBootRequest && p[0] != DHCPOpBootReply {
		return nil, ErrDHCP
	}
	d := &DhcpPacket{
		Op:   p[0],
		XID:  beGet32(p[4:8]),
		Opts: make(map[uint8][]byte),
	}
	copy(d.CIAddr[:], p[12:16])
	copy(d.YIAddr[:], p[16:20])
	copy(d.SIAddr[:], p[20:24])
	copy(d.GIAddr[:], p[24:28])
	copy(d.CHAddr[:6], p[28:34])

	// Options: skip the 4-byte magic cookie (99.130.83.99), then TLV.
	opts := p[236:]
	if len(opts) < 4 {
		opts = p[236:]
	} else {
		opts = opts[4:]
	}
	parseOpts(d.Opts, opts)
	if mt, ok := d.Opts[DHCPOptionDHCPType]; ok && len(mt) == 1 {
		d.MType = mt[0]
	}
	return d, nil
}

func parseOpts(m map[uint8][]byte, opts []byte) {
	for len(opts) >= 2 {
		eid := opts[0]
		if eid == DHCPOptionEnd {
			return
		}
		elen := int(opts[1])
		opts = opts[2:]
		if len(opts) < elen {
			return
		}
		m[eid] = append([]byte(nil), opts[:elen]...)
		opts = opts[elen:]
	}
}

// build constructs a DHCP packet for sending.
func (d *DhcpPacket) build() []byte {
	out := make([]byte, 240)
	out[0] = d.Op
	out[1] = 1 // htype: Ethernet
	out[2] = 6 // hlen
	out[3] = 0 // hops
	bePut32(out[4:8], d.XID)
	bePut16(out[8:10], 0)       // seconds
	bePut16(out[10:12], 0x8000) // flags: broadcast
	copy(out[12:16], d.CIAddr[:])
	copy(out[16:20], d.YIAddr[:])
	copy(out[20:24], d.SIAddr[:])
	copy(out[24:28], d.GIAddr[:])
	copy(out[28:34], d.CHAddr[:6])
	bePut32(out[236:240], 0x63825363) // magic cookie 99.130.83.99

	// Build options from the Opts map (deterministic order: type first).
	opt := make([]byte, 0, 128)
	opt = append(opt, DHCPOptionDHCPType, 1, d.MType)
	// Client Identifier (option 61, hardware type 1 = Ethernet)
	opt = append(opt, DHCPOptionClientID, 7, 1)
	opt = append(opt, d.CHAddr[:6]...)
	if optData, ok := d.Opts[DHCPOptionSubnet]; ok {
		opt = append(opt, DHCPOptionSubnet, byte(len(optData)))
		opt = append(opt, optData...)
	}
	if optData, ok := d.Opts[DHCPOptionRouter]; ok {
		opt = append(opt, DHCPOptionRouter, byte(len(optData)))
		opt = append(opt, optData...)
	}
	for id, val := range d.Opts {
		if id == DHCPOptionDHCPType || id == DHCPOptionClientID || id == DHCPOptionSubnet || id == DHCPOptionRouter {
			continue
		}
		opt = append(opt, id, byte(len(val)))
		opt = append(opt, val...)
	}
	opt = append(opt, DHCPOptionEnd)
	return append(out, opt...)
}

// DhcpResult holds the lease parameters from a DHCPACK.
type DhcpResult struct {
	IP        IP4
	Mask      IP4
	GW        IP4
	ServerID  IP4 // DHCP server identifier (option 54)
	LeaseTime time.Duration
	T1        time.Duration // renewal timer (0.5 × lease)
	T2        time.Duration // rebinding timer (0.875 × lease)
}

func (d *DhcpResult) Empty() bool { return d.IP == (IP4{}) }

func parseDhcpOptsForLease(d *DhcpPacket) DhcpResult {
	r := DhcpResult{LeaseTime: dhcpDefaultLease}
	copy(r.IP[:], d.YIAddr[:])
	if v, ok := d.Opts[DHCPOptionSubnet]; ok && len(v) >= 4 {
		copy(r.Mask[:], v[:4])
	}
	if v, ok := d.Opts[DHCPOptionRouter]; ok && len(v) >= 4 {
		copy(r.GW[:], v[:4])
	}
	if v, ok := d.Opts[DHCPOptionLeaseTime]; ok && len(v) >= 4 {
		secs := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
		r.LeaseTime = time.Duration(secs) * time.Second
	}
	if v, ok := d.Opts[DHCPOptionDHCPServer]; ok && len(v) >= 4 {
		copy(r.ServerID[:], v[:4])
	}
	r.T1 = r.LeaseTime / 2
	r.T2 = time.Duration(float64(r.LeaseTime) * 0.875)
	return r
}

// DhcpClient is a minimal DHCP client that exchanges packets as 802.3
// broadcast Ethernet frames through an OffloadTransport.
type DhcpClient struct {
	off        OffloadTransport
	mac        MAC
	xid        uint32
	state      int
	budget     int
	serverPort int // 0 = default (DHCPServerPort)
}

const (
	dhcpStateIdle    = 0
	dhcpStateOffered = 1
	dhcpStateBound   = 2
)

// NewDhcpClient creates a DHCP client with a random XID seeded from the MAC.
func newDhcpClient(off OffloadTransport, mac MAC) *DhcpClient {
	xid := uint32(mac[0])<<24 | uint32(mac[1])<<16 | uint32(mac[2])<<8 | uint32(mac[3])
	return &DhcpClient{
		off:        off,
		mac:        mac,
		xid:        xid,
		budget:     5000,
		serverPort: 0,
	}
}

func (d *DhcpClient) dstPort() uint16 {
	if d.serverPort != 0 {
		return uint16(d.serverPort)
	}
	return DHCPServerPort
}

func (d *DhcpClient) buildDiscover() []byte {
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CHAddr: d.mac,
		MType:  DHCPMsgDiscover,
	}
	dg := buildUDP(DHCPClientPort, d.dstPort(), pkt.build())
	ip := IP4{0, 0, 0, 0}
	ipDg, _ := buildIPv4(ip, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, d.mac, EthTypeIPv4, ipDg)
}

// buildRequest constructs the SELECTING-state DHCPREQUEST (RFC 2131 §4.3.2).
// The requested IP (option 50) and the server identifier (option 54) are
// mandatory here: without them the server does not know which offer is being
// selected and answers with yiaddr 0.0.0.0, leaving the client with no address.
// ciaddr stays zero in SELECTING state, and the offered IP belongs in the
// option, not in yiaddr -- yiaddr is a server-to-client field.
func (d *DhcpClient) buildRequest(offer *DhcpPacket) []byte {
	reqIP := offer.YIAddr
	var srvID IP4
	if v, ok := offer.Opts[DHCPOptionDHCPServer]; ok && len(v) >= 4 {
		copy(srvID[:], v[:4])
	}
	if srvID == (IP4{}) {
		srvID = offer.SIAddr
	}
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CIAddr: IP4{},
		CHAddr: d.mac,
		MType:  DHCPMsgRequest,
		Opts: map[uint8][]byte{
			DHCPOptionRequestedIP: reqIP[:],
			DHCPOptionDHCPServer:  srvID[:],
		},
	}
	dg := buildUDP(DHCPClientPort, d.dstPort(), pkt.build())
	ip := IP4{0, 0, 0, 0}
	ipDg, _ := buildIPv4(ip, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, d.mac, EthTypeIPv4, ipDg)
}

// pollOffer waits for a DHCPOFFER matching our XID.
func (d *DhcpClient) pollOffer(timeout time.Duration) (*DhcpPacket, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f, ok := d.off.RecvFrame(); ok {
			_, _, udp, err := parseUDPEth(f)
			if err != nil {
				continue
			}
			dhcp, err := parseDhcp(udp.Data)
			if err != nil {
				continue
			}
			if dhcp.XID == d.xid && dhcp.MType == DHCPMsgOffer {
				return dhcp, true
			}
		}
		d.yield()
	}
	return nil, false
}

// pollACK waits for a DHCPACK matching our XID.
func (d *DhcpClient) pollACK(timeout time.Duration) (*DhcpPacket, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f, ok := d.off.RecvFrame(); ok {
			_, _, udp, err := parseUDPEth(f)
			if err != nil {
				continue
			}
			dhcp, err := parseDhcp(udp.Data)
			if err != nil {
				continue
			}
			if dhcp.XID == d.xid && dhcp.MType == DHCPMsgACK {
				return dhcp, true
			}
		}
		d.yield()
	}
	return nil, false
}

func (d *DhcpClient) yield() {
	time.Sleep(time.Millisecond)
}

// Run executes the DORA sequence and returns the lease on success.
func (d *DhcpClient) Run() (DhcpResult, error) {
	d.off.SendFrame(d.buildDiscover())
	offer, ok := d.pollOffer(5 * time.Second)
	if !ok {
		return DhcpResult{}, ErrDHCPTimeout
	}
	d.state = dhcpStateOffered
	d.off.SendFrame(d.buildRequest(offer))
	ack, ok := d.pollACK(5 * time.Second)
	if !ok {
		return DhcpResult{}, ErrDHCPTimeout
	}
	d.state = dhcpStateBound
	return parseDhcpOptsForLease(ack), nil
}

// buildRenew constructs a DHCPREQUEST for lease renewal (RFC 2131 §4.4.2).
func (d *DhcpClient) buildRenew(lease DhcpResult) []byte {
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CIAddr: lease.IP,
		CHAddr: d.mac,
		MType:  DHCPMsgRequest,
		Opts: map[uint8][]byte{
			DHCPOptionRequestedIP: lease.IP[:],
			DHCPOptionDHCPServer:  lease.ServerID[:],
		},
	}
	dg := buildUDP(DHCPClientPort, d.dstPort(), pkt.build())
	ipDg, _ := buildIPv4(lease.IP, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, d.mac, EthTypeIPv4, ipDg)
}

// Renew sends a DHCPREQUEST to renew the lease, with exponential backoff
// on failure. Returns the renewed lease on success.
func (d *DhcpClient) Renew(lease DhcpResult) (DhcpResult, error) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for attempt := 0; attempt < maxRenewAttempts; attempt++ {
		d.state = dhcpStateBound
		d.off.SendFrame(d.buildRenew(lease))
		ack, ok := d.pollACK(2 * time.Second)
		if ok {
			return parseDhcpOptsForLease(ack), nil
		}
		d.yield()
		if attempt < maxRenewAttempts-1 {
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
		}
	}
	return DhcpResult{}, ErrDHCPTimeout
}

// Release sends a DHCPRELEASE for the current lease (RFC 2131 §4.4.4).
func (d *DhcpClient) Release(lease DhcpResult) error {
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CIAddr: lease.IP,
		CHAddr: d.mac,
		MType:  DHCPMsgRelease,
		Opts: map[uint8][]byte{
			DHCPOptionDHCPServer: lease.ServerID[:],
		},
	}
	dg := buildUDP(DHCPClientPort, d.dstPort(), pkt.build())
	ipDg, _ := buildIPv4(lease.IP, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	d.off.SendFrame(buildEth(BroadcastMAC, d.mac, EthTypeIPv4, ipDg))
	return nil
}
