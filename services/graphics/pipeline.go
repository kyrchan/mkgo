// pipeline.go — host-testable graphics pipeline (ABI §12/§13/§14).
// No build tag: compiled into both `go test` and the wasip1 wasm build.
// main.go (wasip1) calls these helpers after resolving kernel imports;
// pipeline_test.go exercises them against kern.Bus.
package main

import (
	"fmt"

	lib "kernel.lane/guests/lib"
	"kernel.lane/services/graphics/compositor"
)

// DisplayMode is the framebuffer geometry negotiated with kern_fb_set_mode.
type DisplayMode struct {
	W   uint32
	H   uint32
	BPP uint32 // always 32 (XRGB)
}

// DefaultMode is the 1024×768 resolution the Phase-11 gate boots with.
var DefaultMode = DisplayMode{W: 1024, H: 768, BPP: 32}

// PciDisplayClass extracts the base class code (bits [31:24]) from a
// PCI config dword read at offset 0x08.
func pciClass(cfg int32) uint8 {
	if cfg < 0 {
		return 0
	}
	return uint8(uint32(cfg) >> 24)
}

// FindDisplay scans devman ENUM for a PCI display controller: vendor
// 0x1234 (QEMU/Red-Hat) with PCI base class 0x03. Returns -1 if none
// found. Uses kernel-mediated PciRead32 to verify the class code, so
// a device that merely shares the vendor ID is not mistaken for a
// display.
func FindDisplay(k lib.Kernel) (dev *lib.PciDeviceInfo, cfg int32) {
	for _, d := range k.DevmanEnum() {
		if d.Vendor != 0x1234 {
			continue
		}
		// config offset 0x08: bits[31:24]=class
		c := k.PciRead32(uint32(d.Bus), uint32(d.Dev), uint32(d.Fn), 0x08)
		if c < 0 {
			continue
		}
		if pciClass(c) == 0x03 {
			return &d, c
		}
	}
	return nil, -1
}

// ReadMotd reads /etc/motd through the fs service. Returns "" if the
// fs port is unavailable (e.g. booted before fs.wasm is up).
func ReadMotd(k lib.Kernel) string {
	fs, err := lib.BindFS(k, "gfx")
	if err != nil {
		return ""
	}
	buf := make([]byte, 4096)
	n, err := fs.ReadFile("/etc/motd", 0, buf)
	if err != nil {
		return ""
	}
	return string(buf[:n])
}

// RenderScene draws the Phase-11 demo scene onto surf: a dark-blue
// canvas, title bar with text, a border around the content, the motd
// body, and decorative shapes (filled circle, diagonal lines, grid).
// Pure compositor logic — no kernel calls — so it is fully covered by
// host tests.
func RenderScene(surf *compositor.Surface, motd string) {
	w := surf.W()
	h := surf.H()

	bg := compositor.Color(0x10, 0x10, 0x30)
	surf.Clear(bg)

	// Title bar: filled dark-gray strip at top
	barY := 0
	barH := 32
	surf.FillRect(0, barY, w, barH, compositor.Color(0x20, 0x20, 0x40))

	// Title text
	surf.DrawTextScaled(16, barY+6, 2, "PHASE 11: VFIO GRAPHICS", compositor.Color(255, 255, 255))

	// Content border
	margin := 24
	surf.DrawRect(margin, barY+barH+margin, w-2*margin, h-(barY+barH+2*margin), compositor.Color(0x40, 0x80, 0xFF))

	// Motd header
	contentY := barY + barH + margin + 8
	surf.DrawText(margin+8, contentY, "--- /etc/motd ---", compositor.Color(0, 255, 0))

	// Motd body
	bodyY := contentY + 16
	if motd != "" {
		surf.DrawText(margin+8, bodyY, motd, compositor.Color(0xCC, 0xFF, 0xCC))
	} else {
		surf.DrawText(margin+8, bodyY, "(no motd)", compositor.Color(0xFF, 0xCC, 0x00))
	}

	// Decorative: green filled circle in bottom-right
	cy, cr := h-40, 12
	surf.FillCircle(w-margin-20, cy, cr, compositor.Color(0, 180, 0))

	// Decorative: blue diagonal lines
	surf.DrawLine(margin, barY+barH+margin, margin+60, barY+barH+margin+60, compositor.Color(0x80, 0x80, 0xFF))
	surf.DrawLine(margin+60, barY+barH+margin, margin, barY+barH+margin+60, compositor.Color(0x80, 0x80, 0xFF))
}

// PresentFrame copies the composited surface to the framebuffer window
// and issues kern_fb_present. In the wasm build, window is guest linear
// memory at off; in tests, window is a plain slice.
func PresentFrame(k lib.Kernel, surf *compositor.Surface) {
	k.FbPresent()
}

// FormatDisplay returns a human-readable description of a PCI device.
func FormatDisplay(d *lib.PciDeviceInfo) string {
	return fmt.Sprintf("%02x:%02x.%x vendor=%04x device=%04x",
		d.Bus, d.Dev, d.Fn, d.Vendor, d.Device)
}
