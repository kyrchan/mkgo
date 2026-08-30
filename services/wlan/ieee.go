package main

import (
	"errors"

	lib "kernel.lane/guests/lib"
)

// Big-endian wire helpers (network byte order for Ethernet/IP/UDP/DHCP).
func bePut16(b []byte, v uint16) { b[0] = byte(v >> 8); b[1] = byte(v) }
func beGet16(b []byte) uint16      { return uint16(b[0])<<8 | uint16(b[1]) }
func bePut32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
func beGet32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// MAC is a 48-bit hardware address.
type MAC [6]byte

// BroadcastMAC is the Ethernet broadcast address.
var BroadcastMAC = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func (m MAC) String() string {
	const h = "0123456789abcdef"
	var b [17]byte
	for i := 0; i < 6; i++ {
		b[i*3] = h[m[i]>>4]
		b[i*3+1] = h[m[i]&0xf]
		if i < 5 {
			b[i*3+2] = ':'
		}
	}
	return string(b[:])
}

func parseMAC(s string) (MAC, error) {
	var m MAC
	if len(s) != 17 {
		return m, errors.New("wlan: bad mac")
	}
	for i := 0; i < 6; i++ {
		hi, ok1 := hexVal(s[i*3])
		lo, ok2 := hexVal(s[i*3+1])
		if !ok1 || !ok2 {
			return m, errors.New("wlan: bad mac hex")
		}
		m[i] = hi<<4 | lo
	}
	return m, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// IP4 is an IPv4 address (raw 4 bytes, no endianness).
type IP4 [4]byte

func (ip IP4) String() string {
	return itoa(int(ip[0])) + "." + itoa(int(ip[1])) + "." +
		itoa(int(ip[2])) + "." + itoa(int(ip[3]))
}

func parseIP(s string) (IP4, error) {
	var ip IP4
	parts := splitFields(s, '.')
	if len(parts) != 4 {
		return ip, errors.New("wlan: bad ip")
	}
	for i, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return ip, errors.New("wlan: bad ip octet")
		}
		v := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return ip, errors.New("wlan: bad ip digit")
			}
			v = v*10 + int(ch-'0')
		}
		if v > 255 {
			return ip, errors.New("wlan: bad ip octet")
		}
		ip[i] = byte(v)
	}
	return ip, nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func splitFields(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// ---- 802.11 management frames ----

// MgmtFrame is a parsed 802.11 management frame.
type MgmtFrame struct {
	FrameControl uint16
	Addr1        MAC // DA / receiver
	Addr2        MAC // SA / transmitter (BSSID for beacons)
	Addr3        MAC // BSSID / addr3
	Body         []byte // from SeqCtrl onward: varies by subtype
}

// 802.11 management frame subtypes (subset).
const (
	MgmtBeacon      uint8 = 8
	MgmtAssocResp   uint8 = 1
	MgmtProbeResp   uint8 = 5
)

// 802.11 fixed fields within a beacon body (after SeqCtrl):
// BeaconInterval(2) + Capabilities(2), so IEs start at body[4].
const beaconFixedLen = 4

// Status codes for association.
const (
	AssocSuccess           uint16 = 0
	AssocRefusedReason1    uint16 = 1  // unspecified
	AssocRefusedCapability uint16 = 13 // capability not supported
)

// ElemID / IE element IDs.
const (
	EIE_SSID        uint8 = 0
	EIE_SupportedRates uint8 = 1
	EIE_DS          uint8 = 3
	EIE_Cap         uint8 = 7
)

// ParseMgmt parses an 802.11 management frame (FrameTypeData body = full
// 802.11 frame, NOT yet stripped of headers).
func parseMgmt(raw []byte) (*MgmtFrame, error) {
	if len(raw) < 24 {
		return nil, errors.New("wlan: short management frame")
	}
	m := &MgmtFrame{
		FrameControl: lib.Get16(raw[0:2]), // 802.11 uses LE
	}
	copy(m.Addr1[:], raw[4:10])
	copy(m.Addr2[:], raw[10:16])
	copy(m.Addr3[:], raw[16:22])
	m.Body = raw[24:]
	return m, nil
}

// Subtype extracts the management subtype from FrameControl.
func (m *MgmtFrame) Subtype() uint8 {
	return uint8((m.FrameControl >> 4) & 0xF)
}

// BeaconIEs parses Information Elements from a beacon body.
type BeaconIEs struct {
	SSID   string
	SSIDOk bool
	DS     uint8 // DS parameter set: current channel
}

func parseBeaconIEs(body []byte) (*BeaconIEs, bool) {
	if len(body) < beaconFixedLen {
		return nil, false
	}
	e := &BeaconIEs{}
	p := body[beaconFixedLen:]
	for len(p) >= 2 {
		eid := p[0]
		elen := int(p[1])
		p = p[2:]
		if len(p) < elen {
			break
		}
		switch eid {
		case EIE_SSID:
			e.SSID = string(p[:elen])
			e.SSIDOk = true
		case EIE_DS:
			if elen >= 1 {
				e.DS = p[0]
			}
		}
		p = p[elen:]
	}
	return e, true
}

// ParseBeacon decodes a beacon frame body (MgmtFrame.Body).
func parseBeacon(body []byte) (ssid string, chan_ uint8, ok bool) {
	ies, ok := parseBeaconIEs(body)
	if !ok {
		return "", 0, false
	}
	return ies.SSID, ies.DS, true
}

// ParseAssocResp decodes an association response. Returns status code and
// association ID. ok=false on truncated frame.
func parseAssocResp(body []byte) (status, aid uint16, ok bool) {
	if len(body) < 4 {
		return 0, 0, false
	}
	return beGet16(body[0:2]), beGet16(body[2:4]), true
}

// ---- 802.3 Ethernet frames ----

const (
	EthHdrLen  = 14
	EthTypeIPv4 uint16 = 0x0800
)

// EthFrame is a parsed 802.3 Ethernet II frame.
type EthFrame struct {
	Dst    MAC
	Src    MAC
	Type   uint16
	Payload []byte
}

func parseEth(raw []byte) (*EthFrame, error) {
	if len(raw) < EthHdrLen {
		return nil, errors.New("wlan: short ethernet frame")
	}
	f := &EthFrame{
		Type:    beGet16(raw[12:14]),
		Payload: raw[EthHdrLen:],
	}
	copy(f.Dst[:], raw[0:6])
	copy(f.Src[:], raw[6:12])
	return f, nil
}

// buildEth assembles an Ethernet frame.
func buildEth(dst, src MAC, ethertype uint16, payload []byte) []byte {
	out := make([]byte, EthHdrLen+len(payload))
	copy(out[0:6], dst[:])
	copy(out[6:12], src[:])
	bePut16(out[12:14], ethertype)
	copy(out[EthHdrLen:], payload)
	return out
}

// ---- IPv4 ----

const (
	IP4HdrLen    = 20
	IP4ProtoICMP uint8 = 1
	IP4ProtoUDP  uint8 = 17
)

type IP4Packet struct {
	Src, Dst IP4
	Proto    uint8
	TTL      uint8
	Payload  []byte
}

func parseIPv4(p []byte) (*IP4Packet, error) {
	if len(p) < IP4HdrLen {
		return nil, errors.New("wlan: short ipv4")
	}
	if p[0]>>4 != 4 || p[0]&0x0f != 5 {
		return nil, errors.New("wlan: ipv4 v/hw unsupported")
	}
	total := int(beGet16(p[2:4]))
	if total < IP4HdrLen || total > len(p) {
		return nil, errors.New("wlan: ipv4 length")
	}
	if beGet16(p[6:8])&0x1FFF != 0 {
		return nil, errors.New("wlan: ipv4 fragmented")
	}
	return &IP4Packet{
		Src:     IP4{p[12], p[13], p[14], p[15]},
		Dst:     IP4{p[16], p[17], p[18], p[19]},
		Proto:   p[9],
		TTL:     p[8],
		Payload: p[IP4HdrLen:total],
	}, nil
}

// buildIPv4 constructs a minimal IPv4 datagram (DF set, no options).
func buildIPv4(src, dst IP4, proto uint8, payload []byte) ([]byte, error) {
	if len(payload) > 1500-IP4HdrLen {
		return nil, errors.New("wlan: ipv4 payload too big")
	}
	out := make([]byte, IP4HdrLen+len(payload))
	out[0] = 0x45
	bePut16(out[2:4], uint16(len(out)))
	out[6], out[7] = 0x40, 0x00 // DF
	out[8] = 64
	out[9] = proto
	copy(out[12:16], src[:])
	copy(out[16:20], dst[:])
	bePut16(out[10:12], ipChecksum(out[:IP4HdrLen]))
	copy(out[IP4HdrLen:], payload)
	return out, nil
}

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(beGet16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// ---- UDP ----

const UDPHdrLen = 8

type UDPDatagram struct {
	SrcPort uint16
	DstPort uint16
	Data    []byte
}

func parseUDP(p []byte) (*UDPDatagram, error) {
	if len(p) < UDPHdrLen {
		return nil, errors.New("wlan: short udp")
	}
	total := int(beGet16(p[4:6]))
	if total < UDPHdrLen || total > len(p) {
		return nil, errors.New("wlan: udp length")
	}
	return &UDPDatagram{
		SrcPort: beGet16(p[0:2]),
		DstPort: beGet16(p[2:4]),
		Data:    p[UDPHdrLen:total],
	}, nil
}

func buildUDP(srcPort, dstPort uint16, data []byte) []byte {
	out := make([]byte, UDPHdrLen+len(data))
	bePut16(out[0:2], srcPort)
	bePut16(out[2:4], dstPort)
	bePut16(out[4:6], uint16(len(out)))
	bePut16(out[6:8], 0) // checksum 0 = no checksum
	copy(out[UDPHdrLen:], data)
	return out
}

// parseUDPEth parses Ethernet → IPv4 → UDP from a raw 802.3 frame.
func parseUDPEth(raw []byte) (*EthFrame, *IP4Packet, *UDPDatagram, error) {
	f, err := parseEth(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	if f.Type != EthTypeIPv4 {
		return nil, nil, nil, errors.New("wlan: not ipv4")
	}
	ip, err := parseIPv4(f.Payload)
	if err != nil {
		return nil, nil, nil, err
	}
	udp, err := parseUDP(ip.Payload)
	if err != nil {
		return nil, nil, nil, err
	}
	return f, ip, udp, nil
}
