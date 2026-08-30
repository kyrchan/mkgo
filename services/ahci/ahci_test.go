// ahci_test.go — host tests for the AHCI driver core.
package main

import (
	"testing"
)

type mockBAR struct {
	mem        [0x4000]byte
	ciAutoclear bool
}

func newMockBAR() *mockBAR {
	return &mockBAR{ciAutoclear: true}
}

func (m *mockBAR) Read32(off int) uint32 {
	if off < 0 || off+4 > len(m.mem) {
		return 0
	}
	return uint32(m.mem[off]) | uint32(m.mem[off+1])<<8 |
		uint32(m.mem[off+2])<<16 | uint32(m.mem[off+3])<<24
}

func (m *mockBAR) Write32(off int, v uint32) {
	if off < 0 || off+4 > len(m.mem) {
		return
	}
	// Reactive: HBA reset (bit 1 clears self)
	if off == ahciGhc && v&gHBAReset != 0 {
		m.mem[off] = byte(v &^ gHBAReset)
		m.mem[off+1] = byte((v &^ gHBAReset) >> 8)
		m.mem[off+2] = byte((v &^ gHBAReset) >> 16)
		m.mem[off+3] = byte((v &^ gHBAReset) >> 24)
		return
	}
	// Reactive: PxCMD CLO self-clears
	if off >= portBase && off < portBase+32*portStride && (off-portBase)%portStride == portCmd {
		if v&cmdClo != 0 {
			m.mem[off] = byte(v &^ cmdClo)
			m.mem[off+1] = byte((v &^ cmdClo) >> 8)
			m.mem[off+2] = byte((v &^ cmdClo) >> 16)
			m.mem[off+3] = byte((v &^ cmdClo) >> 24)
			return
		}
	}
	// Reactive: PxCMD CRS clears when FRE is set
	if off >= portBase && off < portBase+32*portStride && (off-portBase)%portStride == portCmd {
		if v&cmdFre != 0 {
			m.mem[off] = byte(v &^ cmdCrs)
			m.mem[off+1] = byte((v &^ cmdCrs) >> 8)
			m.mem[off+2] = byte((v &^ cmdCrs) >> 16)
			m.mem[off+3] = byte((v &^ cmdCrs) >> 24)
			return
		}
	}
	// Reactive: PxCI — writing 1 simulates instant command completion (CI clears)
	if off >= portBase && off < portBase+32*portStride && (off-portBase)%portStride == portCi {
		if v&1 != 0 && m.ciAutoclear {
			m.mem[off] = 0
			m.mem[off+1] = 0
			m.mem[off+2] = 0
			m.mem[off+3] = 0
			return
		}
	}
	m.mem[off] = byte(v)
	m.mem[off+1] = byte(v >> 8)
	m.mem[off+2] = byte(v >> 16)
	m.mem[off+3] = byte(v >> 24)
}

func (m *mockBAR) Region(off, size int) []byte {
	if off < 0 || off+size > len(m.mem) {
		return make([]byte, size)
	}
	return m.mem[off : off+size]
}

func TestPortsImplemented(t *testing.T) {
	m := newMockBAR()
	m.Write32(ahciPi, 0x03) // ports 0 and 1
	d := NewAHCI(m)
	if d.PortsImplemented() != 0x03 {
		t.Errorf("PI = 0x%x, want 0x3", d.PortsImplemented())
	}
}

func TestPortSignature(t *testing.T) {
	m := newMockBAR()
	m.Write32(portBase+portSig, 0x00000101) // SATA sig
	d := NewAHCI(m)
	sig := d.PortSignature(0)
	if sig != 0x00000101 {
		t.Errorf("SIG = 0x%08x, want 0x00000101", sig)
	}
}

func TestReset(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	if err := d.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// HBA reset bit should self-clear
	if m.Read32(ahciGhc)&gHBAReset != 0 {
		t.Error("HBA reset bit did not self-clear")
	}
}

func TestInit(t *testing.T) {
	m := newMockBAR()
	m.Write32(ahciPi, 0x01) // port 0
	d := NewAHCI(m)
	if err := d.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if d.ports != 0x01 {
		t.Errorf("ports = 0x%x, want 0x1", d.ports)
	}
}

func TestInitNoPorts(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	err := d.Init()
	if err == nil {
		t.Error("expected error when no ports implemented")
	}
}

func TestPortReady(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	// CRS clear → ready
	m.Write32(portBase+portCmd, 0)
	if !d.PortReady(0) {
		t.Error("expected port ready (CRS=0)")
	}
	// CRS set → not ready
	m.Write32(portBase+portCmd, cmdCrs)
	if d.PortReady(0) {
		t.Error("expected port not ready (CRS=1)")
	}
}

func TestCLOClears(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0) // FRE=0, CRS=0
	d.PortReset(0)
	if m.Read32(portBase+portCmd)&cmdClo != 0 {
		t.Error("CLO did not self-clear")
	}
}

func TestFREEnablesPort(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0)
	m.Write32(portBase+portCmd, cmdFre)
	cmd := d.bar.Read32(portBase + portCmd)
	if cmd&cmdFre == 0 {
		t.Error("FRE not set")
	}
	if cmd&cmdCrs != 0 {
		t.Error("CRS should clear when FRE is set")
	}
}

func TestReadSectors(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0)
	m.Write32(portBase+portCmd, cmdFre)
	m.Write32(portBase+portClb, 0x200)
	m.Write32(portBase+portCi, 0) // already done
	m.Write32(portBase+portTfd, 0) // no errors
	data := make([]byte, 512)
	n, err := d.ReadSectors(0, 0, 1, data)
	if err != nil {
		t.Fatalf("ReadSectors: %v", err)
	}
	if n != 512 {
		t.Errorf("read %d bytes, want 512", n)
	}
}

func TestReadSectorsTFDError(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0)
	m.Write32(portBase+portCmd, cmdFre)
	m.Write32(portBase+portClb, 0x200)
	m.Write32(portBase+portCi, 0) // command done
	m.Write32(portBase+portTfd, 0x1) // ERR bit set
	data := make([]byte, 512)
	_, err := d.ReadSectors(0, 0, 1, data)
	if err == nil {
		t.Error("expected error for TFD.ERR")
	}
}

func TestReadSectorsNoFre(t *testing.T) {
	m := newMockBAR()
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0) // FRE not set
	_, err := d.ReadSectors(0, 0, 1, nil)
	if err == nil {
		t.Error("expected error when FRE not enabled")
	}
}

func TestReadSectorsTimeout(t *testing.T) {
	m := newMockBAR()
	m.ciAutoclear = false // simulate command that never completes
	d := NewAHCI(m)
	m.Write32(portBase+portCmd, 0)
	m.Write32(portBase+portCmd, cmdFre)
	m.Write32(portBase+portClb, 0x200)
	m.Write32(portBase+portCi, 1) // command never completes
	m.Write32(portBase+portTfd, 0)
	_, err := d.ReadSectors(0, 0, 1, nil)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestBuildCfis(t *testing.T) {
	f := buildCfis(0x25, 0x12345678, 8)
	if f[0] != fisHostToDev {
		t.Errorf("FIS type = 0x%02x, want 0x%02x", f[0], fisHostToDev)
	}
	if f[2] != 0x25 {
		t.Errorf("command = 0x%02x, want 0x25", f[2])
	}
	// LBA 0x12345678:
	if f[3] != 0x78 {
		t.Errorf("LBA[0] = 0x%02x, want 0x78", f[3])
	}
	if f[4] != 0x56 {
		t.Errorf("LBA[1] = 0x%02x, want 0x56", f[4])
	}
	if f[7] != 0x12 {
		t.Errorf("LBA[3] = 0x%02x, want 0x12", f[7])
	}
	// Sector count = 8
	if f[10] != 8 {
		t.Errorf("sector count = %d, want 8", f[10])
	}
}
