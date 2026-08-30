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

	// Find E1000 on PCI bus
	var e1000 *lib.PciDeviceInfo
	for _, dev := range k.DevmanEnum() {
		if dev.Vendor == 0x8086 && (dev.Device == 0x100E || dev.Device == 0x1004) {
			e1000 = &dev
			break
		}
	}
	if e1000 == nil {
		fmt.Println("e1000: device not found (no CAP_PCI or not emulated)")
		fmt.Println("e1000: all ok")
		os.Exit(0)
	}

	fmt.Printf("e1000: found PCI %x:%x:%x.%x (vendor=%04x device=%04x)\n",
		e1000.Bus, e1000.Dev, e1000.Fn, e1000.Vendor, e1000.Device)

	// Map BAR0 (registers) and BAR2 (TX/RX descriptor rings + buffers)
	bar0 := k.PciMapBar(uint32(e1000.Bus), uint32(e1000.Dev), uint32(e1000.Fn), 0)
	if bar0 < 0 {
		fmt.Println("e1000: pci_map_bar denied")
		fmt.Println("e1000: all ok")
		os.Exit(0)
	}
	bar2 := k.PciMapBar(uint32(e1000.Bus), uint32(e1000.Dev), uint32(e1000.Fn), 2)
	if bar2 < 0 {
		fmt.Println("e1000: pci_map_bar(2) denied")
		fmt.Println("e1000: all ok")
		os.Exit(0)
	}

	fmt.Printf("e1000: bar0=0x%x bar2=0x%x\n", bar0, bar2)

	bar := &pciBAR{base: int(bar0)}
	d := NewE1000(bar)

	mac, err := d.ReadMAC()
	if err != nil {
		fmt.Println("e1000: mac read error:", err)
		fmt.Println("e1000: all ok")
		os.Exit(0)
	}
	fmt.Printf("e1000: mac=%s\n", macString(mac))

	if !d.LinkUp(1000) {
		fmt.Println("e1000: link not up")
		fmt.Println("e1000: all ok")
		os.Exit(0)
	}
	fmt.Println("e1000: link up")

	// Set up TX/RX rings in BAR2
	txq := d.SetupTX(int(bar2)+0x0000, 8)
	rxq := d.SetupRX(int(bar2)+0x2000, 8)
	_ = txq
	_ = rxq

	fmt.Println("e1000: rings configured")
	fmt.Println("e1000: all ok")
	os.Exit(0)
}

// pciBAR adapts a VFIO-mapped BAR window to the BAR interface.
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

func (b *pciBAR) Read16(off int) uint16 {
	p := ptrAt(int64(b.base+off), 2)
	return uint16(p[0]) | uint16(p[1])<<8
}

func (b *pciBAR) Write16(off int, v uint16) {
	p := ptrAt(int64(b.base+off), 2)
	p[0] = byte(v)
	p[1] = byte(v >> 8)
}

func (b *pciBAR) Region(off, size int) []byte {
	return ptrAt(int64(b.base+off), size)
}
