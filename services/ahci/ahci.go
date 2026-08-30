// ahci.go — AHCI SATA storage driver core (Phase 13, ABI §12 VFIO).
package main

import (
	"errors"
)

// --- AHCI global register offsets ---
const (
	ahciCap = 0x00 // Host Capabilities
	ahciGhc = 0x04 // Global Host Control
	ahciIs  = 0x08 // Interrupt Status
	ahciPi  = 0x0C // Ports Implemented
	ahciVs  = 0x10 // AHCI Version
)

// Per-port register offsets (port base = 0x100 + port*0x80)
const (
	portClb  = 0x00 // Command List Base (low 32)
	portClbu = 0x04 // Command List Base Upper
	portFis  = 0x08 // FIS Base Address (low 32)
	portFisu = 0x0C // FIS Base Address Upper
	portIs   = 0x10 // Interrupt Status
	portIe   = 0x14 // Interrupt Enable
	portCmd  = 0x18 // Port Command and Control (PxCMD)
	portTfd  = 0x20 // Task File Data
	portSig  = 0x24 // Signature
	portSerr = 0x28 // SATA Error
	portCi   = 0x30 // Command Issue
	portSact = 0x34 // SActive
	portFbs  = 0x38 // FIS-Based Switching
)

const (
	portStride = 0x80
	portBase   = 0x100
)

// GHC (Global Host Control) bits
const (
	gHBAReset = 1 << 1 // HBA Reset (self-clearing)
	gHBIE     = 1 << 2 // Interrupt Enable
)

// PxCMD bits (AHCI 1.3.1)
const (
	cmdFre  = 1 << 4  // FIS Receive Enable
	cmdClo  = 1 << 7  // Clear On Lock
	cmdCrs  = 1 << 24 // Command Running
	cmdSste = 1 << 25 // SActive STr Enable
)

// FIS types
const (
	fisHostToDev = 0x27
)

// --- BAR interface ---
type BAR interface {
	Read32(off int) uint32
	Write32(off int, v uint32)
	Region(off, size int) []byte
}

// --- AHCI driver ---
type AHCIDriver struct {
	bar   BAR
	ports uint32
}

func NewAHCI(bar BAR) *AHCIDriver {
	return &AHCIDriver{bar: bar}
}

func portRegOffset(port int) int {
	return portBase + port*portStride
}

// PortsImplemented reads the PI register.
func (d *AHCIDriver) PortsImplemented() uint32 {
	return d.bar.Read32(ahciPi)
}

// PortSignature reads the signature for a port.
func (d *AHCIDriver) PortSignature(port int) uint32 {
	return d.bar.Read32(portRegOffset(port) + portSig)
}

// PortReady checks if the port's command list is not running (CRS clear).
func (d *AHCIDriver) PortReady(port int) bool {
	cmd := d.bar.Read32(portRegOffset(port) + portCmd)
	return cmd&cmdCrs == 0
}

func (d *AHCIDriver) WaitReady(port int, timeout int) bool {
	for i := 0; i < timeout; i++ {
		if d.PortReady(port) {
			return true
		}
	}
	return false
}

// Reset performs a global HBA reset.
func (d *AHCIDriver) Reset() error {
	d.bar.Write32(ahciGhc, gHBAReset)
	for i := 0; i < 100000; i++ {
		if d.bar.Read32(ahciGhc)&gHBAReset == 0 {
			return nil
		}
	}
	return errors.New("ahci: HBA reset timeout")
}

// PortReset resets a specific port by setting CLO.
func (d *AHCIDriver) PortReset(port int) error {
	base := portRegOffset(port)
	if !d.WaitReady(port, 1000) {
		return errors.New("ahci: port not ready before reset")
	}
	cmd := d.bar.Read32(base + portCmd)
	d.bar.Write32(base+portCmd, cmd|cmdClo)
	for i := 0; i < 1000; i++ {
		cmd = d.bar.Read32(base + portCmd)
		if cmd&cmdClo == 0 {
			break
		}
	}
	return nil
}

// buildCfis constructs a Host-to-Device FIS for READ SECTOR EXT (0x25).
func buildCfis(cmd byte, lba uint64, sectors uint16) [20]byte {
	var f [20]byte
	f[0] = fisHostToDev
	f[1] = 0x80
	f[2] = cmd
	f[3] = byte(lba)
	f[4] = byte(lba >> 8)
	f[5] = byte(lba >> 16)
	f[7] = byte(lba >> 24)
	f[8] = byte(lba >> 32)
	f[9] = byte(lba >> 40)
	f[10] = byte(sectors)
	f[11] = byte(sectors >> 8)
	return f
}

// ReadSectors reads `sectors` 512-byte sectors from `lba` on `port`.
func (d *AHCIDriver) ReadSectors(port int, lba uint64, sectors uint16, data []byte) (int, error) {
	if port >= 32 {
		return 0, errors.New("ahci: invalid port")
	}
	base := portRegOffset(port)
	cmd := d.bar.Read32(base + portCmd)
	if cmd&cmdFre == 0 {
		return 0, errors.New("ahci: FIS receive not enabled")
	}
	cfis := buildCfis(0x25, lba, sectors)
	clb := int(d.bar.Read32(base + portClb))
	if clb == 0 {
		return 0, errors.New("ahci: no command list base")
	}
	ctOff := clb + 32
	ct := d.bar.Region(ctOff, 20)
	copy(ct, cfis[:])
	entry := d.bar.Region(clb, 32)
	put32(entry[0:4], uint32(ctOff))
	d.bar.Write32(base+portCi, 1)
	for i := 0; i < 100000; i++ {
		if d.bar.Read32(base+portCi)&1 == 0 {
			tfd := d.bar.Read32(base + portTfd)
			if tfd&0x1 != 0 {
				return 0, errors.New("ahci: read error (TFD.ERR)")
			}
			return int(sectors) * 512, nil
		}
	}
	d.bar.Write32(base+portCi, 0)
	return 0, errors.New("ahci: read timeout")
}

// Init performs basic AHCI initialization: reset HBA, enable, enumerate ports.
func (d *AHCIDriver) Init() error {
	if err := d.Reset(); err != nil {
		return err
	}
	d.bar.Write32(ahciGhc, gHBIE)
	d.ports = d.bar.Read32(ahciPi)
	if d.ports == 0 {
		return errors.New("ahci: no ports implemented")
	}
	return nil
}

func put32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
