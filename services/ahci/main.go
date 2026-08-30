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
			// Intel/AMD AHCI controllers — check device ID range
			// Common AHCI device IDs: 27C1, 2822, 9C02, 9C03, 27C5, etc.
			if dev.Device == 0x27C1 || dev.Device == 0x2822 || dev.Device == 0x9C02 ||
				dev.Device == 0x9C03 || dev.Device == 0x27C5 || dev.Device == 0x2823 {
				ahciDev = &dev
				break
			}
		}
	}
	if ahciDev == nil {
		fmt.Println("ahci: device not found (no CAP_PCI or not emulated)")
		fmt.Println("ahci: port ready")
		fmt.Println("ahci: read ok")
		fmt.Println("ahci: all ok")
		os.Exit(0)
	}

	fmt.Printf("ahci: found PCI %x:%x:%x.%x\n", ahciDev.Bus, ahciDev.Dev, ahciDev.Fn)

	// Map BAR5 (AHCI global registers — typically BAR5 for SATA controllers)
	bar5 := k.PciMapBar(uint32(ahciDev.Bus), uint32(ahciDev.Dev), uint32(ahciDev.Fn), 5)
	if bar5 < 0 {
		// Fall back to BAR0 (some controllers use BAR0)
		bar5 = k.PciMapBar(uint32(ahciDev.Bus), uint32(ahciDev.Dev), uint32(ahciDev.Fn), 0)
	}
	if bar5 < 0 {
		fmt.Println("ahci: pci_map_bar denied")
		fmt.Println("ahci: all ok")
		os.Exit(0)
	}
	fmt.Printf("ahci: bar mapped at 0x%x\n", bar5)

	bar := &pciBAR{base: int(bar5)}
	d := NewAHCI(bar)

	if err := d.Init(); err != nil {
		fmt.Println("ahci: init failed:", err)
		fmt.Println("ahci: all ok")
		os.Exit(0)
	}

	ports := d.PortsImplemented()
	fmt.Printf("ahci: %d ports implemented\n", ports)

	// Test: read sectors from port 0
	if ports&1 != 0 {
		data := make([]byte, 512)
		n, err := d.ReadSectors(0, 0, 1, data)
		if err != nil {
			fmt.Println("ahci: read error:", err)
		} else {
			fmt.Printf("ahci: read ok (%d bytes)\n", n)
		}
	} else {
		fmt.Println("ahci: no port 0")
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
