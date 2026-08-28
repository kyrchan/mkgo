package main

import (
	"testing"
)

// FuzzParseEth exercises the Ethernet frame parser (AGENTS.md practice #4):
// arbitrary bytes must never panic; a successful parse must yield a frame
// whose payload slice stays within the input bounds.
func FuzzParseEth(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 13))
	f.Add(make([]byte, EthHdrLen))
	good := BuildEth(BroadcastMAC, MAC{1, 2, 3, 4, 5, 6}, EthTypeIPv4, []byte("payload"))
	f.Add(good)
	f.Add(good[:EthMinLen-3])

	f.Fuzz(func(t *testing.T, data []byte) {
		frm, err := ParseEth(data)
		if err != nil {
			return
		}
		if len(frm.Payload) > len(data)-EthHdrLen {
			t.Fatalf("payload overflow: payload=%d data=%d", len(frm.Payload), len(data))
		}
		// Build must roundtrip the header without panicking.
		_ = BuildEth(frm.Dst, frm.Src, frm.Type, frm.Payload)
	})
}

// FuzzParseARP exercises the ARP packet parser: arbitrary bytes must never
// panic; a successful parse must yield a 28-byte build.
func FuzzParseARP(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 27))
	f.Add(make([]byte, ARPLen))
	good := (&ARPPacket{Oper: ARPOpRequest, SrcMAC: MAC{1, 2, 3, 4, 5, 6},
		SrcIP: MustIP("10.0.0.1"), DstIP: MustIP("10.0.0.2")}).Build()
	f.Add(good)
	f.Add(append(good, 0, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := ParseARP(data)
		if err != nil {
			return
		}
		if len(a.Build()) != ARPLen {
			t.Fatalf("arp build len %d", len(a.Build()))
		}
	})
}

// FuzzParseIP4 exercises the IPv4 datagram parser: arbitrary bytes must
// never panic; a successful parse must yield a packet whose payload stays
// within input bounds and whose checksum validates.
func FuzzParseIP4(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 19))
	f.Add(make([]byte, IP4HdrLen))
	good, _ := (&IP4Packet{Src: MustIP("1.2.3.4"), Dst: MustIP("5.6.7.8"),
		Proto: IP4ProtoUDP, Payload: []byte("hi")}).Build()
	f.Add(good)
	f.Add(append(good, 0xff, 0xff))

	f.Fuzz(func(t *testing.T, data []byte) {
		pkt, err := ParseIP4(data)
		if err != nil {
			return
		}
		if len(pkt.Payload) > len(data)-IP4HdrLen {
			t.Fatalf("ip4 payload overflow: payload=%d data=%d", len(pkt.Payload), len(data))
		}
		// Rebuild must not panic and must yield a valid checksum.
		out, err := pkt.Build()
		if err != nil {
			return
		}
		if _, err := ParseIP4(out); err != nil {
			t.Fatalf("rebuild parse failed: %v", err)
		}
	})
}

// FuzzParseICMP exercises the ICMP echo parser: arbitrary bytes must never
// panic; checksum must validate on successful parse.
func FuzzParseICMP(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 7))
	f.Add(make([]byte, 8))
	good := (&ICMPPacket{Type: ICMPEchoRequest, ID: 1, Seq: 2, Data: []byte("ping")}).Build()
	f.Add(good)
	f.Add(append(good, 0, 0, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		msg, err := ParseICMP(data)
		if err != nil {
			return
		}
		out := msg.Build()
		if len(out) < 8 {
			t.Fatalf("icmp build too short: %d", len(out))
		}
		// Echo reply must not panic.
		_ = BuildEchoReply(msg)
	})
}

// FuzzParseUDP exercises the UDP datagram parser: arbitrary bytes must
// never panic; successful parse must respect the length field bounds.
func FuzzParseUDP(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 7))
	f.Add(make([]byte, UDPHdrLen))
	good := (&UDPDatagram{SrcPort: 1234, DstPort: 5678, Data: []byte("datagram")}).Build()
	f.Add(good)
	f.Add(append(good, 0xff))

	f.Fuzz(func(t *testing.T, data []byte) {
		dg, err := ParseUDP(data)
		if err != nil {
			return
		}
		if len(dg.Data) > len(data)-UDPHdrLen {
			t.Fatalf("udp data overflow: data=%d data_len=%d", len(dg.Data), len(data))
		}
		// Build must not panic.
		_ = dg.Build()
	})
}

// FuzzParseTCP exercises the TCP segment parser: arbitrary bytes must
// never panic; successful parse must respect the data-offset and payload
// bounds.
func FuzzParseTCP(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 19))
	f.Add(make([]byte, TCPHdrLen))
	syn := &TCPSegment{SrcPort: 1234, DstPort: 80, Seq: 1000, Flags: TCPSyn, Window: 65535}
	f.Add(syn.Build())
	dataSeg := &TCPSegment{SrcPort: 1234, DstPort: 80, Seq: 1001, Ack: 500,
		Flags: TCPAck | TCPPsh, Window: 65535, Payload: []byte("tcp payload")}
	f.Add(dataSeg.Build())
	f.Add(append(dataSeg.Build(), 0, 0, 0))

	f.Fuzz(func(t *testing.T, data []byte) {
		seg, err := ParseTCP(data)
		if err != nil {
			return
		}
		if len(seg.Payload) > len(data) {
			t.Fatalf("tcp payload overflow: payload=%d data=%d", len(seg.Payload), len(data))
		}
		// Build must not panic and must roundtrip the header.
		out := seg.Build()
		if len(out) < TCPHdrLen {
			t.Fatalf("tcp build too short: %d", len(out))
		}
		if _, err := ParseTCP(out); err != nil {
			t.Fatalf("tcp rebuild parse failed: %v", err)
		}
	})
}

// FuzzParseMAC exercises the MAC address parser: arbitrary bytes must
// never panic.
func FuzzParseMAC(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("02:00:00:00:00:01"))
	f.Add([]byte("nope"))
	f.Add([]byte("ff:ff:ff:ff:ff:ff"))
	f.Add([]byte("0g:00:00:00:00:01"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := ParseMAC(string(data))
		if err != nil {
			return
		}
		if m.String() == "" {
			t.Fatal("empty mac string")
		}
	})
}
