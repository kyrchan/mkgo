package main

import (
	"bytes"
	"sync"
	"time"
)

// Stack is one host's network stack attached to a PacketFeed. A single
// goroutine drives pump() (the "interrupt"); protocol handlers run
// inline, keeping every layer race-free by construction. Deliveries to
// registered transports (UDP ports, TCP sockets) go through small
// mutex-guarded queues so test drivers can consume from other
// goroutines.

type Stack struct {
	MAC MAC
	IP  IP4

	src  PacketSource
	sink PacketSink
	arp  *ARPCache

	// hmu serializes pop+handle so multiple pumpers (service loops)
	// cannot reorder inbound segments relative to each other.
	hmu sync.Mutex

	mu     sync.Mutex
	icmpIn []ICMPPacket // completed echo replies for tests/clients
	udp    *UDPDemux
	tcp    *TCPStack

	// Stats for assertions.
	RxEth, RxARP, RxIPv4, RxICMP uint64
}

func NewStack(mac MAC, ip IP4, feed PacketFeed) *Stack {
	s := &Stack{
		MAC:  mac,
		IP:   ip,
		src:  feed,
		sink: feed,
		arp:  NewARPCache(),
	}
	s.udp = NewUDPDemux(s)
	s.tcp = NewTCPStack(s)
	return s
}

// pump services one inbound frame; returns whether a frame was present.
// Call it in a loop (or from the future §6 window poller).
func (s *Stack) pump() bool {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	raw, ok := s.src.Recv()
	if !ok {
		return false
	}
	s.handleFrame(raw)
	return true
}

// Drain pumps until the wire is empty (test convenience).
func (s *Stack) Drain() int {
	n := 0
	for s.pump() {
		n++
	}
	return n
}

func (s *Stack) handleFrame(raw []byte) {
	f, err := ParseEth(raw)
	if err != nil {
		return
	}
	if f.Dst != s.MAC && !f.Dst.IsBroadcast() {
		return // not for us (no promiscuous mode)
	}
	s.mu.Lock()
	s.RxEth++
	s.mu.Unlock()

	switch f.Type {
	case EthTypeARP:
		s.handleARP(f)
	case EthTypeIPv4:
		s.handleIPv4(f)
	}
}

// handleARP implements RFC 826: merge sender info; reply to requests
// targeting us.
func (s *Stack) handleARP(f *EthFrame) {
	pkt, err := ParseARP(f.Payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.RxARP++
	s.mu.Unlock()

	if pkt.SrcIP != (IP4{}) {
		s.arp.Update(pkt.SrcIP, pkt.SrcMAC)
	}
	switch pkt.Oper {
	case ARPOpRequest:
		if pkt.DstIP == s.IP {
			reply := &ARPPacket{
				Oper:   ARPOpReply,
				SrcMAC: s.MAC,
				SrcIP:  s.IP,
				DstMAC: pkt.SrcMAC,
				DstIP:  pkt.SrcIP,
			}
			s.sink.Send(BuildEth(pkt.SrcMAC, s.MAC, EthTypeARP, reply.Build()))
		}
	case ARPOpReply:
		s.arp.Update(pkt.DstIP, pkt.DstMAC) // resolver completion path
	}
}

func (s *Stack) handleIPv4(f *EthFrame) {
	var broadcast IP4 = [4]byte{255, 255, 255, 255}
	pkt, err := ParseIP4(f.Payload)
	if err != nil || (pkt.Dst != s.IP && pkt.Dst != broadcast) {
		return
	}
	s.mu.Lock()
	s.RxIPv4++
	s.mu.Unlock()

	switch pkt.Proto {
	case IP4ProtoICMP:
		s.handleICMP(pkt)
	case IP4ProtoUDP:
		s.udp.handle(pkt)
	case IP4ProtoTCP:
		s.tcp.handle(pkt)
	}
}

func (s *Stack) handleICMP(pkt *IP4Packet) {
	msg, err := ParseICMP(pkt.Payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.RxICMP++
	s.mu.Unlock()

	switch msg.Type {
	case ICMPEchoRequest:
		reply := BuildEchoReply(msg)
		s.sendIPv4(pkt.Src, IP4ProtoICMP, reply)
	case ICMPEchoReply:
		s.mu.Lock()
		s.icmpIn = append(s.icmpIn, *msg)
		s.mu.Unlock()
	}
}

// Ping sends an echo request and waits (bounded yields) for its reply.
func (s *Stack) Ping(dst IP4, id, seq uint16, payload []byte, budget int) (*ICMPPacket, error) {
	req := &ICMPPacket{Type: ICMPEchoRequest, ID: id, Seq: seq, Data: payload}
	if err := s.sendIPv4(dst, IP4ProtoICMP, req.Build()); err != nil {
		return nil, err
	}
	for i := 0; i < budget; i++ {
		time.Sleep(50 * time.Microsecond)
		s.mu.Lock()
		for j := range s.icmpIn {
			r := s.icmpIn[j]
			if r.Type == ICMPEchoReply && r.ID == id && r.Seq == seq &&
				bytes.Equal(r.Data, payload) {
				s.icmpIn = append(s.icmpIn[:j], s.icmpIn[j+1:]...)
				s.mu.Unlock()
				return &r, nil
			}
		}
		s.mu.Unlock()
		s.pump()
	}
	return nil, ErrNoReplyNet
}

// sendIPv4 resolves ARP (broadcast + wait) then frames the datagram.
func (s *Stack) sendIPv4(dst IP4, proto uint8, payload []byte) error {
	mac, err := s.Resolve(dst, 2000)
	if err != nil {
		return err
	}
	dg, err := (&IP4Packet{Src: s.IP, Dst: dst, Proto: proto, Payload: payload}).Build()
	if err != nil {
		return err
	}
	s.sink.Send(BuildEth(mac, s.MAC, EthTypeIPv4, dg))
	return nil
}

// injectFrom delivers a raw TCP segment as if it came from peer's IP
// (tests: forged RST etc.). Frames through the normal inbound path.
func (s *Stack) injectFrom(peer *Stack, seg *TCPSegment) {
	dg, err := (&IP4Packet{Src: peer.IP, Dst: s.IP, Proto: IP4ProtoTCP,
		Payload: seg.Build()}).Build()
	if err != nil {
		return
	}
	s.handleFrame(BuildEth(s.MAC, peer.MAC, EthTypeIPv4, dg))
}

// SendUDPDatagram is the UDP layer's outbound entry (used by demux).
func (s *Stack) SendUDPDatagram(dstIP IP4, dgram []byte) error {
	return s.sendIPv4(dstIP, IP4ProtoUDP, dgram)
}

// SendTCPSegment is the TCP layer's outbound entry.
func (s *Stack) SendTCPSegment(dstIP IP4, seg []byte) error {
	return s.sendIPv4(dstIP, IP4ProtoTCP, seg)
}

// Stats snapshots the receive counters (tests/diagnostics).
func (s *Stack) Stats() (eth, arp, ipv4, icmp uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.RxEth, s.RxARP, s.RxIPv4, s.RxICMP
}

var ErrNoReplyNet = &netError{"net: no reply"}

type netError struct{ msg string }

func (e *netError) Error() string { return e.msg }
