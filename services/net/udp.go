package main

import (
	"errors"
	"sync"
)

// UDP (RFC 768): 8-byte header {srcPort, dstPort, len, checksum}.
// Checksum may be 0 (IPv4 allows skipping it); we compute it anyway for
// loopback fidelity but accept zero on receive.

const UDPHdrLen = 8
const MaxUDPPayload = EthPayloadM - IP4HdrLen - UDPHdrLen

var ErrUDP = errors.New("net: malformed udp")

type UDPDatagram struct {
	SrcPort     uint16
	DstPort     uint16
	Data        []byte
	checksumSrc IP4 // pseudo-header endpoints for Build (wire checksum)
	checksumDst IP4
}

// Build2 renders the segment using the stack-provided pseudo-header.
func (d *UDPDatagram) Build2() []byte { return d.Build() }

func ParseUDP(p []byte) (*UDPDatagram, error) {
	if len(p) < UDPHdrLen {
		return nil, ErrShortFrame
	}
	total := int(BeGet16(p[4:6]))
	if total < UDPHdrLen || total > len(p) {
		return nil, ErrUDP
	}
	return &UDPDatagram{
		SrcPort: BeGet16(p[0:2]),
		DstPort: BeGet16(p[2:4]),
		Data:    p[UDPHdrLen:total],
	}, nil
}

// Build assembles the UDP segment. checksumSrc/Dst carry the IPv4
// pseudo-header endpoints; real-world hosts (slirp included) validate
// the checksum WITH the pseudo-header, so it is mandatory on the wire.
func (d *UDPDatagram) Build() []byte {
	checksumSrc, checksumDst := d.checksumSrc, d.checksumDst
	out := make([]byte, UDPHdrLen+len(d.Data))
	BePut16(out[0:2], d.SrcPort)
	BePut16(out[2:4], d.DstPort)
	BePut16(out[4:6], uint16(len(out)))
	copy(out[UDPHdrLen:], d.Data)
	if checksumSrc == (IP4{}) && checksumDst == (IP4{}) {
		BePut16(out[6:8], 0) // loopback/tests: skip
		return out
	}
	var sum uint32
	buf := make([]byte, 0, len(out)+12)
	buf = append(buf, checksumSrc[:]...)
	buf = append(buf, checksumDst[:]...)
	buf = append(buf, 0, 17)
	var l [2]byte
	bePut16(l[:], uint16(len(out)))
	buf = append(buf, l[:]...)
	buf = append(buf, out...)
	if len(buf)%2 != 0 {
		buf = append(buf, 0)
	}
	for i := 0; i < len(buf); i += 2 {
		sum += uint32(buf[i])<<8 | uint32(buf[i+1])
	}
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	BePut16(out[6:8], ^uint16(sum))
	return out
}

func bePut16(p []byte, v uint16) { p[0] = byte(v >> 8); p[1] = byte(v) }

// ---- port demux ----

// UDPQueue is one bound port's receive queue.
type UDPQueue struct {
	mu     sync.Mutex
	frames [][]byte // payload bytes of received datagrams
	closed bool
}

func (q *UDPQueue) push(data []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	q.frames = append(q.frames, cp)
}

// Recv pops one datagram payload; ok=false when empty/closed-drained.
func (q *UDPQueue) Recv() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return nil, false
	}
	d := q.frames[0]
	q.frames = q.frames[1:]
	return d, true
}

// UDPDemux routes inbound datagrams to per-port queues.
type UDPDemux struct {
	s *Stack

	mu    sync.Mutex
	ports map[uint16]*UDPQueue
}

func NewUDPDemux(s *Stack) *UDPDemux {
	return &UDPDemux{s: s, ports: make(map[uint16]*UDPQueue)}
}

// Bind registers (or returns the existing) queue for port.
// Bind registers a receive queue for port. Port 0 allocates an
// ephemeral high port so remote peers have something to reply to.
// Returns the queue and the ACTUAL bound port.
func (d *UDPDemux) Bind(port uint16) (*UDPQueue, uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if port == 0 {
		for p := uint16(49152); p != 0; p++ {
			if _, taken := d.ports[p]; !taken {
				port = p
				break
			}
		}
	}
	q, ok := d.ports[port]
	if !ok {
		q = &UDPQueue{}
		d.ports[port] = q
	}
	return q, port
}

// Unbind removes a port binding.
func (d *UDPDemux) Unbind(port uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.ports, port)
}

func (d *UDPDemux) handle(pkt *IP4Packet) {
	dg, err := ParseUDP(pkt.Payload)
	if err != nil {
		return
	}
	d.mu.Lock()
	q, ok := d.ports[dg.DstPort]
	d.mu.Unlock()
	if ok {
		q.push(dg.Data)
	} // unbound port: dropped silently (no ICMP-unreachable in v1)
}

// SendTo transmits data from srcPort to dstIP:dstPort.
func (d *UDPDemux) SendTo(srcPort uint16, dstIP IP4, dstPort uint16, data []byte) error {
	dg := &UDPDatagram{SrcPort: srcPort, DstPort: dstPort, Data: data}
	return d.s.SendUDP(dstIP, dg)
}
