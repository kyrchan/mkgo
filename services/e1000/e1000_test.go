// e1000_test.go — host tests for the E1000 driver core.
package main

import (
	"testing"
)

// mockBAR simulates E1000 MMIO registers for host testing.
type mockBAR struct {
	mem   [0x4000]byte
	eeprom [64]uint16
}

func newMockBAR() *mockBAR {
	m := &mockBAR{}
	// Pre-populate some EEPROM words for testing
	m.eeprom[0] = 0x0420 // typical EEPROM checksum
	return m
}

func (m *mockBAR) Read32(off int) uint32 {
	if off < 0 || off+4 > len(m.mem) {
		return 0
	}
	// Little-endian read
	return uint32(m.mem[off]) | uint32(m.mem[off+1])<<8 | uint32(m.mem[off+2])<<16 | uint32(m.mem[off+3])<<24
}

func (m *mockBAR) Write32(off int, v uint32) {
	if off < 0 || off+4 > len(m.mem) {
		return
	}
	// Reactive EERD: on Start, load data from EEPROM and set Done
	if off == e1000RegEerd && v&1 != 0 {
		addr := (v >> 8) & 0xFF
		data := uint32(m.eeprom[addr&63])
		result := data<<16 | 0x10 // Done bit + data
		m.mem[off] = byte(result)
		m.mem[off+1] = byte(result >> 8)
		m.mem[off+2] = byte(result >> 16)
		m.mem[off+3] = byte(result >> 24)
		return
	}
	m.mem[off] = byte(v)
	m.mem[off+1] = byte(v >> 8)
	m.mem[off+2] = byte(v >> 16)
	m.mem[off+3] = byte(v >> 24)
}

func (m *mockBAR) Read16(off int) uint16 {
	if off < 0 || off+2 > len(m.mem) {
		return 0
	}
	return uint16(m.mem[off]) | uint16(m.mem[off+1])<<8
}

func (m *mockBAR) Write16(off int, v uint16) {
	if off < 0 || off+2 > len(m.mem) {
		return
	}
	m.mem[off] = byte(v)
	m.mem[off+1] = byte(v >> 8)
}

func (m *mockBAR) Region(off, size int) []byte {
	if off < 0 || off+size > len(m.mem) {
		return make([]byte, size)
	}
	return m.mem[off : off+size]
}

func TestReadMAC(t *testing.T) {
	m := newMockBAR()
	// Set a MAC: 00:11:22:33:44:55
	m.Write32(e1000RegMacLow, 0x33221100)
	m.Write32(e1000RegMacHigh, 0x00005544)
	d := NewE1000(m)
	mac, err := d.ReadMAC()
	if err != nil {
		t.Fatalf("ReadMAC: %v", err)
	}
	expected := [6]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	if mac != expected {
		t.Errorf("MAC = %s, want %s", macString(mac), macString(expected))
	}
}

func TestReadMACZero(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	_, err := d.ReadMAC()
	if err == nil {
		t.Error("expected error for zero MAC")
	}
}

func TestLinkUp(t *testing.T) {
	m := newMockBAR()
	// Set link-up bit in STATUS
	m.Write32(e1000RegStatus, e1000StatusLanDone)
	d := NewE1000(m)
	if !d.LinkUp(10) {
		t.Error("expected link up")
	}
}

func TestLinkUpTimeout(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	if d.LinkUp(5) {
		t.Error("expected link down timeout")
	}
}

func TestSetupTX(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	q := d.SetupTX(0x3800, 8)
	if q == nil {
		t.Fatal("SetupTX returned nil")
	}
	// Check TDT is 0
	if m.Read32(e1000RegTxDt) != 0 {
		t.Errorf("TDT = %d, want 0", m.Read32(e1000RegTxDt))
	}
}

func TestTransmit(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	q := d.SetupTX(0x3800, 8)
	pkt := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x08, 0x00}
	if !q.Transmit(pkt) {
		t.Error("Transmit failed")
	}
	// Check TDT advanced
	if m.Read32(e1000RegTxDt) != 1 {
		t.Errorf("TDT = %d, want 1", m.Read32(e1000RegTxDt))
	}
}

func TestSetupRX(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	q := d.SetupRX(0x2800, 8)
	if q == nil {
		t.Fatal("SetupRX returned nil")
	}
	// Check RCTL has receiver enable + broadcast accept
	rctl := m.Read32(e1000RegRctl)
	if rctl&e1000RctlEn == 0 {
		t.Error("RCTL receiver enable not set")
	}
}

func TestPollRecvEmpty(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	q := d.SetupRX(0x2800, 8)
	if q.PollRecv() != nil {
		t.Error("expected nil when no packets")
	}
}

func TestPollRecvPacket(t *testing.T) {
	m := newMockBAR()
	d := NewE1000(m)
	q := d.SetupRX(0x2800, 8)
	// Simulate a received packet: set DD bit and length in descriptor 0
	desc := m.Region(0x2800, 16)
	desc[12] = 0x03 // DD + EOP
	desc[13] = 0x00
	desc[4] = 0x06 // length = 6
	desc[5] = 0x00
	// Put data in rxBuf at offset 0
	d.rxBuf[0] = 0xAA
	d.rxBuf[1] = 0xBB
	// Set link up
	m.Write32(e1000RegStatus, e1000StatusLanDone)
	pkt := q.PollRecv()
	if pkt == nil {
		t.Fatal("expected packet")
	}
	if len(pkt) != 6 {
		t.Errorf("packet length = %d, want 6", len(pkt))
	}
	if pkt[0] != 0xAA || pkt[1] != 0xBB {
		t.Errorf("packet data mismatch: got %x %x", pkt[0], pkt[1])
	}
}

func TestEEPROMRead(t *testing.T) {
	m := newMockBAR()
	m.eeprom[0] = 0x0420
	d := NewE1000(m)
	val, err := d.ReadEEPROM(0)
	if err != nil {
		t.Fatalf("ReadEEPROM: %v", err)
	}
	if val != 0x0420 {
		t.Errorf("EEPROM = 0x%04x, want 0x0420", val)
	}
}

func TestMACString(t *testing.T) {
	mac := [6]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}
	s := macString(mac)
	if s != "00:11:22:33:44:55" {
		t.Errorf("macString = %s, want 00:11:22:33:44:55", s)
	}
}
