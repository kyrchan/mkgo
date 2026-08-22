package main

import (
	"bytes"
	"errors"
	"sync"
	"time"
)

// ARP (RFC 826) over Ethernet/IPv4: htype 1, ptype 0x0800, hlen 6,
// plen 4. Packet is 28 bytes inside the ethernet payload.

const (
	ARPHwEther   uint16 = 1
	ARPProtoIPv4 uint16 = 0x0800

	ARPLen = 28

	ARPOpRequest uint16 = 1
	ARPOpReply   uint16 = 2
)

var ErrBadARP = errors.New("net: malformed arp")

type ARPPacket struct {
	Oper   uint16
	SrcMAC MAC
	SrcIP  IP4
	DstMAC MAC
	DstIP  IP4
}

func ParseARP(p []byte) (*ARPPacket, error) {
	if len(p) < ARPLen {
		return nil, ErrShortFrame
	}
	if BeGet16(p[0:2]) != ARPHwEther || BeGet16(p[2:4]) != ARPProtoIPv4 ||
		p[4] != 6 || p[5] != 4 {
		return nil, ErrBadARP
	}
	a := &ARPPacket{Oper: BeGet16(p[6:8])}
	copy(a.SrcMAC[:], p[8:14])
	copy(a.SrcIP[:], p[14:18])
	copy(a.DstMAC[:], p[18:24])
	copy(a.DstIP[:], p[24:28])
	return a, nil
}

func (a *ARPPacket) Build() []byte {
	p := make([]byte, ARPLen)
	BePut16(p[0:2], ARPHwEther)
	BePut16(p[2:4], ARPProtoIPv4)
	p[4], p[5] = 6, 4
	BePut16(p[6:8], a.Oper)
	copy(p[8:14], a.SrcMAC[:])
	copy(p[14:18], a.SrcIP[:])
	copy(p[18:24], a.DstMAC[:])
	copy(p[24:28], a.DstIP[:])
	return p
}

// IP4 is an IPv4 address.
type IP4 [4]byte

func (ip IP4) String() string {
	return itoa(int(ip[0])) + "." + itoa(int(ip[1])) + "." +
		itoa(int(ip[2])) + "." + itoa(int(ip[3]))
}

// ParseIP parses "a.b.c.d" dotted-quad form.
func ParseIP(s string) (IP4, error) {
	var ip IP4
	parts := bytes.Split([]byte(s), []byte("."))
	if len(parts) != 4 {
		return ip, errors.New("net: bad ip")
	}
	for i, p := range parts {
		v := 0
		if len(p) == 0 || len(p) > 3 {
			return ip, errors.New("net: bad ip octet")
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return ip, errors.New("net: bad ip digit")
			}
			v = v*10 + int(ch-'0')
		}
		if v > 255 {
			return ip, errors.New("net: bad ip octet")
		}
		ip[i] = byte(v)
	}
	return ip, nil
}

// MustIP panics on malformed input; tests/config only.
func MustIP(s string) IP4 {
	ip, err := ParseIP(s)
	if err != nil {
		panic(err)
	}
	return ip
}

func itoa(v int) string { return uitoa64(uint64(v)) }

func uitoa64(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// ---- ARP cache + resolver ----

const arpEntryTTL = 30 * time.Second

type arpEntry struct {
	mac     MAC
	expires time.Time
}

// ARPCache maps IPv4 → MAC with TTL expiry (-race safe: the stack's
// single serve goroutine owns it; mutex guards test-time direct access).
type ARPCache struct {
	mu sync.Mutex
	m  map[[4]byte]arpEntry
}

func NewARPCache() *ARPCache { return &ARPCache{m: make(map[[4]byte]arpEntry)} }

func (c *ARPCache) Lookup(ip IP4) (MAC, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[ip]
	if !ok || time.Now().After(e.expires) {
		return MAC{}, false
	}
	return e.mac, true
}

func (c *ARPCache) Update(ip IP4, mac MAC) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[ip] = arpEntry{mac: mac, expires: time.Now().Add(arpEntryTTL)}
}

// Resolve returns the cached MAC for ip or broadcasts one ARP request and
// waits for the stack's receive loop to fill the cache. waitFn yields
// between polls and returns false to abort.
func (s *Stack) Resolve(ip IP4, budget int) (MAC, error) {
	if mac, ok := s.arp.Lookup(ip); ok {
		return mac, nil
	}
	frame := BuildEth(BroadcastMAC, s.MAC, EthTypeARP, (&ARPPacket{
		Oper:   ARPOpRequest,
		SrcMAC: s.MAC,
		SrcIP:  s.IP,
		DstIP:  ip,
	}).Build())
	s.sink.Send(frame)
	for i := 0; i < budget; i++ {
		if mac, ok := s.arp.Lookup(ip); ok {
			return mac, nil
		}
		s.pump() // service inbound frames (replies) while waiting
		time.Sleep(100 * time.Microsecond)
	}
	return MAC{}, errors.New("net: arp timeout")
}
