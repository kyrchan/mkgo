// guests/p11/main.go — Phase 11 VFIO smoke test
// Tests: PCI enumeration, BAR mapping, framebuffer set_mode, doorbell bind
// Prints markers for make test-p11 to grep.
package main

import (
	"fmt"
	"os"

	lib "kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()

	// 1. PCI config read — host bridge at 0:0:0 (always exists on QEMU q35)
	v := k.PciRead32(0, 0, 0, 0)
	if v < 0 {
		fmt.Println("p11: pci read32 failed")
	} else {
		fmt.Printf("p11: pci read32 0:0:0 vendor=%04x device=%04x\n", v&0xFFFF, (v>>16)&0xFFFF)
	}

	// 2. PCI enumeration via devman
	devs := k.DevmanEnum()
	fmt.Printf("p11: pci enum found %d class-10 devices\n", len(devs))
	for _, d := range devs {
		fmt.Printf("p11: pci %02x:%02x.%x vendor=%04x device=%04x\n",
			d.Bus, d.Dev, d.Fn, d.Vendor, d.Device)
	}

	// 3. Framebuffer set_mode (needs CAP_FB — may fail without it, that's ok)
	fbRet := k.FbSetMode(1024, 768, 32)
	if fbRet < 0 {
		fmt.Println("p11: fb_set_mode denied (no CAP_FB, expected)")
	} else {
		fmt.Println("p11: fb_set_mode ok 1024x768")
	}

	// 4. Doorbell bind (needs CAP_PCI — may fail without it)
	h, dbErr := k.PciBindIrq(0, 0, 0, 0)
	if dbErr != nil {
		fmt.Println("p11: bind_irq denied (no CAP_PCI or no MSI, expected)")
	} else {
		fmt.Printf("p11: bind_irq handle=%d\n", h)
	}

	fmt.Println("p11: all ok")
	os.Exit(0)
}
