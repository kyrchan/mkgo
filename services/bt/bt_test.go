package main

import (
	"bytes"
	"testing"

	lib "kernel.lane/guests/lib"
)

// --- capture builders (lane-local: exact replay of a controller) ---

// cc builds a Command Complete event frame (H4 type + code 0x0E + len + params).
func cc(opcode uint16, status byte, ret []byte) []byte {
	params := make([]byte, 0, 4+len(ret))
	params = append(params, 0x01)               // num_hci_command_packets
	params = append(params, byte(opcode), byte(opcode>>8))
	params = append(params, status)
	params = append(params, ret...)
	return frameHCIEvent(EvtCmdComplete, params)
}

// cmdStatus builds a Command Status event frame for an async command.
func cmdStatus(opcode uint16, status byte) []byte {
	params := make([]byte, 0, 4)
	params = append(params, 0x01) // num_hci_command_packets
	params = append(params, byte(opcode), byte(opcode>>8))
	params = append(params, status)
	return frameHCIEvent(EvtCmdStatus, params)
}

// leMeta builds a LE Meta Event frame with the given subevent + body.
func leMeta(sub uint8, body []byte) []byte {
	params := append([]byte{sub}, body...)
	return frameHCIEvent(EvtLeMeta, params)
}

// advReportSub builds the LE Meta Advertising Report subevent *body*
// (everything after the subevent code byte).
func advReportSub(addrType byte, bss [6]byte, rssi int8, data []byte) []byte {
	b := make([]byte, 0, 12+len(data))
	b = append(b, 1)         // num_reports
	b = append(b, 0x03)      // event type = ADV_IND
	b = append(b, addrType)  // address type
	b = append(b, bss[:]...) // address
	b = append(b, byte(len(data)))
	b = append(b, data...)
	b = append(b, byte(int8(rssi)))
	return b
}

// advReportFrame builds a complete LE Meta Advertising Report event frame.
func advReportFrame(addrType byte, bss [6]byte, rssi int8, data []byte) []byte {
	return leMeta(LeMetaAdvReport, advReportSub(addrType, bss, rssi, data))
}

// connComplete builds a LE Meta Connection Complete subevent *body*.
func connComplete(handle uint16, role byte, peer [6]byte) []byte {
	b := make([]byte, 0, 16)
	b = append(b, 0x00) // status = OK
	var two [2]byte
	lib.Put16(two[:], handle)
	b = append(b, two[:]...) // connection handle
	b = append(b, role)      // role
	b = append(b, 0x01)      // peer address type
	b = append(b, peer[:]...)
	lib.Put16(two[:], 0x0018)
	b = append(b, two[:]...) // conn interval
	lib.Put16(two[:], 0x0000)
	b = append(b, two[:]...) // conn latency
	lib.Put16(two[:], 0x01F4)
	b = append(b, two[:]...) // supervision timeout
	b = append(b, 0x00)      // clock accuracy (pad)
	return b
}

// attFrame wraps an ATT PDU in an ACL data frame for a connection handle.
func attFrame(conn uint16, att []byte) []byte { return frameACL(conn, att) }

// buildCapture returns the full RX byte stream a controller would emit for
// the bt Run() flow, in exact read order.
func buildCapture() []byte {
	bd := [6]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	bss := [6]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	var out []byte
	out = append(out, cc(HciOpReset, 0, nil)...)
	out = append(out, cc(HciOpReadBdAddr, 0, bd[:])...)
	out = append(out, cc(HciLeSetScanParams, 0, nil)...)
	out = append(out, cc(HciLeSetScanEnable, 0, nil)...)
	out = append(out, advReportFrame(AdvAddrRandom, bss, int8(-56), []byte{0x02, 0x01})...)
	out = append(out, leMeta(LeMetaScanTimeout, nil)...)
	out = append(out, cmdStatus(HciLeCreateConn, 0)...)
	out = append(out, leMeta(LeMetaConnComplete, connComplete(0x0001, 0, bss))...)
	// ATT GATT responses (conn handle 0x0001):
	out = append(out, attFrame(0x0001, []byte{0x03, 0x17, 0x00})...)                         // Exchange MTU Rsp (srv=23)
	out = append(out, attFrame(0x0001, []byte{0x11, 0x06, 0x01, 0x00, 0x0F, 0x00, 0x0F, 0x18})...) // Read By Group Type Rsp (svc 0x180F, 1..0x000F)
	out = append(out, attFrame(0x0001, []byte{0x09, 0x07, 0x01, 0x00, 0x02, 0x03, 0x00, 0x19, 0x2A})...) // Read By Type Rsp (char value@0x0003, uuid 0x2A19)
	out = append(out, attFrame(0x0001, []byte{0x0B, 0x64})...)                              // Read Rsp (value 0x64=100%)
	return out
}

// TestBtGateReplayFeed drives Run() against a captured controller stream and
// asserts the Phase 12 gate milestones appear in order.
func TestBtGateReplayFeed(t *testing.T) {
	u := NewMockUART()
	u.Feed(buildCapture())
	var buf bytes.Buffer
	rc := Run(u, &buf)
	out := buf.String()
	if rc != 0 {
		t.Fatalf("Run rc=%d out=%q", rc, out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("bt: hci_le_scan ok")) {
		t.Errorf("missing scan milestone; out=%q", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("bt: gatt_read ok")) {
		t.Errorf("missing gatt_read milestone; out=%q", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("bt: bd_addr 66:55:44:33:22:11")) {
		t.Errorf("bd_addr not parsed; out=%q", out)
	}
}

// TestH4FrameRoundTrip checks the H4 builders/parsers are internally
// consistent: a command builder lays down the right bytes; an event and an
// ACL frame parse back to their decoded fields.
func TestH4FrameRoundTrip(t *testing.T) {
	cmd := frameHCICommand(HciOpReset, nil)
	if cmd[0] != H4Cmd || len(cmd) != 4 || cmd[1] != 0x03 || cmd[2] != 0x0C {
		t.Fatalf("bad command frame % x", cmd)
	}
	evt := frameHCIEvent(EvtCmdComplete, []byte{0x01, 0x03, 0x0C, 0x00})
	if evt[0] != H4Evt || evt[1] != EvtCmdComplete || evt[2] != 4 {
		t.Fatalf("bad event frame % x", evt)
	}
	u := NewMockUART()
	u.Feed(evt)
	e, err := readHCIEvent(u)
	if err != nil || e.Code != EvtCmdComplete || lib.Get16(e.Params[1:3]) != HciOpReset {
		t.Fatalf("readHCIEvent %+v %v", e, err)
	}

	acl := frameACL(0x0001, []byte{0x0A, 0x01, 0x00, 0x00, 0x00})
	u2 := NewMockUART()
	u2.Feed(acl)
	got, err := readACL(u2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Handle != 0x0001 || got.PB != 0x01 {
		t.Fatalf("acl handle/pb = %d/%d", got.Handle, got.PB)
	}
	if !bytes.Equal(got.Data, []byte{0x0A, 0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("acl data mismatch % x", got.Data)
	}

	// corrupt ACL length truncates without panic
	u3 := NewMockUART()
	u3.Feed([]byte{H4Acl, 0x01, 0x10, 0x10, 0x00})
	if _, err := readACL(u3); err != ErrTruncated {
		t.Fatalf("expected truncation, got %v", err)
	}
}

// TestParseAdvReports decodes a hand-built advertising report body.
func TestParseAdvReports(t *testing.T) {
	bss := [6]byte{1, 2, 3, 4, 5, 6}
	body := advReportSub(AdvAddrPublic, bss, int8(-56), []byte{0x02, 0x01})
	r, ok := parseAdvReports(body)
	if !ok || len(r) != 1 {
		t.Fatalf("parseAdvReports ok=%v len=%d", ok, len(r))
	}
	if r[0].Addr != bss {
		t.Errorf("addr mismatch % x", r[0].Addr)
	}
	if r[0].RSSI != -56 || r[0].AddrType != AdvAddrPublic {
		t.Fatalf("unexpected adv %+v", r[0])
	}
}

// TestParseConnComplete validates the LE Connection Complete decoder offsets.
func TestParseConnComplete(t *testing.T) {
	peer := [6]byte{9, 8, 7, 6, 5, 4}
	c, ok := parseConnComplete(connComplete(0x0042, 0, peer))
	if !ok {
		t.Fatal("parse failed")
	}
	if c.Handle != 0x0042 || c.PeerAddr != peer || c.Interval != 0x0018 {
		t.Fatalf("unexpected conn %+v", c)
	}
	// short body must not panic and report ok=false
	if _, ok := parseConnComplete([]byte{0}); ok {
		t.Fatal("short body accepted")
	}
}

// TestATTClientGateReplay exercises the ATT/GATT layer end-to-end over a
// pre-fed ACL capture: exchange MTU -> discover svc -> discover char ->
// read value.
func TestATTClientGateReplay(t *testing.T) {
	conn := uint16(0x0001)
	var cap []byte
	cap = append(cap, attFrame(conn, []byte{0x03, 0x17, 0x00})...)                             // Exchange MTU Rsp
	cap = append(cap, attFrame(conn, []byte{0x11, 0x06, 0x01, 0x00, 0x0F, 0x00, 0x0F, 0x18})...)  // Read By Group Type Rsp (svc 0x180F)
	cap = append(cap, attFrame(conn, []byte{0x09, 0x07, 0x01, 0x00, 0x02, 0x03, 0x00, 0x19, 0x2A})...) // Read By Type Rsp (char value@3)
	cap = append(cap, attFrame(conn, []byte{0x0B, 0x64})...)                                   // Read Rsp

	u := NewMockUART()
	u.Feed(cap)
	mc := NewController(u)
	ac := NewATTClient(mc, conn)
	if mtu, err := ac.ExchangeMTU(); err != nil || mtu != 23 {
		t.Fatalf("exchange mtu %d %v", mtu, err)
	}
	svcs, err := ac.DiscoverServices(GattSvcBattery)
	if err != nil || len(svcs) != 1 || svcs[0].UUID != GattSvcBattery {
		t.Fatalf("discover services %+v %v", svcs, err)
	}
	chars, err := ac.DiscoverCharacteristics(svcs[0].Start, svcs[0].End)
	if err != nil || len(chars) != 1 || chars[0].ValueHandle != 0x0003 {
		t.Fatalf("discover chars %+v %v", chars, err)
	}
	val, err := ac.ReadCharacteristic(chars[0].ValueHandle)
	if err != nil || len(val) != 1 || val[0] != 0x64 {
		t.Fatalf("read char % x %v", val, err)
	}
}

// TestRunNoCaptureFails ensures Run() fails gracefully (no panic, no
// spin) on an empty UART.
func TestRunNoCaptureFails(t *testing.T) {
	u := NewMockUART()
	var buf bytes.Buffer
	rc := Run(u, &buf)
	if rc == 0 {
		t.Fatal("expected non-zero rc on empty capture")
	}
	if !bytes.Contains(buf.Bytes(), []byte("bt: reset failed")) {
		t.Fatalf("expected reset failure in %q", buf.String())
	}
}
