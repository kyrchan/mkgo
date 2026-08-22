package main

import (
	"errors"
)

// IPv4 (RFC 791) — no options, no fragmentation in v1 (DF set; oversized
// datagrams are rejected locally). Header is 20 bytes.

const (
	IP4HdrLen    = 20
	IP4ProtoICMP = 1
	IP4ProtoUDP  = 17
	IP4ProtoTCP  = 6
)

var ErrIPv4 = errors.New("net: malformed ipv4")
var ErrTooBig = errors.New("net: payload exceeds mtu (no fragmentation)")

type IP4Packet struct {
	Src, Dst IP4
	Proto    uint8
	TTL      uint8
	Payload  []byte // L4 datagram
}

func Checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(BeGet16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func ParseIP4(p []byte) (*IP4Packet, error) {
	if len(p) < IP4HdrLen {
		return nil, ErrShortFrame
	}
	if p[0]>>4 != 4 || p[0]&0x0f != 5 {
		return nil, ErrIPv4 // v1: no options
	}
	total := int(BeGet16(p[2:4]))
	if total < IP4HdrLen || total > len(p) {
		return nil, ErrIPv4
	}
	if BeGet16(p[6:8]) != 0x4000 { // exactly DF set, MF=0, offset=0
		return nil, ErrIPv4
	}
	pkt := &IP4Packet{
		Proto:   p[9],
		TTL:     p[8],
		Payload: p[IP4HdrLen:total],
	}
	copy(pkt.Src[:], p[12:16])
	copy(pkt.Dst[:], p[16:20])
	if Checksum(p[:IP4HdrLen]) != 0 {
		return nil, ErrIPv4
	}
	return pkt, nil
}

// Build assembles the IPv4 datagram with a valid checksum and DF set.
func (p *IP4Packet) Build() ([]byte, error) {
	if len(p.Payload) > EthPayloadM-IP4HdrLen {
		return nil, ErrTooBig
	}
	out := make([]byte, IP4HdrLen+len(p.Payload))
	out[0] = 0x45 // v4, ihl 5
	BePut16(out[2:4], uint16(len(out)))
	out[6], out[7] = 0x40, 0x00 // DF, no fragment offset
	out[8] = p.TTL
	if out[8] == 0 {
		out[8] = 64
	}
	out[9] = p.Proto
	copy(out[12:16], p.Src[:])
	copy(out[16:20], p.Dst[:])
	BePut16(out[10:12], Checksum(out[:IP4HdrLen]))
	copy(out[IP4HdrLen:], p.Payload)
	return out, nil
}

// ---- ICMP echo (RFC 792) ----

const (
	ICMPEchoReply   uint8 = 0
	ICMPEchoRequest uint8 = 8
)

var ErrBadICMP = errors.New("net: malformed icmp")

type ICMPPacket struct {
	Type uint8
	Code uint8
	ID   uint16
	Seq  uint16
	Data []byte
}

func ParseICMP(p []byte) (*ICMPPacket, error) {
	if len(p) < 8 {
		return nil, ErrShortFrame
	}
	if Checksum(p) != 0 {
		return nil, ErrBadICMP
	}
	c := &ICMPPacket{Type: p[0], Code: p[1]}
	c.ID = BeGet16(p[4:6])
	c.Seq = BeGet16(p[6:8])
	c.Data = append([]byte(nil), p[8:]...)
	return c, nil
}

func (c *ICMPPacket) Build() []byte {
	out := make([]byte, 8+len(c.Data))
	out[0] = c.Type
	out[1] = c.Code
	BePut16(out[4:6], c.ID)
	BePut16(out[6:8], c.Seq)
	copy(out[8:], c.Data)
	BePut16(out[2:4], Checksum(out))
	return out
}

// BuildEchoReply mirrors an echo request back to its source.
func BuildEchoReply(req *ICMPPacket) []byte {
	r := &ICMPPacket{Type: ICMPEchoReply, ID: req.ID, Seq: req.Seq, Data: req.Data}
	return r.Build()
}
