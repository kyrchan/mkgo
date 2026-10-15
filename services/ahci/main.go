//go:build wasip1

package main

import (
	"fmt"
	"os"
	"unsafe"

	lib "kernel.lane/guests/lib"
)

func ptrAt(off int64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

func main() {
	k := lib.Real()

	// Find AHCI device on PCI bus (class 010401 or 010601)
	var ahciDev *lib.PciDeviceInfo
	for _, dev := range k.DevmanEnum() {
		if dev.Vendor == 0x8086 || dev.Vendor == 0x1022 || dev.Vendor == 0x103C {
			// Intel/AMD AHCI controllers. 0x2922 is the q35 PCH SATA
			// (present on every q35 machine, which is what the gates
			// boot); the rest are the standalone ICH9/AMD IDs.
			if dev.Device == 0x27C1 || dev.Device == 0x2822 || dev.Device == 0x9C02 ||
				dev.Device == 0x9C03 || dev.Device == 0x27C5 || dev.Device == 0x2823 ||
				dev.Device == 0x2922 {
				ahciDev = &dev
				break
			}
		}
	}
	if ahciDev == nil {
		// See the e1000 guest: printing the success markers from the
		// failure path made `make test-p13` unfalsifiable.
		fmt.Println("ahci: FAIL device not found (no CAP_PCI or not emulated)")
		os.Exit(1)
	}

	// Same stray fourth %x as e1000: it printed %!x(MISSING) for the
	// function and made the BDF unreadable.
	fmt.Printf("ahci: found PCI %x:%x:%x\n", ahciDev.Bus, ahciDev.Dev, ahciDev.Fn)

	// Map the ABAR. SATA controllers use BAR5 (ICH9/q35) or BAR0; the
	// window offset is NOT the BAR index -- passing the offset here is how
	// the first mediated attempt read BAR0 of a BAR5 controller and got
	// zeroes back from every register.
	barIndex := uint32(5)
	win := k.PciMapBar(uint32(ahciDev.Bus), uint32(ahciDev.Dev), uint32(ahciDev.Fn), barIndex)
	if win < 0 {
		barIndex = 0
		win = k.PciMapBar(uint32(ahciDev.Bus), uint32(ahciDev.Dev), uint32(ahciDev.Fn), 0)
	}
	if win < 0 {
		fmt.Println("ahci: FAIL pci_map_bar denied")
		os.Exit(1)
	}
	fmt.Printf("ahci: bar%d mapped at 0x%x\n", barIndex, win)

	// Mediated access (ABI v2.5): forwards register reads/writes to the
	// kernel. The window is guest linear memory, not device MMIO.
	bar := lib.NewMediatedBAR(k, uint32(ahciDev.Bus), uint32(ahciDev.Dev),
		uint32(ahciDev.Fn), barIndex, 0x1000).WithWindow(uint64(win))
	d := NewAHCI(bar)

	if err := d.Init(); err != nil {
		fmt.Println("ahci: FAIL init:", err)
		os.Exit(1)
	}

	ports := d.PortsImplemented()

	// The vendor signature is a device-only value: it cannot come from
	// guest memory, so reading a non-zero VS0 through the mediated path is
	// the assertion that the kernel really forwarded the access.
	vs0, vs1 := d.VendorSignature()
	if vs0 == 0 && vs1 == 0 {
		fmt.Println("ahci: FAIL empty vendor signature (device did not answer)")
		os.Exit(1)
	}
	fmt.Printf("ahci: vs=%08x%08x\n", vs0, vs1)
	if ports&1 == 0 {
		fmt.Println("ahci: FAIL no port 0 implemented")
		os.Exit(1)
	}
	fmt.Printf("ahci: %d ports implemented\n", ports)
	// A sector read needs device-visible memory for DMA, which the kernel
	// cannot hand out yet (see lib.MediatedBAR). Ask for it rather than
	// reporting a read that never touched a disk.
	if bar.Region(0, 4096) == nil {
		fmt.Println("ahci: registers ok, dma unavailable (no iommu-mapped buffers)")
	} else {
		data := make([]byte, 512)
		if n, err := d.ReadSectors(0, 0, 1, data); err == nil {
			fmt.Printf("ahci: read ok (%d bytes)\n", n)
		} else {
			fmt.Println("ahci: FAIL read:", err)
			os.Exit(1)
		}
	}
	fmt.Println("ahci: all ok")
	os.Exit(0)
}

type pciBAR struct {
	base int
}

func (b *pciBAR) Read32(off int) uint32 {
	p := ptrAt(int64(b.base+off), 4)
	return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
}

func (b *pciBAR) Write32(off int, v uint32) {
	p := ptrAt(int64(b.base+off), 4)
	p[0] = byte(v)
	p[1] = byte(v >> 8)
	p[2] = byte(v >> 16)
	p[3] = byte(v >> 24)
}

func (b *pciBAR) Region(off, size int) []byte {
	return ptrAt(int64(b.base+off), size)
}
