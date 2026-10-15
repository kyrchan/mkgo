package main

import (
	"bytes"
	"errors"
)

// Ethernet II (RFC 894): dst[6] src[6] ethertype[2] payload[..1500].
// Frames pad to the 60-byte minimum on send; parse accepts any length.

const (
	EthHdrLen   = 14
	EthMinLen   = 60
	EthPayloadM = MaxFrame - EthHdrLen // 1512 usable payload over §6

	EthTypeARP  uint16 = 0x0806
	EthTypeIPv4 uint16 = 0x0800
)

var ErrShortFrame = errors.New("net: frame too short")

// MAC is a 48-bit hardware address.
type MAC [6]byte

var BroadcastMAC = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func (m MAC) IsBroadcast() bool { return m == BroadcastMAC }

func (m MAC) String() string {
	const hexdigits = "0123456789abcdef"
	var b [17]byte
	for i := 0; i < 6; i++ {
		b[i*3] = hexdigits[m[i]>>4]
		b[i*3+1] = hexdigits[m[i]&0xf]
		if i < 5 {
			b[i*3+2] = ':'
		}
	}
	return string(b[:])
}

func ParseMAC(s string) (MAC, error) {
	var m MAC
	parts := bytes.Split([]byte(s), []byte(":"))
	if len(parts) != 6 {
		return m, errors.New("net: bad mac")
	}
	for i, p := range parts {
		if len(p) != 2 {
			return m, errors.New("net: bad mac octet")
		}
		hi, ok1 := hexVal(p[0])
		lo, ok2 := hexVal(p[1])
		if !ok1 || !ok2 {
			return m, errors.New("net: bad mac digit")
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

// EthFrame is a parsed inbound ethernet frame.
type EthFrame struct {
	Dst, Src MAC
	Type     uint16
	Payload  []byte
}

// ParseEth decodes an Ethernet II frame. VLAN etc. unsupported (v1).
func ParseEth(raw []byte) (*EthFrame, error) {
	if len(raw) < EthHdrLen {
		return nil, ErrShortFrame
	}
	f := &EthFrame{
		Type: BeGet16(raw[12:14]),
	}
	copy(f.Dst[:], raw[0:6])
	copy(f.Src[:], raw[6:12])
	f.Payload = raw[EthHdrLen:]
	return f, nil
}

// BuildEth assembles one frame (padded to the 60-byte minimum).
func BuildEth(dst, src MAC, ethertype uint16, payload []byte) []byte {
	n := EthHdrLen + len(payload)
	if n < EthMinLen {
		n = EthMinLen
	}
	out := make([]byte, n)
	copy(out[0:6], dst[:])
	copy(out[6:12], src[:])
	BePut16(out[12:14], ethertype)
	copy(out[EthHdrLen:], payload)
	return out
}
