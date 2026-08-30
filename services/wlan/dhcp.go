package main

import (
	"errors"
	"time"
)

// DHCP (RFC 2131) — minimal client state machine for wlan.wasm Phase 12.

const (
	DHCPOpBootRequest = 1
	DHCPOpBootReply   = 2

	DHCPMsgDiscover  = 1
	DHCPMsgOffer     = 2
	DHCPMsgRequest   = 3
	DHCPMsgDecline   = 4
	DHCPMsgACK       = 5
	DHCPMsgNACK      = 6

	DHCPServerPort = 67
	DHCPClientPort = 68

	DHCPOptionLeaseTime    = 51
	DHCPOptionSubnet       = 1
	DHCPOptionRouter       = 3
	DHCPOptionDNSServer    = 6
	DHCPOptionClientID     = 61
	DHCPOptionDHCPType     = 53
	DHCPOptionDHCPServer   = 54
	DHCPOptionEnd         = 255
)

var (
	ErrDHCP     = errors.New("wlan: malformed dhcp")
	ErrDHCPTimeout = errors.New("wlan: dhcp timeout")
)

// DhcpPacket is a parsed DHCP message.
type DhcpPacket struct {
	Op    uint8
	XID   uint32
	CIAddr IP4
	YIAddr IP4
	SIAddr IP4
	GIAddr IP4
	CHAddr MAC
	MType uint8 // DHCP Message Type option value
	Opts  map[uint8][]byte
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
		Op:    p[0],
		XID:   beGet32(p[4:8]),
		Opts:  make(map[uint8][]byte),
	}
	copy(d.CIAddr[:], p[12:16])
	copy(d.YIAddr[:], p[16:20])
	copy(d.SIAddr[:], p[20:24])
	copy(d.GIAddr[:], p[24:28])
	copy(d.CHAddr[:6], p[28:34])

	// Options: skip the 4-byte magic cookie (0x6382536), then TLV.
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
	bePut16(out[8:10], 0)      // seconds
	bePut16(out[10:12], 0x8000) // flags: broadcast
	copy(out[12:16], d.CIAddr[:])
	copy(out[16:20], d.YIAddr[:])
	copy(out[20:24], d.SIAddr[:])
	copy(out[24:28], d.GIAddr[:])
	copy(out[28:34], d.CHAddr[:6])
	bePut32(out[236:240], 0x6382536) // magic cookie

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
	IP   IP4
	Mask IP4
	GW   IP4
}

func (d *DhcpResult) Empty() bool { return d.IP == (IP4{}) }

func parseDhcpOptsForLease(d *DhcpPacket) DhcpResult {
	r := DhcpResult{}
	copy(r.IP[:], d.YIAddr[:])
	if v, ok := d.Opts[DHCPOptionSubnet]; ok && len(v) >= 4 {
		copy(r.Mask[:], v[:4])
	}
	if v, ok := d.Opts[DHCPOptionRouter]; ok && len(v) >= 4 {
		copy(r.GW[:], v[:4])
	}
	return r
}

// DhcpClient is a minimal DHCP client that exchanges packets as 802.3
// broadcast Ethernet frames through an OffloadTransport.
type DhcpClient struct {
	off   OffloadTransport
	mac   MAC
	xid   uint32
	state int
	budget int
}

const (
	dhcpStateIdle     = 0
	dhcpStateOffered  = 1
	dhcpStateBound    = 2
)

// NewDhcpClient creates a DHCP client with a random XID seeded from the MAC.
func newDhcpClient(off OffloadTransport, mac MAC) *DhcpClient {
	xid := uint32(mac[0])<<24 | uint32(mac[1])<<16 | uint32(mac[2])<<8 | uint32(mac[3])
	return &DhcpClient{
		off:   off,
		mac:   mac,
		xid:   xid,
		budget: 5000,
	}
}

func (d *DhcpClient) buildDiscover() []byte {
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CHAddr: d.mac,
		MType:  DHCPMsgDiscover,
	}
	dg := buildUDP(DHCPClientPort, DHCPServerPort, pkt.build())
	ip := IP4{0, 0, 0, 0}
	ipDg, _ := buildIPv4(ip, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, d.mac, EthTypeIPv4, ipDg)
}

func (d *DhcpClient) buildRequest(offerIP IP4) []byte {
	pkt := &DhcpPacket{
		Op:     DHCPOpBootRequest,
		XID:    d.xid,
		CIAddr: IP4{},
		YIAddr: offerIP,
		CHAddr: d.mac,
		MType:  DHCPMsgRequest,
	}
	dg := buildUDP(DHCPClientPort, DHCPServerPort, pkt.build())
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
	d.off.SendFrame(d.buildRequest(offer.YIAddr))
	ack, ok := d.pollACK(5 * time.Second)
	if !ok {
		return DhcpResult{}, ErrDHCPTimeout
	}
	d.state = dhcpStateBound
	return parseDhcpOptsForLease(ack), nil
}
