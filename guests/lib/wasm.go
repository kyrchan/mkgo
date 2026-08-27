//go:build wasip1

package kern

import "fmt"

// Raw bindings over the frozen ABI v1 imports. The kernel links these
// under module "kernel" (core/wasi_glue.cc); sched_yield is the frozen
// WASI profile's. Signatures must not drift from abi/ABI.md §1/§4.

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield()

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func port_send(h Handle, buf *byte, length uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h Handle, buf *byte, cap uint32) int32

//go:wasmimport kernel kern_input_recv
func input_recv(buf *byte, cap uint32) int32

//go:wasmimport kernel kern_focus_set
func focus_set(h Handle)

// === VFIO (ABI §12/§13/§14, v2.0) ===

//go:wasmimport kernel kern_pci_read32
func pci_read32(bus, dev, fn, offset uint32) int32

//go:wasmimport kernel kern_pci_write32
func pci_write32(bus, dev, fn, offset, val uint32) int32

//go:wasmimport kernel kern_pci_map_bar
func pci_map_bar(bus, dev, fn, bar uint32) int64

//go:wasmimport kernel kern_pci_unmap_bar
func pci_unmap_bar(bus, dev, fn, bar uint32) int32

//go:wasmimport kernel kern_pci_enable_busmaster
func pci_enable_busmaster(bus, dev, fn uint32) int32

//go:wasmimport kernel kern_pci_bind_irq
func pci_bind_irq(bus, dev, fn, irqType uint32) int32

//go:wasmimport kernel kern_pci_flr
func pci_flr(bus, dev, fn uint32) int32

//go:wasmimport kernel kern_fb_set_mode
func fb_set_mode(w, h, bpp uint32) int32

//go:wasmimport kernel kern_fb_set_cursor
func fb_set_cursor(x, y uint32) int32

//go:wasmimport kernel kern_doorbell_wait
func doorbell_wait(handle, timeoutMs uint32) int32

type realKernel struct{}

// Real returns the wasm-backed Kernel (raw kernel imports).
func Real() Kernel { return realKernel{} }

func sb(s string) []byte {
	b := make([]byte, len(s))
	copy(b, s)
	return b
}

func (realKernel) PortCreate(name string) Handle {
	if len(name) == 0 || len(name) > MaxName {
		return InvalidHandle
	}
	b := sb(name)
	return port_create(&b[0], uint32(len(b)))
}

func (realKernel) PortBind(name string) Handle {
	if len(name) == 0 || len(name) > MaxName {
		return InvalidHandle
	}
	b := sb(name)
	return port_bind(&b[0], uint32(len(b)))
}

func (realKernel) PortSend(h Handle, data []byte) int32 {
	if len(data) == 0 || len(data) > MaxMsg {
		return StatusErr // kernel rejects len==0 / oversize outright
	}
	return port_send(h, &data[0], uint32(len(data)))
}

func (realKernel) PortRecv(h Handle, buf []byte) int32 {
	if len(buf) == 0 {
		return StatusErr
	}
	return port_recv(h, &buf[0], uint32(len(buf)))
}

func (realKernel) InputRecv(buf []byte) int32 {
	if len(buf) == 0 {
		return 0
	}
	return input_recv(&buf[0], uint32(len(buf)))
}

func (realKernel) FocusSet(h Handle) { focus_set(h) }

func (realKernel) Yield() { sched_yield() }

// === VFIO implementations ===

func (realKernel) PciRead32(bus, dev, fn, offset uint32) int32 {
	return pci_read32(bus, dev, fn, offset)
}

func (realKernel) PciWrite32(bus, dev, fn, offset, val uint32) int32 {
	return pci_write32(bus, dev, fn, offset, val)
}

func (realKernel) PciMapBar(bus, dev, fn, bar uint32) int64 {
	return pci_map_bar(bus, dev, fn, bar)
}

func (realKernel) PciUnmapBar(bus, dev, fn, bar uint32) int32 {
	return pci_unmap_bar(bus, dev, fn, bar)
}

func (realKernel) PciEnableBusmaster(bus, dev, fn uint32) int32 {
	return pci_enable_busmaster(bus, dev, fn)
}

func (realKernel) PciBindIrq(bus, dev, fn, irqType uint32) (int32, error) {
	h := pci_bind_irq(bus, dev, fn, irqType)
	if h < 0 {
		return -1, fmt.Errorf("bind_irq failed")
	}
	return h, nil
}

func (realKernel) PciFlr(bus, dev, fn uint32) int32 {
	return pci_flr(bus, dev, fn)
}

func (realKernel) FbSetMode(w, h, bpp uint32) int32 {
	return fb_set_mode(w, h, bpp)
}

func (realKernel) FbSetCursor(x, y uint32) int32 {
	return fb_set_cursor(x, y)
}

func (realKernel) DoorbellWait(handle, timeoutMs uint32) int32 {
	return doorbell_wait(handle, timeoutMs)
}

func (realKernel) DevmanEnum() []PciDeviceInfo {
	// Use devman ENUM to count devices; class 10 = PCI (VFIO)
	dc, err := BindDevman(realKernel{})
	if err != nil {
		return nil
	}
	devs, err := dc.Enum()
	if err != nil {
		return nil
	}
	out := make([]PciDeviceInfo, 0, len(devs))
	for _, d := range devs {
		if d.Class == 10 { // ClassPCI
			out = append(out, PciDeviceInfo{
				Bus:    uint8(d.Inst >> 8),
				Dev:    uint8(d.Inst),
				Fn:     0,
				Vendor: uint16(d.WinOff),
				Device: uint16(d.WinOff >> 16),
			})
		}
	}
	return out
}
