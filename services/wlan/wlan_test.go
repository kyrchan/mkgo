package main

import (
	"bytes"
	"sync"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// --- Beacon frame builder ---

func buildBeacon(bssid MAC, ssid string, ch uint8) []byte {
	// 802.11 beacon: FC(2) + Dur(2) + DA(6) + SA(6) + BSSID(6) + SeqCtrl(2) +
	// BeaconInterval(2) + Capabilities(2) + IEs
	body := make([]byte, 4)      // BeaconInterval + Capabilities
	lib.Put16(body[0:2], 100)    // beacon interval
	lib.Put16(body[2:4], 0x0001) // capabilities: ESS
	// SSID IE
	body = append(body, EIE_SSID, byte(len(ssid)))
	body = append(body, ssid...)
	// DS IE
	body = append(body, EIE_DS, 1, ch)

	hdr := make([]byte, 24)
	lib.Put16(hdr[0:2], 0x0080)      // FC: type=mgmt, subtype=beacon
	copy(hdr[4:10], BroadcastMAC[:]) // DA
	copy(hdr[10:16], bssid[:])       // SA = BSSID
	copy(hdr[16:22], bssid[:])       // BSSID

	out := make([]byte, 1, 1+len(hdr)+len(body))
	out[0] = FrameTypeMgmt
	out = append(out, hdr...)
	out = append(out, body...)
	return out
}

// --- IEEE parsing tests ---

func TestParseMAC(t *testing.T) {
	m, err := parseMAC("02:00:00:00:00:09")
	if err != nil {
		t.Fatal(err)
	}
	want := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	if m != want {
		t.Fatalf("got %v want %v", m, want)
	}
	if m.String() != "02:00:00:00:00:09" {
		t.Fatalf("String() = %q", m.String())
	}
}

func TestParseIP(t *testing.T) {
	ip, err := parseIP("10.0.2.15")
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "10.0.2.15" {
		t.Fatalf("got %s", ip.String())
	}
}

func TestParseBeacon(t *testing.T) {
	bssid := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	raw := buildBeacon(bssid, "testnet", 6)
	m, err := parseMgmt(raw[1:])
	if err != nil {
		t.Fatal(err)
	}
	if m.Subtype() != MgmtBeacon {
		t.Fatalf("subtype=%d want %d", m.Subtype(), MgmtBeacon)
	}
	if m.Addr2 != bssid {
		t.Fatalf("BSSID=%.2x want %.2x", m.Addr2, bssid)
	}
	ssid, ch, ok := parseBeacon(m.Body)
	if !ok {
		t.Fatal("parseBeacon failed")
	}
	if ssid != "testnet" {
		t.Fatalf("ssid=%q", ssid)
	}
	if ch != 6 {
		t.Fatalf("chan=%d", ch)
	}
}

func TestParseAssocResp(t *testing.T) {
	body := make([]byte, 4)
	bePut16(body[0:2], AssocSuccess)
	bePut16(body[2:4], 0x0001)
	st, aid, ok := parseAssocResp(body)
	if !ok {
		t.Fatal("parseAssocResp failed")
	}
	if st != AssocSuccess {
		t.Fatalf("status=%d", st)
	}
	if aid != 1 {
		t.Fatalf("aid=%d", aid)
	}
}

func TestEthRoundTrip(t *testing.T) {
	dst := MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	src := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	eth := buildEth(dst, src, EthTypeIPv4, []byte("hello"))
	f, err := parseEth(eth)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != EthTypeIPv4 {
		t.Fatalf("type=%#x", f.Type)
	}
	if f.Dst != dst || f.Src != src {
		t.Fatal("mac mismatch")
	}
	if string(f.Payload) != "hello" {
		t.Fatalf("payload=%q", f.Payload)
	}
}

func TestParseIPv4RoundTrip(t *testing.T) {
	dg, err := buildIPv4(IP4{10, 0, 2, 15}, IP4{10, 0, 2, 1}, IP4ProtoUDP, []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := parseIPv4(dg)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.Src != (IP4{10, 0, 2, 15}) || pkt.Dst != (IP4{10, 0, 2, 1}) {
		t.Fatalf("ips: %v %v", pkt.Src, pkt.Dst)
	}
	if pkt.Proto != IP4ProtoUDP {
		t.Fatalf("proto=%d", pkt.Proto)
	}
}

func TestParseUDP(t *testing.T) {
	dg := buildUDP(1234, 5678, []byte("test"))
	udp, err := parseUDP(dg)
	if err != nil {
		t.Fatal(err)
	}
	if udp.SrcPort != 1234 || udp.DstPort != 5678 {
		t.Fatalf("ports: %d %d", udp.SrcPort, udp.DstPort)
	}
	if string(udp.Data) != "test" {
		t.Fatalf("data=%q", udp.Data)
	}
}

func TestParseUDPEth(t *testing.T) {
	srcMAC := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	dstMAC := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	udp := buildUDP(1234, 5678, []byte("hello-uat"))
	ip, _ := buildIPv4(IP4{10, 0, 2, 15}, IP4{10, 0, 2, 1}, IP4ProtoUDP, udp)
	eth := buildEth(dstMAC, srcMAC, EthTypeIPv4, ip)

	f, ipk, udp2, err := parseUDPEth(eth)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != EthTypeIPv4 {
		t.Fatal("not ipv4")
	}
	if ipk.Dst != (IP4{10, 0, 2, 1}) {
		t.Fatalf("ip dst=%v", ipk.Dst)
	}
	if udp2.DstPort != 5678 {
		t.Fatalf("udp dst=%d", udp2.DstPort)
	}
	if string(udp2.Data) != "hello-uat" {
		t.Fatalf("data=%q", udp2.Data)
	}
}

// --- DHCP flow test ---

func TestDhcpDORA(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	offer := &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    dhcp.xid,
		YIAddr: IP4{10, 0, 2, 15},
		Opts: map[uint8][]byte{
			DHCPOptionDHCPType: {DHCPMsgOffer},
		},
		MType: DHCPMsgOffer,
	}
	offerBytes := buildDHCPFrame(offer, mac)

	ack := &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    dhcp.xid,
		YIAddr: IP4{10, 0, 2, 15},
		Opts: map[uint8][]byte{
			DHCPOptionDHCPType: {DHCPMsgACK},
			DHCPOptionSubnet:   {255, 255, 255, 0},
			DHCPOptionRouter:   {10, 0, 2, 1},
		},
		MType: DHCPMsgACK,
	}
	ackBytes := buildDHCPFrame(ack, mac)

	off.Inject(offerBytes)
	off.Inject(ackBytes)

	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("dhcp Run: %v", err)
	}
	if lease.IP != (IP4{10, 0, 2, 15}) {
		t.Fatalf("ip=%v", lease.IP)
	}
	if lease.GW != (IP4{10, 0, 2, 1}) {
		t.Fatalf("gw=%v", lease.GW)
	}
}

// buildDHCPFrame wraps a DHCP packet in Ethernet/IP/UDP broadcast.
func buildDHCPFrame(d *DhcpPacket, mac MAC) []byte {
	dg := buildUDP(DHCPClientPort, DHCPServerPort, d.build())
	ip, _ := buildIPv4(IP4{}, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, mac, EthTypeIPv4, ip)
}

// buildDHCPFrameWithSrc wraps a DHCP packet from src IP (for renew/release
// that carry CIAddr).
func buildDHCPFrameWithSrc(d *DhcpPacket, mac MAC, src IP4) []byte {
	dg := buildUDP(DHCPClientPort, DHCPServerPort, d.build())
	ip, _ := buildIPv4(src, IP4{255, 255, 255, 255}, IP4ProtoUDP, dg)
	return buildEth(BroadcastMAC, mac, EthTypeIPv4, ip)
}

// --- DHCP renew + release tests ---

func buildAckWithLease(xid uint32, ip IP4, serverID IP4, leaseSecs uint32) *DhcpPacket {
	opts := map[uint8][]byte{
		DHCPOptionDHCPType:   {DHCPMsgACK},
		DHCPOptionSubnet:     {255, 255, 255, 0},
		DHCPOptionRouter:     {10, 0, 2, 1},
		DHCPOptionDHCPServer: serverID[:],
	}
	if leaseSecs > 0 {
		opts[DHCPOptionLeaseTime] = []byte{
			byte(leaseSecs >> 24), byte(leaseSecs >> 16),
			byte(leaseSecs >> 8), byte(leaseSecs),
		}
	}
	return &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    xid,
		YIAddr: ip,
		Opts:   opts,
		MType:  DHCPMsgACK,
	}
}

func buildOfferWithLease(xid uint32, ip IP4, serverID IP4, leaseSecs uint32) *DhcpPacket {
	opts := map[uint8][]byte{
		DHCPOptionDHCPType:   {DHCPMsgOffer},
		DHCPOptionSubnet:     {255, 255, 255, 0},
		DHCPOptionRouter:     {10, 0, 2, 1},
		DHCPOptionDHCPServer: serverID[:],
	}
	if leaseSecs > 0 {
		opts[DHCPOptionLeaseTime] = []byte{
			byte(leaseSecs >> 24), byte(leaseSecs >> 16),
			byte(leaseSecs >> 8), byte(leaseSecs),
		}
	}
	return &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    xid,
		YIAddr: ip,
		Opts:   opts,
		MType:  DHCPMsgOffer,
	}
}

func TestDhcpRenewSuccess(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	serverID := IP4{10, 0, 2, 1}
	leaseIP := IP4{10, 0, 2, 15}

	offer := &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    dhcp.xid,
		YIAddr: leaseIP,
		Opts: map[uint8][]byte{
			DHCPOptionDHCPType:   {DHCPMsgOffer},
			DHCPOptionDHCPServer: serverID[:],
		},
		MType: DHCPMsgOffer,
	}
	ack := buildAckWithLease(dhcp.xid, leaseIP, serverID, 30)

	off.Inject(buildDHCPFrame(offer, mac))
	off.Inject(buildDHCPFrame(ack, mac))

	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.ServerID != serverID {
		t.Fatalf("server ID=%v want %v", lease.ServerID, serverID)
	}
	if lease.LeaseTime != 30*time.Second {
		t.Fatalf("lease=%v want 30s", lease.LeaseTime)
	}
	if lease.T1 != 15*time.Second {
		t.Fatalf("T1=%v want 15s", lease.T1)
	}

	// Renewal: inject a fresh ACK.
	renewAck := buildAckWithLease(dhcp.xid, leaseIP, serverID, 60)
	off.Inject(buildDHCPFrame(renewAck, mac))

	renewed, err := dhcp.Renew(lease)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.IP != leaseIP {
		t.Fatalf("renewed IP=%v", renewed.IP)
	}
	if renewed.LeaseTime != 60*time.Second {
		t.Fatalf("renewed lease=%v want 60s", renewed.LeaseTime)
	}

	out := off.Outgoing()
	// Find the DHCPREQUEST among outgoing frames.
	found := false
	for _, f := range out {
		_, _, udp, err := parseUDPEth(f)
		if err != nil {
			continue
		}
		d, err := parseDhcp(udp.Data)
		if err != nil {
			continue
		}
		if d.MType == DHCPMsgRequest {
			found = true
		}
	}
	if !found {
		t.Fatal("no DHCPREQUEST sent during renewal")
	}
}

func TestDhcpRelease(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	serverID := IP4{10, 0, 2, 1}
	leaseIP := IP4{10, 0, 2, 15}

	offer := &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    dhcp.xid,
		YIAddr: leaseIP,
		Opts: map[uint8][]byte{
			DHCPOptionDHCPType:   {DHCPMsgOffer},
			DHCPOptionDHCPServer: serverID[:],
		},
		MType: DHCPMsgOffer,
	}
	ack := buildAckWithLease(dhcp.xid, leaseIP, serverID, 30)

	off.Inject(buildDHCPFrame(offer, mac))
	off.Inject(buildDHCPFrame(ack, mac))

	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := dhcp.Release(lease); err != nil {
		t.Fatalf("Release: %v", err)
	}

	out := off.Outgoing()
	found := false
	for _, f := range out {
		_, _, udp, err := parseUDPEth(f)
		if err != nil {
			continue
		}
		d, err := parseDhcp(udp.Data)
		if err != nil {
			continue
		}
		if d.MType == DHCPMsgRelease {
			found = true
			if d.CIAddr != leaseIP {
				t.Fatalf("release CIAddr=%v want %v", d.CIAddr, leaseIP)
			}
		}
	}
	if !found {
		t.Fatal("no DHCPRELEASE sent")
	}
}

func TestDhcpRenewBackoff(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	serverID := IP4{10, 0, 2, 1}
	leaseIP := IP4{10, 0, 2, 15}

	offer := &DhcpPacket{
		Op:     DHCPOpBootReply,
		XID:    dhcp.xid,
		YIAddr: leaseIP,
		Opts: map[uint8][]byte{
			DHCPOptionDHCPType:   {DHCPMsgOffer},
			DHCPOptionDHCPServer: serverID[:],
		},
		MType: DHCPMsgOffer,
	}
	ack := buildAckWithLease(dhcp.xid, leaseIP, serverID, 30)

	off.Inject(buildDHCPFrame(offer, mac))
	off.Inject(buildDHCPFrame(ack, mac))

	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// No renewal ACK injected — client should retry with backoff and time out.
	start := time.Now()
	_, err = dhcp.Renew(lease)
	elapsed := time.Since(start)

	if err != ErrDHCPTimeout {
		t.Fatalf("Renew error=%v, want ErrDHCPTimeout", err)
	}

	out := off.Outgoing()
	requestCount := 0
	for _, f := range out {
		_, _, udp, e := parseUDPEth(f)
		if e != nil {
			continue
		}
		d, e := parseDhcp(udp.Data)
		if e != nil {
			continue
		}
		if d.MType == DHCPMsgRequest {
			requestCount++
		}
	}
	if requestCount < maxRenewAttempts {
		t.Fatalf("sent %d DHCPREQUEST frames, want >= %d", requestCount, maxRenewAttempts)
	}

	// Backoff should be at least 3s (1s + 2s sleeps between 3 attempts).
	if elapsed < 3*time.Second {
		t.Fatalf("elapsed=%v, backoff too short (want >= 3s)", elapsed)
	}
}

// TestLeaseRenewalEndToEnd drives the full DHCP client lifecycle against an
// in-process DHCP server: lease acquisition (DORA), renewal at T1 with a
// fresh ACK, and release on shutdown. It is the host-side proof that the
// gaps closed for M2 (no RELEASE / no backoff / renew never fired) are
// exercised as a single coherent flow rather than in isolation.
func TestLeaseRenewalEndToEnd(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	serverID := IP4{10, 0, 2, 1}
	leaseIP := IP4{10, 0, 2, 15}

	releaseCh := make(chan struct{}, 1)
	done := make(chan struct{})

	// In-process DHCP server: respond to each client message type.
	go func() {
		offerSent := false
		for {
			select {
			case <-done:
				return
			default:
			}
			frames := off.Outgoing()
			for _, f := range frames {
				_, _, udp, err := parseUDPEth(f)
				if err != nil {
					continue
				}
				d, err := parseDhcp(udp.Data)
				if err != nil {
					continue
				}
				switch d.MType {
				case DHCPMsgDiscover:
					if !offerSent {
						off.Inject(buildDHCPFrame(buildOfferWithLease(dhcp.xid, leaseIP, serverID, 30), mac))
						offerSent = true
					}
				case DHCPMsgRequest:
					// DORA request carries CIAddr=0; a renewal carries the
					// leased address. Use that to pick the ACK lease time so
					// the test can observe a fresh lease on renewal.
					leaseSecs := uint32(60)
					if d.CIAddr == (IP4{}) {
						leaseSecs = 30
					}
					off.Inject(buildDHCPFrame(buildAckWithLease(dhcp.xid, leaseIP, serverID, leaseSecs), mac))
				case DHCPMsgRelease:
					select {
					case releaseCh <- struct{}{}:
					default:
					}
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// 1. Lease acquisition (DORA): discover -> offer -> request -> ack.
	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lease.IP != leaseIP {
		t.Fatalf("lease IP=%v want %v", lease.IP, leaseIP)
	}
	if lease.GW != serverID {
		t.Fatalf("GW=%v want %v", lease.GW, serverID)
	}
	if lease.ServerID != serverID {
		t.Fatalf("ServerID=%v want %v", lease.ServerID, serverID)
	}
	if lease.LeaseTime != 30*time.Second {
		t.Fatalf("LeaseTime=%v want 30s", lease.LeaseTime)
	}
	if lease.T1 != 15*time.Second {
		t.Fatalf("T1=%v want 15s", lease.T1)
	}

	// 2. Renewal at T1: the server answers with a fresh 60s lease.
	renewed, err := dhcp.Renew(lease)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.IP != leaseIP {
		t.Fatalf("renewed IP=%v want %v", renewed.IP, leaseIP)
	}
	if renewed.LeaseTime != 60*time.Second {
		t.Fatalf("renewed LeaseTime=%v want 60s", renewed.LeaseTime)
	}
	if renewed.T1 != 30*time.Second {
		t.Fatalf("renewed T1=%v want 30s", renewed.T1)
	}

	// 3. Release on shutdown: the server must observe the DHCPRELEASE.
	if err := dhcp.Release(lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
	select {
	case <-releaseCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server never observed DHCPRELEASE")
	}
	close(done)
}

// TestDhcpRenewalGate proves the full DHCP lifecycle end-to-end with a
// short lease: the in-process server grants a 200 ms lease (T1 = 100 ms),
// the Run() main loop lets the timer fire without an explicit Renew() call,
// and the test asserts the renewal marker appears on the writer before the
// minute deadline expires. Release is asserted via the server-side channel.
// This is the host-side gate that was missing for M2: no real hardware
// required (MockTransport + FakeKernel with a minimal net dispatch loop).
func TestDhcpRenewalGate(t *testing.T) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	off := NewMockTransport()
	dhcp := newDhcpClient(off, mac)

	serverID := IP4{10, 0, 2, 1}
	leaseIP := IP4{10, 0, 2, 15}

	w := &renewalGateWriter{}
	done := make(chan struct{})
	releaseCh := make(chan struct{}, 1)

	// FakeKernel satisfies lib.Kernel. Create the "net" port and run a
	// minimal net-dispatch goroutine that routes replies to the client's
	// inbox port (mirroring ServeNet's dispatch + ReplyBook pattern).
	fk := lib.NewFakeKernel()
	fk.PortCreate(lib.NameNet)
	netH := fk.PortBind(lib.NameNet)
	netIter := 0
	go func() {
		buf := make([]byte, lib.MaxMsg)
		for {
			select {
			case <-done:
				return
			default:
			}
			n := fk.PortRecv(netH, buf)
			if n < lib.CanonicalHeaderLen+2 {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			netIter++
			if netIter <= 3 {
				t.Logf("net dispatch: iter=%d n=%d op=%d rnam=%q", netIter, n, lib.Get16(buf[0:2]), string(buf[8:24]))
			}
			hdr, _ := lib.ParseHeader(buf[:n])
			payload := buf[lib.CanonicalHeaderLen:n]
			var rep []byte
			switch hdr.Op {
			case 1: // NetOpOpen
				sid := uint16(1)
				if fk.NetSockets == nil {
					fk.NetSockets = map[uint16]struct{}{}
				}
				fk.NetSockets[sid] = struct{}{}
				rep = make([]byte, 30)
				lib.Put16(rep, hdr.Op)
				lib.Put16(rep[2:], hdr.Seq)
				lib.Put32(rep[24:], 0)
				lib.Put16(rep[28:], sid)
			case 5: // NetOpClose
				if len(payload) >= 2 {
					delete(fk.NetSockets, lib.Get16(payload[0:2]))
				}
				rep = make([]byte, 28)
				lib.Put16(rep, hdr.Op)
				lib.Put16(rep[2:], hdr.Seq)
				lib.Put32(rep[24:], 0)
			case 2, 3, 4, 6, 7: // NetOpConn/Send/Recv/Ping/Status
				rep = make([]byte, 28)
				lib.Put16(rep, hdr.Op)
				lib.Put16(rep[2:], hdr.Seq)
				lib.Put32(rep[24:], 0)
			default:
				rep = make([]byte, 28)
				lib.Put16(rep, hdr.Op)
				lib.Put16(rep[2:], hdr.Seq)
				lib.Put32(rep[24:], 0xFFFFFFFF)
			}
			if rep != nil && hdr.RNam != "" {
				if rh := fk.PortBind(hdr.RNam); rh != lib.InvalidHandle {
					fk.PortSend(rh, rep)
					if netIter <= 3 {
						t.Logf("net dispatch: sent reply to rnam=%q handle=%d", hdr.RNam, rh)
					}
				} else {
					if netIter <= 3 {
						t.Logf("net dispatch: PortBind rnam=%q FAILED", hdr.RNam)
					}
				}
			}
		}
	}()

	// In-process DHCP server: respond to each client message type.
	go func() {
		// Send one beacon so Scan() finds the BSSID and Associate() succeeds.
		off.Inject(buildBeacon(MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, "testnet", 6))
		iter := 0
		offerSent := false
		assocRespInjected := false
		for {
			iter++
			select {
			case <-done:
				return
			default:
			}
			frames := off.Outgoing()
			for _, f := range frames {
				// Association requests are management frames (not UDP).
				if len(f) >= 1 && f[0] == FrameTypeMgmt {
					m, err := parseMgmt(f[1:])
					if err == nil && m.Subtype() == 0 && !assocRespInjected {
						t.Logf("server goroutine: iter=%d: injecting assoc resp for subtype=0", iter)
						off.Inject(buildAssocResp(MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, AssocSuccess, 1))
						assocRespInjected = true
					}
					continue
				}
				_, _, udp, err := parseUDPEth(f)
				if err != nil {
					continue
				}
				d, err := parseDhcp(udp.Data)
				if err != nil {
					continue
				}
				switch d.MType {
				case DHCPMsgDiscover:
					if !offerSent {
						t.Logf("server goroutine: iter=%d: sending OFFER", iter)
						off.Inject(buildDHCPFrame(
							buildOfferWithLease(dhcp.xid, leaseIP, serverID, 2), mac))
						offerSent = true
					}
				case DHCPMsgRequest:
					leaseSecs := uint32(4)
					if d.CIAddr == (IP4{}) {
						leaseSecs = 2
					}
					t.Logf("server goroutine: iter=%d: sending ACK lease=%d", iter, leaseSecs)
					off.Inject(buildDHCPFrame(
						buildAckWithLease(dhcp.xid, leaseIP, serverID, leaseSecs), mac))
				case DHCPMsgRelease:
					select {
					case releaseCh <- struct{}{}:
					default:
					}
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Run with a 1-minute deadline; the 2-second lease keeps wall-clock time low.
	exit := Run(fk, off, w, "testnet")
	allOut := w.Bytes()

	// 1. DORA must have succeeded (exit 0, lease acquired).
	if exit != 0 {
		t.Fatalf("Run exit=%d, want 0; output:\n%s", exit, string(allOut))
	}

	// 2. Renewal marker must be present (proves T1 fired without an explicit
	//    Renew() call from the test driver).
	if !bytes.Contains(allOut, []byte("[wlan] renewed:")) {
		t.Fatalf("renewal marker not found in output:\n%s", string(allOut))
	}

	// 3. Release must have been observed by the mock server.
	select {
	case <-releaseCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server never observed DHCPRELEASE")
	}
	close(done)
}

// renewalGateWriter is a thread-safe io.Writer that accumulates output for
// the TestDhcpRenewalGate assertion.
type renewalGateWriter struct {
	mu  sync.Mutex
	out []byte
}

func (w *renewalGateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out = append(w.out, p...)
	return len(p), nil
}

func (w *renewalGateWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, len(w.out))
	copy(cp, w.out)
	return cp
}

func TestWifiDriverScan(t *testing.T) {
	off := NewMockTransport()
	bssid := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	off.Inject(buildBeacon(bssid, "netA", 6))
	off.Inject(buildBeacon(bssid, "netA", 6))
	off.Inject(buildBeacon(MAC{0x02, 0, 0, 0, 0, 2}, "netB", 11))

	bus := lib.NewFakeKernel()
	d := newWifiDriver(off, bus, &discardWriter{}, bssid)
	bss, err := d.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(bss) != 2 {
		t.Fatalf("found %d bss, want 2", len(bss))
	}
	names := map[string]bool{}
	for _, b := range bss {
		names[b.SSID] = true
	}
	if !names["netA"] || !names["netB"] {
		t.Fatalf("ssids: %v", names)
	}
}

// --- wifiDriver associate test ---

func TestWifiDriverAssociate(t *testing.T) {
	off := NewMockTransport()
	bssid := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	off.Inject(buildBeacon(bssid, "mywifi", 6))

	bus := lib.NewFakeKernel()
	d := newWifiDriver(off, bus, &discardWriter{}, bssid)
	if _, err := d.Scan(); err != nil {
		t.Fatal(err)
	}
	off.Inject(buildAssocResp(bssid, AssocSuccess, 1))

	if err := d.Associate("mywifi"); err != nil {
		t.Fatalf("assoc: %v", err)
	}
	out := off.Outgoing()
	if len(out) == 0 {
		t.Fatal("no assoc request sent")
	}
}

func buildAssocResp(bssid MAC, status, aid uint16) []byte {
	body := make([]byte, 4)
	bePut16(body[0:2], status)
	bePut16(body[2:4], aid)

	hdr := make([]byte, 24)
	lib.Put16(hdr[0:2], 0x0010) // FC: type=mgmt, subtype=assoc-resp(1)
	copy(hdr[4:10], bssid[:])   // DA
	copy(hdr[10:16], bssid[:])  // SA
	copy(hdr[16:22], bssid[:])  // BSSID

	out := make([]byte, 1, 1+len(hdr)+len(body))
	out[0] = FrameTypeMgmt
	out = append(out, hdr...)
	out = append(out, body...)
	return out
}

// --- NetBridge test: offload → net ---

func TestNetBridgeForwardOffloadToNet(t *testing.T) {
	fk := lib.NewFakeKernel()
	srv := newMockNetServer(fk)
	off := NewMockTransport()

	apMAC := MAC{0x02, 0, 0, 0, 0, 1}
	ourMAC := MAC{0x02, 0, 0, 0, 0, 9}
	ourIP := IP4{10, 0, 2, 15}
	gwIP := IP4{10, 0, 2, 1}

	nc, err := lib.BindNet(fk, "bridge")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(500)

	nb, err := newNetBridge(nc, off, ourMAC, apMAC, ourIP, gwIP)
	if err != nil {
		t.Fatal(err)
	}
	defer nb.Close()

	// Inject a UDP data frame from the AP → should arrive at net socket.
	udp := buildUDP(1234, 5678, []byte("ping"))
	ip, _ := buildIPv4(gwIP, ourIP, IP4ProtoUDP, udp)
	eth := buildEth(ourMAC, apMAC, EthTypeIPv4, ip)
	off.Inject(append([]byte{FrameTypeData}, eth...))

	if !nb.recvFromOffload() {
		t.Fatal("recvFromOffload returned false")
	}

	// Verify the mock net server received the Send.
	waitForCond(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.received) > 0 && srv.received[0] == "ping"
	}, "net server did not receive ping")
}

// --- NetBridge test: net → offload ---

func TestNetBridgeForwardNetToOffload(t *testing.T) {
	fk := lib.NewFakeKernel()
	srv := newMockNetServer(fk)
	off := NewMockTransport()

	apMAC := MAC{0x02, 0, 0, 0, 0, 1}
	ourMAC := MAC{0x02, 0, 0, 0, 0, 9}
	ourIP := IP4{10, 0, 2, 15}
	gwIP := IP4{10, 0, 2, 1}

	nc, err := lib.BindNet(fk, "flood")
	if err != nil {
		t.Fatal(err)
	}
	nc.SetBudget(500)

	nb, err := newNetBridge(nc, off, ourMAC, apMAC, ourIP, gwIP)
	if err != nil {
		t.Fatal(err)
	}
	defer nb.Close()

	// Queue data in the mock net server for the socket.
	srv.mu.Lock()
	srv.nextSock = nb.sock
	srv.queued = append(srv.queued, []byte("outgoing"))
	srv.mu.Unlock()

	if !nb.recvFromNet() {
		t.Fatal("recvFromNet returned false")
	}

	out := off.Outgoing()
	if len(out) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(out))
	}
	// Verify it's a data frame with UDP payload.
	f := out[0]
	if f[0] != FrameTypeData {
		t.Fatal("not a data frame")
	}
	_, _, udp, err := parseUDPEth(f[1:])
	if err != nil {
		t.Fatal(err)
	}
	if string(udp.Data) != "outgoing" {
		t.Fatalf("data=%q", udp.Data)
	}
}

// --- mockNetServer ---

type mockNetServer struct {
	k        lib.Kernel
	h        lib.Handle
	mu       sync.Mutex
	conns    map[uint16][]byte
	nextID   uint16
	nextSock uint16
	queued   [][]byte
	received []string
}

func newMockNetServer(k lib.Kernel) *mockNetServer {
	h := k.PortCreate(lib.NameNet)
	if h == lib.InvalidHandle {
		h = k.PortBind(lib.NameNet)
	}
	if h == lib.InvalidHandle {
		panic("mockNetServer: create net failed")
	}
	s := &mockNetServer{
		k:     k,
		h:     h,
		conns: make(map[uint16][]byte),
	}
	go s.serve()
	return s
}

func (s *mockNetServer) serve() {
	buf := make([]byte, lib.MaxMsg)
	for {
		n := s.k.PortRecv(s.h, buf)
		if n <= 0 {
			time.Sleep(time.Millisecond)
			continue
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		s.handle(req)
	}
}

func (s *mockNetServer) handle(req []byte) {
	if len(req) < lib.CanonicalHeaderLen+2 {
		return
	}
	op := lib.Get16(req[0:2])
	seq := lib.Get16(req[2:4])
	rname := string(bytes.TrimRight(req[8:24], "\x00"))
	payload := req[lib.CanonicalHeaderLen:]

	var body []byte
	status := int32(0)
	switch op {
	case lib.NetOpOpen:
		s.mu.Lock()
		id := s.nextID
		s.nextID++
		s.conns[id] = nil
		s.mu.Unlock()
		body = make([]byte, 2)
		lib.Put16(body[0:2], id)
	case lib.NetOpConn:
		status = 0
	case lib.NetOpSend:
		if len(payload) >= 4 {
			n := int(lib.Get16(payload[2:4]))
			if len(payload) >= 4+n {
				s.mu.Lock()
				s.received = append(s.received, string(payload[4:4+n]))
				s.mu.Unlock()
			}
		}
	case lib.NetOpRecv:
		s.mu.Lock()
		sock := lib.Get16(payload[0:2])
		queued := s.conns[sock]
		if len(queued) == 0 && sock == s.nextSock {
			queued = s.queued[0]
			s.queued = s.queued[1:]
		}
		s.mu.Unlock()
		body = make([]byte, 2+len(queued))
		lib.Put16(body, uint16(len(queued)))
		copy(body[2:], queued)
	default:
		return
	}

	rep := make([]byte, lib.CanonicalHeaderLen+4+len(body))
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)
	copy(rep[8:24], padName16(rname))
	lib.Put32(rep[24:], uint32(status))
	copy(rep[28:], body)
	rh := s.k.PortBind(rname)
	if rh != lib.InvalidHandle {
		s.k.PortSend(rh, rep)
	}
}

func padName16(s string) []byte {
	b := make([]byte, 16)
	copy(b, s)
	return b
}

// --- discardWriter ---

type discardWriter struct{}

func (w *discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// --- helpers ---

func waitForCond(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

var _ = bytes.HasPrefix
