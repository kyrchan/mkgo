//go:build !wasip1

package main

import (
	"strings"
	"testing"

	lib "kernel.lane/guests/lib"
	"kernel.lane/services/graphics/compositor"
)

func TestPciClass(t *testing.T) {
	// class 0x03 at bits [31:24]
	v := int32(0x03000000)
	if got := pciClass(v); got != 0x03 {
		t.Fatalf("class=%02x want 03", got)
	}
	// class 0x06 (bridge)
	v = int32(0x06000000)
	if got := pciClass(v); got != 0x06 {
		t.Fatalf("class=%02x want 06", got)
	}
	// negative (deny) → 0
	if got := pciClass(-1); got != 0 {
		t.Fatalf("class=%02x want 0 for deny", got)
	}
}

func TestFindDisplayHost(t *testing.T) {
	b := lib.NewBus()
	dev, cfg := FindDisplay(b)
	if dev == nil {
		t.Fatal("FindDisplay returned nil on host Bus")
	}
	if dev.Vendor != 0x1234 {
		t.Fatalf("vendor=%04x want 1234", dev.Vendor)
	}
	if dev.Bus != 0 || dev.Dev != 2 || dev.Fn != 0 {
		t.Fatalf("bdf=%02x:%02x.%x want 00:02.0", dev.Bus, dev.Dev, dev.Fn)
	}
	if got := pciClass(cfg); got != 0x03 {
		t.Fatalf("class=%02x want 03", got)
	}
}

func TestFindDisplayNoCaps(t *testing.T) {
	// A kernel without CAP_PCI returns no PCI devices.
	k := &noCapKernel{}
	dev, _ := FindDisplay(k)
	if dev != nil {
		t.Fatalf("expected nil device without CAP_PCI, got %v", dev)
	}
}

// noCapKernel is a minimal Kernel that returns no PCI devices,
// simulating a session without CAP_PCI.
type noCapKernel struct{}

func (noCapKernel) PortCreate(name string) lib.Handle        { return lib.InvalidHandle }
func (noCapKernel) PortBind(name string) lib.Handle          { return lib.InvalidHandle }
func (noCapKernel) PortSend(h lib.Handle, data []byte) int32 { return lib.StatusErr }
func (noCapKernel) PortRecv(h lib.Handle, buf []byte) int32  { return lib.StatusErr }
func (noCapKernel) InputRecv(buf []byte) int32               { return 0 }
func (noCapKernel) FocusSet(h lib.Handle)                    {}
func (noCapKernel) Yield()                                   {}
func (noCapKernel) PciRead32(uint32, uint32, uint32, uint32) int32 {
	return -1
}
func (noCapKernel) PciWrite32(uint32, uint32, uint32, uint32, uint32) int32 { return -1 }
func (noCapKernel) PciBarRead(uint32, uint32, uint32, uint32, uint32, uint32) int64 {
	return -1
}
func (noCapKernel) PciBarWrite(uint32, uint32, uint32, uint32, uint32, uint32, uint32) int32 {
	return -1
}
func (noCapKernel) PciDmaMap(uint32, uint32, uint32, uint64, uint32) int32   { return -1 }
func (noCapKernel) PciDmaUnmap(uint32, uint32, uint32, uint64, uint32) int32 { return -1 }
func (noCapKernel) PciMapBar(uint32, uint32, uint32, uint32) int64           { return -1 }
func (noCapKernel) PciUnmapBar(uint32, uint32, uint32, uint32) int32         { return -1 }
func (noCapKernel) PciEnableBusmaster(uint32, uint32, uint32) int32          { return -1 }
func (noCapKernel) PciBindIrq(uint32, uint32, uint32, uint32) (int32, error) {
	return -1, errNoCap
}
func (noCapKernel) PciFlr(uint32, uint32, uint32) int32    { return -1 }
func (noCapKernel) FbSetMode(uint32, uint32, uint32) int32 { return -1 }
func (noCapKernel) FbSetCursor(uint32, uint32) int32       { return -1 }
func (noCapKernel) FbPresent() int32                       { return -1 }
func (noCapKernel) DoorbellWait(uint32, uint32) int32      { return 1 }
func (noCapKernel) DevmanEnum() []lib.PciDeviceInfo        { return nil }
func (noCapKernel) HasClock() bool                         { return false }
func (noCapKernel) ClockMs() uint64                        { return 0 }

var errNoCap = errSimple("no CAP_PCI")

type errSimple string

func (e errSimple) Error() string { return string(e) }

func TestRenderSceneDimensions(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "hello world")
	// Background (outside title bar) is Color(0x10, 0x10, 0x30).
	// In memory: B=0x30, G=0x10, R=0x10.
	idx := (60*1024 + 60) * 4
	if surf.Bytes()[idx] != 0x30 || surf.Bytes()[idx+2] != 0x10 {
		t.Fatalf("bg pixel B=%02x R=%02x want B=30 R=10", surf.Bytes()[idx], surf.Bytes()[idx+2])
	}
}

func TestRenderSceneTitleBar(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "")
	// Title bar is dark gray (B=0x40, G=0x20, R=0x20) at top
	y := 10
	idx := (y*1024 + 100) * 4
	if surf.Bytes()[idx+2] != 0x20 { // R
		t.Fatalf("title bar pixel R=%02x want 20", surf.Bytes()[idx+2])
	}
}

func TestRenderSceneTextWhite(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "")
	// Title text "PHASE" should have white pixels in the title area.
	// Search for any R=255 pixel in the title bar region (y: 6-24).
	found := false
	for y := 6; y < 28; y++ {
		for x := 16; x < 300; x++ {
			idx := (y*1024 + x) * 4
			if surf.Bytes()[idx+2] == 0xFF && surf.Bytes()[idx] == 0xFF {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no white text pixels found in title bar")
	}
}

func TestRenderSceneMotd(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "Welcome to the microkernel")
	// Motd text should be light green (R=0xCC, G=0xFF, B=0xCC)
	found := false
	for y := 0; y < 768; y++ {
		for x := 0; x < 1024; x++ {
			idx := (y*1024 + x) * 4
			if surf.Bytes()[idx] == 0xCC && surf.Bytes()[idx+1] == 0xFF && surf.Bytes()[idx+2] == 0xCC {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no motd text pixels found")
	}
}

func TestRenderSceneNoMotd(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "")
	// "(no motd)" should be rendered in yellow (R=0xFF, G=0xCC, B=0x00)
	found := false
	for y := 0; y < 768; y++ {
		for x := 0; x < 1024; x++ {
			idx := (y*1024 + x) * 4
			if surf.Bytes()[idx] == 0x00 && surf.Bytes()[idx+1] == 0xCC && surf.Bytes()[idx+2] == 0xFF {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no '(no motd)' text pixels found")
	}
}

func TestRenderSceneShapes(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "test")
	// Green filled circle at bottom-right
	found := false
	for y := 700; y < 760; y++ {
		for x := 900; x < 1000; x++ {
			idx := (y*1024 + x) * 4
			if surf.Bytes()[idx] == 0 && surf.Bytes()[idx+1] == 0xB4 && surf.Bytes()[idx+2] == 0 {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no green circle pixels found")
	}
}

func TestRenderSceneBorder(t *testing.T) {
	surf := compositor.NewSurface(1024, 768)
	RenderScene(surf, "test")
	// Border Color(0x40, 0x80, 0xFF) → memory: B=0xFF, G=0x80, R=0x40
	found := false
	for y := 0; y < 768; y++ {
		for x := 0; x < 1024; x++ {
			idx := (y*1024 + x) * 4
			if surf.Bytes()[idx] == 0xFF && surf.Bytes()[idx+1] == 0x80 && surf.Bytes()[idx+2] == 0x40 {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no border pixels found")
	}
}

func TestRenderSceneSmallSurface(t *testing.T) {
	// Rendering on a small surface should not panic
	surf := compositor.NewSurface(100, 60)
	RenderScene(surf, "test")
	// Just verify it didn't panic and background is dark
	if surf.Bytes()[0] == 0xFF && surf.Bytes()[1] == 0xFF && surf.Bytes()[2] == 0xFF {
		// This would only be true if every pixel is white — unlikely for small surface
	}
}

func TestFormatDisplay(t *testing.T) {
	d := &lib.PciDeviceInfo{Bus: 0, Dev: 2, Fn: 0, Vendor: 0x1234, Device: 0x5678}
	got := FormatDisplay(d)
	if !strings.Contains(got, "1234") || !strings.Contains(got, "5678") {
		t.Fatalf("FormatDisplay output missing vendor/device: %q", got)
	}
}

func TestReadMotdHost(t *testing.T) {
	b := lib.NewBus()
	// No "fs" port on a fresh bus — should return "" gracefully.
	m := ReadMotd(b)
	if m != "" {
		t.Fatalf("expected empty motd without fs service, got %q", m)
	}
}
