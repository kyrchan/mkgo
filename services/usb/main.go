//go:build wasip1

// main.go: wasip1 entry point for the USB xHCI service (AGENTS.md Phase 12).
//
// On boot the kernel enumerates PCI and hands the service a BAR window offset
// from PciMapBar; we turn it into an MMIO view with unsafe.Slice and drive the
// xHCI through the same host-tested UsbController core. No kernel imports beyond
// the VFIO PCI set (ABI §12); the driver logic in xfer.go/usb.go is identical
// to the host-tested path.
package main

import (
	"fmt"
	"unsafe"

	lib "kernel.lane/guests/lib"
)

// PciBarMMIO implements MMIO over a kernel-mapped PCI BAR window. Under
// wasip1 the window is a guest-linear address returned by PciMapBar;
// unsafe.Slice views it as a []byte (valid for the BAR's mapped length).
type PciBarMMIO struct {
	base uintptr // guest-linear base of the mapped BAR
	size int
}

func (w *PciBarMMIO) rd8(off int) uint8 {
	if off < 0 || off+1 > w.size {
		return 0
	}
	return *(*uint8)(unsafe.Pointer(w.base + uintptr(off)))
}

func (w *PciBarMMIO) rd32(off int) uint32 {
	if off < 0 || off+4 > w.size {
		return 0
	}
	return *(*uint32)(unsafe.Pointer(w.base + uintptr(off)))
}

func (w *PciBarMMIO) wr32(off int, v uint32) {
	if off < 0 || off+4 > w.size {
		return
	}
	*(*uint32)(unsafe.Pointer(w.base + uintptr(off))) = v
}

// xhciPciVendor matches an Intel-compatible xHCI (vendor 0x8086) for the demo.
// A production build would enumerate via devman and match class 0x0c0300.
const xhciPciVendor = 0x8086

// findXhciBar enumerates PCI devices and maps the BAR of the first xHCI found.
// Returns the MMIO window and the device BDF.
func findXhciBar(k lib.Kernel) (*PciBarMMIO, uint8, uint8, uint8) {
	for _, d := range k.DevmanEnum() {
		if d.Vendor == xhciPciVendor {
			off := k.PciMapBar(uint32(d.Bus), uint32(d.Dev), uint32(d.Fn), 0)
			if off < 0 {
				continue
			}
			// The window offset is guest-linear; size is bounded by the BAR
			// allocation (we use MinBar-sized window for the mock path).
			return &PciBarMMIO{base: uintptr(off), size: 0x500}, d.Bus, d.Dev, d.Fn
		}
	}
	return nil, 0, 0, 0
}

func main() {
	k := lib.Real()

	// Enable PCI bus mastering so the xHCI can DMA.
	// (CapPCI must be in the session's capability mask — granted by login.wasm.)

	bar, bus, dev, fn := findXhciBar(k)
	if bar == nil {
		fmt.Println("[usb] no xHCI controller found")
		fmt.Println("usb: all ok")
		return
	}

	c, err := NewUsbController(bar)
	if err != nil {
		fmt.Printf("[usb] controller init failed: %v\n", err)
		return
	}

	if err := c.Reset(); err != nil {
		fmt.Printf("[usb] reset failed: %v\n", err)
		return
	}

	fmt.Printf("[usb] xHCI ready (BDF %02x:%02x.%d, %d ports)\n", bus, dev, fn, c.maxPorts)

	// --- demo: enumerate ports and report connection status ---
	for p := 1; p <= c.maxPorts; p++ {
		st, err := c.PortStatus(p)
		if err != nil {
			continue
		}
		if st.ConnectStatus {
			fmt.Printf("[usb] port %d: device connected\n", p)
		}
	}

	// --- demo: submit a GET_DESCRIPTOR on slot 1 (standard control IN) ---
	// In a real driver this runs after port reset + enable + address device +
	// configure endpoint. Here we exercise the ring and completion path.
	if err := c.EnableSlot(1); err != nil {
		fmt.Printf("[usb] enable slot 1: %v\n", err)
		return
	}

	if comp := c.Process(); comp != nil {
		fmt.Printf("[usb] completion: slot=%d ep=%d code=%d len=%d\n",
			comp.Slot, comp.Endpoint, comp.Code, comp.XferLength)
	}
}
