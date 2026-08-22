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
	SrcPort uint16
	DstPort uint16
	Data    []byte
}

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

func (d *UDPDatagram) Build() []byte {
	out := make([]byte, UDPHdrLen+len(d.Data))
	BePut16(out[0:2], d.SrcPort)
	BePut16(out[2:4], d.DstPort)
	BePut16(out[4:6], uint16(len(out)))
	copy(out[UDPHdrLen:], d.Data)
	BePut16(out[6:8], Checksum(out)) // pseudo-header omitted in v1 (loopback-fidelity note)
	return out
}

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
func (d *UDPDemux) Bind(port uint16) *UDPQueue {
	d.mu.Lock()
	defer d.mu.Unlock()
	q, ok := d.ports[port]
	if !ok {
		q = &UDPQueue{}
		d.ports[port] = q
	}
	return q
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
	return d.s.SendUDPDatagram(dstIP, dg.Build())
}
