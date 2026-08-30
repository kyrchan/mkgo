// xHCI host controller driver skeleton (AGENTS.md Phase 11, ABI §9 class 8).
//
// Host-testable Go: the UsbController accesses controller registers through
// a mock BAR (byte slice) that simulates PCI MMIO. On real hardware the bar
// would be the BAR mapped via kern_pci_map_bar (ABI §12); the driver logic is
// identical. Register layout follows the xHCI spec's capability/operational
// split. No .wasm output yet — pure host test per the task.
package main

import (
	"errors"
	"fmt"

	lib "kernel.lane/guests/lib"
)

// --- xHCI capability register offsets (abi/ABI.md §12 MMIO at PCI BAR0) ---
const (
	xhciCapLength  = 0x00 // CAPLENGTH: size of operational regs
	xhciHcVersion  = 0x02 // HCIVERSION
	xhciHcsParams1 = 0x04 // HCSPARAMS1: max ports[31:24], max slots[23:16]
	xhciHcsParams2 = 0x08 // HCSPARAMS2
	xhciHccParams1 = 0x10 // HCCPARAMS1
	xhciDbOff      = 0x14 // DBOFF: Doorbell Array offset (dwords, RO)
	xhciRtsOff     = 0x18 // RTSOFF: Runtime Register space offset (RO)
)

// MMIO is the register-window access surface both the host stub (MockBAR,
// a byte slice simulating the PCI BAR) and the real wasip1 deployment (a
// slice over the PciMapBar window) provide. Register offsets are absolute
// within the BAR, little-endian.
type MMIO interface {
	rd8(off int) uint8
	rd32(off int) uint32
	wr32(off int, v uint32)
}

// --- operational register offsets (relative to operational base = CAPLENGTH) ---
const (
	xhciUsbCmd     = 0x00 // USBCMD
	xhciUsbSts     = 0x04 // USBSTS
	xhciPortScBase = 0x400 // PORTSC base (per-port, 0x10 stride)
)

// --- runtime / doorbell layout ---
// xHCI doorbell registers live at MMIO[DBOFF] (read from cap 0x14), one
// 32-bit register per device slot (indexed by slot ID 1..N). Each holds a
// DB Target field (low byte = RType|endpoint) telling the HC which ring to
// process. See xHCI spec §4.7/§5.6.
const (
	xhciDbStride    = 4   // bytes per doorbell register
	xhciMaxSlots    = 255 // slot IDs 1..255
)

// --- USBCMD bits ---
const (
	xhciCmdRun   uint32 = 1 << 0 // Run/Stop
	xhciCmdHcrst uint32 = 1 << 1 // Host Controller Reset (self-clearing)
	xhciCmdIntEn uint32 = 1 << 2 // Interrupter Enable
)

// --- USBSTS bits ---
const (
	xhciStsHalted uint32 = 1 << 0  // HCHalted
	xhciStsHse    uint32 = 1 << 2  // Host System Error
	xhciStsCnr    uint32 = 1 << 11 // Controller Not Ready
)

// --- PORTSC bits (per-port) ---
const (
	xhciPortScCcs uint32 = 1 << 0  // Current Connect Status
	xhciPortScPed uint32 = 1 << 1  // Port Enabled/Disable
	xhciPortScOca uint32 = 1 << 3  // Over-Current Active
	xhciPortScPr  uint32 = 1 << 4  // Port Reset
	xhciPortScPp  uint32 = 1 << 9  // Port Power
	xhciPortScCsc uint32 = 1 << 17 // Connect Status Change (W1C)
	xhciPortScPec uint32 = 1 << 18 // Port Enable/Disable Change (W1C)
	xhciPortScPrc uint32 = 1 << 21 // Port Reset Change (W1C)
)

// minBar is the minimum mock BAR size (capability + operational + 16 ports).
const minBar = 0x500

// UsbController is a host-testable xHCI host controller skeleton. It reads
// and writes controller registers through an MMIO window that simulates PCI
// BAR MMIO. The same driver code targets real hardware by swapping the mock
// for the PciMapBar window (ABI §12).
type UsbController struct {
	bar      MMIO
	base     int // operational register base (CAPLENGTH)
	maxPorts int // from HCSPARAMS1[31:24]

	// dbOff is the doorbell-array MMIO offset (read once from DBOFF cap reg).
	dbOff int
	// slots tracks enabled device slots (slotID -> slot). On real hardware the
	// slots are tracked by the device/device-context arrays in guest RAM; here
	// we model just enough to submit control transfers on EP0.
	slots     map[int]*XhciSlot
	pendingDB map[int]int // slotID -> endpoint ring target (door bell pending)
	// nextCompletion is the head of the host-side completion log (test-only).
	completions []*Completion
}

// NewUsbController creates a controller backed by an MMIO window (mock or
// real). It reads the capability registers to size the operational region
// and port count.
func NewUsbController(bar MMIO) (*UsbController, error) {
	if bar == nil {
		return nil, errors.New("usb: nil MMIO")
	}
	c := &UsbController{bar: bar, slots: map[int]*XhciSlot{}, pendingDB: map[int]int{}}
	c.base = int(bar.rd8(xhciCapLength))
	if c.base == 0 {
		c.base = 0x40
	}
	c.maxPorts = int((bar.rd32(xhciHcsParams1) >> 24) & 0xFF)
	if c.maxPorts == 0 || c.maxPorts > 16 {
		c.maxPorts = 4
	}
	c.dbOff = int(bar.rd32(xhciDbOff))
	if c.dbOff == 0 {
		c.dbOff = 0x180 // sane default if DBOFF reads as 0
	}
	return c, nil
}

// rdOp32 reads a u32 from operational space.
func (c *UsbController) rdOp32(off int) uint32 { return c.bar.rd32(c.base + off) }

// wrOp32 writes a u32 to operational space.
func (c *UsbController) wrOp32(off int, v uint32) { c.bar.wr32(c.base+off, v) }

// portSc returns the operational offset of PORTSC for a 1-based port.
func (c *UsbController) portSc(port int) int {
	return xhciPortScBase + (port-1)*16
}

// Reset performs the xHCI host-controller reset sequence:
//  1. Stop the controller (clear Run).
//  2. Poll until HCHalted.
//  3. Assert HCRST (self-clearing).
//  4. Poll until HCRST clears.
//  5. Poll until CNR (Controller Not Ready) clears.
//
// Returns an error if any step times out.
func (c *UsbController) Reset() error {
	c.wrOp32(xhciUsbCmd, c.rdOp32(xhciUsbCmd) &^ xhciCmdRun)

	for i := 0; i < 1000; i++ {
		if c.rdOp32(xhciUsbSts)&xhciStsHalted != 0 {
			break
		}
		if i == 999 {
			return errors.New("usb: reset timeout waiting for halt")
		}
	}

	c.wrOp32(xhciUsbCmd, xhciCmdHcrst)

	for i := 0; i < 1000; i++ {
		if c.rdOp32(xhciUsbCmd)&xhciCmdHcrst == 0 {
			break
		}
		if i == 999 {
			return errors.New("usb: reset timeout waiting for HCRST clear")
		}
	}

	for i := 0; i < 1000; i++ {
		if c.rdOp32(xhciUsbSts)&xhciStsCnr == 0 {
			return nil
		}
	}
	return errors.New("usb: reset timeout waiting for ready")
}

// PortStatus reads and decodes the PORTSC register for a 1-based port.
// Returns an error if the port is out of range.
func (c *UsbController) PortStatus(port int) (PortStatus, error) {
	if port < 1 || port > c.maxPorts {
		return PortStatus{}, fmt.Errorf("usb: port %d out of range [1,%d]", port, c.maxPorts)
	}
	sc := c.rdOp32(c.portSc(port))
	return PortStatus{
		ConnectStatus: sc&xhciPortScCcs != 0,
		Enabled:       sc&xhciPortScPed != 0,
		Powered:       sc&xhciPortScPp != 0,
		Reset:         sc&xhciPortScPr != 0,
		OverCurrent:   sc&xhciPortScOca != 0,
		Raw:           sc,
	}, nil
}

// PortEnable powers on a port (1-based) and clears sticky change bits.
// Returns an error if the port is over-current or out of range.
func (c *UsbController) PortEnable(port int) error {
	if port < 1 || port > c.maxPorts {
		return fmt.Errorf("usb: port %d out of range [1,%d]", port, c.maxPorts)
	}
	off := c.portSc(port)
	sc := c.rdOp32(off)
	if sc&xhciPortScOca != 0 {
		return fmt.Errorf("usb: port %d over-current", port)
	}
	sc |= xhciPortScPp
	c.wrOp32(off, sc)
	// Clear change bits (write-1-to-clear).
	c.wrOp32(off, sc&xhciPortScPp|xhciPortScCsc|xhciPortScPec|xhciPortScPrc)
	return nil
}

// PortStatus is the decoded state of one xHCI port.
type PortStatus struct {
	ConnectStatus bool
	Enabled       bool
	Powered       bool
	Reset         bool
	OverCurrent   bool
	Raw           uint32
}

// MockBAR simulates an xHCI MMIO region for host testing. It reacts to key
// register writes so driver sequences (reset, port enable) complete without
// manual test choreography:
//   - USBCMD.Run=0  → sets HCHalted in USBSTS.
//   - USBCMD.HCRST  → self-clears, sets then clears CNR (reset done).
//   - PORTSC.PP=1   → latches Powered in subsequent reads.
type MockBAR struct {
	mem [minBar]byte
}

// NewMockBAR returns a zeroed mock BAR with sane capability defaults
// (CAPLENGTH=0x40, 4 ports).
func NewMockBAR() *MockBAR {
	m := &MockBAR{}
	m.mem[xhciCapLength] = 0x40
	lib.Put32(m.mem[xhciHcsParams1:], 4<<24) // 4 ports
	return m
}

// rd8 reads a u8 from capability space.
func (m *MockBAR) rd8(off int) uint8 {
	if off >= 0 && off < len(m.mem) {
		return m.mem[off]
	}
	return 0
}

// rd32 reads a u32 (LE) from the MMIO region, applying reactive behavior
// for operational registers.
func (m *MockBAR) rd32(off int) uint32 {
	if off < 0 || off+4 > len(m.mem) {
		return 0
	}
	// USBSTS reacts to the current USBCMD state.
	if off == int(m.mem[xhciCapLength])+xhciUsbSts {
		cmd := lib.Get32(m.mem[int(m.mem[xhciCapLength])+xhciUsbCmd:])
		sts := uint32(0)
		if cmd&xhciCmdRun == 0 {
			sts |= xhciStsHalted
		}
		return sts
	}
	return lib.Get32(m.mem[off:])
}

// wr32 writes a u32 (LE) to the MMIO region, applying reactive behavior.
func (m *MockBAR) wr32(off int, v uint32) {
	if off < 0 || off+4 > len(m.mem) {
		return
	}
	base := int(m.mem[xhciCapLength])
	// USBCMD: HCRST self-clears and triggers a CNR cycle.
	if off == base+xhciUsbCmd {
		v &^= xhciCmdHcrst // HCRST never reads back set
		lib.Put32(m.mem[off:], v)
		return
	}
	lib.Put32(m.mem[off:], v)
}
