package kern

import "unsafe"

// MediatedBAR is a PCI BAR view for drivers.
//
// It deliberately does NOT dereference the offset returned by PciMapBar.
// That window is the guest's own linear memory: the kernel runs one flat
// identity map with no per-session page tables (rule 2), so the window
// cannot be aliased onto the BAR's physical range. A driver that wrote
// through the window pointer was writing to its own heap while believing
// it programmed a device -- which is how the Phase 13 gate used to pass.
//
// Every access here is forwarded to the kernel, which performs the cycle
// at the physical address and checks two things first: that the BDF is
// assigned to the caller's session, and that the address is inside that
// session's IOMMU domain.
//
// Region() -- the bulk []byte view the ring code wants -- is not available
// through this path and returns nil. Handing a device a pointer to guest
// memory for DMA needs the memory to be device-visible, which on this
// platform means an IOMMU-mapped page; until the kernel can hand one out,
// a driver that gets nil here must report that it cannot do DMA rather
// than pretend. Host tests still exercise the ring code through their own
// mock BAR.
type MediatedBAR struct {
	k        Kernel
	Bus      uint32
	Dev      uint32
	Fn       uint32
	Bar      uint32
	Size     uint32
	lastFail bool
	// DMA state: one refusal is final for the session (the kernel will not
	// change its mind mid-session), and successful mappings are tracked so
	// they can be released.
	dmaRefused bool
	dmaMapped  [][2]uint64
	// winOff is the guest-linear offset PciMapBar returned. Drivers address
	// a BAR with offsets from its base, and the ring buffers they build sit
	// at that offset in guest memory, so a device-visible request has to
	// translate BAR offset -> guest address through it.
	winOff uint64
}

// WithWindow records the window offset returned by PciMapBar so Region can
// turn a BAR-relative offset into the guest address the kernel needs.
func (b *MediatedBAR) WithWindow(win uint64) *MediatedBAR {
	b.winOff = win
	return b
}

// NewMediatedBAR wraps a mapped BAR. The BAR is not mapped as device
// memory; the window returned by PciMapBar is what the kernel records the
// mapping and the IOMMU grant against, and callers must still have called
// PciMapBar so the domain covers the addresses this forwards to.
func NewMediatedBAR(k Kernel, bus, dev, fn, bar, size uint32) *MediatedBAR {
	return &MediatedBAR{k: k, Bus: bus, Dev: dev, Fn: fn, Bar: bar, Size: size}
}

func (b *MediatedBAR) inRange(off int, width int) bool {
	if b.Size == 0 {
		return true // unknown size: the kernel does the bounds check
	}
	return off >= 0 && uint64(off)+uint64(width) <= uint64(b.Size)
}

func (b *MediatedBAR) Read32(off int) uint32 {
	if !b.inRange(off, 4) {
		b.lastFail = true
		return 0
	}
	v := b.k.PciBarRead(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 4)
	if v < 0 {
		b.lastFail = true
		return 0
	}
	return uint32(v)
}

func (b *MediatedBAR) Write32(off int, v uint32) {
	if !b.inRange(off, 4) {
		b.lastFail = true
		return
	}
	if b.k.PciBarWrite(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 4, v) != 0 {
		b.lastFail = true
	}
}

func (b *MediatedBAR) Read16(off int) uint16 {
	if !b.inRange(off, 2) {
		b.lastFail = true
		return 0
	}
	v := b.k.PciBarRead(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 2)
	if v < 0 {
		b.lastFail = true
		return 0
	}
	return uint16(v)
}

func (b *MediatedBAR) Write16(off int, v uint16) {
	if !b.inRange(off, 2) {
		b.lastFail = true
		return
	}
	if b.k.PciBarWrite(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 2, uint32(v)) != 0 {
		b.lastFail = true
	}
}

func (b *MediatedBAR) Read8(off int) uint8 {
	if !b.inRange(off, 1) {
		b.lastFail = true
		return 0
	}
	v := b.k.PciBarRead(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 1)
	if v < 0 {
		b.lastFail = true
		return 0
	}
	return uint8(v)
}

func (b *MediatedBAR) Write8(off int, v uint8) {
	if !b.inRange(off, 1) {
		b.lastFail = true
		return
	}
	if b.k.PciBarWrite(b.Bus, b.Dev, b.Fn, b.Bar, uint32(off), 1, uint32(v)) != 0 {
		b.lastFail = true
	}
}

// Region asks the kernel to make [off, off+size) of this session's linear
// memory reachable for the device, and returns a slice over it on success.
// The address is unchanged: the kernel maps the same frames into the
// device's IOMMU domain, so a descriptor ring is programmed at the address
// the driver already has.
//
// It returns nil when the kernel refuses -- on a platform with no
// programmable IOMMU, which is every platform today -- and a driver must
// treat that as "no DMA available" rather than proceeding. Refusals are
// cached so a driver asking repeatedly does not re-request per descriptor.
func (b *MediatedBAR) Region(off, size int) []byte {
	if size <= 0 {
		return nil
	}
	if b.dmaRefused {
		return nil
	}
	// Round up to a page: the kernel takes a 4 KiB multiple.
	size = (size + 0xFFF) &^ 0xFFF
	guestOff := b.winOff + uint64(off)
	if b.k.PciDmaMap(b.Bus, b.Dev, b.Fn, guestOff, uint32(size)) != 0 {
		b.dmaRefused = true
		return nil
	}
	b.dmaMapped = append(b.dmaMapped, [2]uint64{guestOff, uint64(size)})
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(guestOff))), size)
}

// DmaRefused reports whether the kernel refused device-visible memory, so
// the caller can print why rather than just what it could not do.
func (b *MediatedBAR) DmaRefused() bool { return b.dmaRefused }

// Failed reports whether any access was refused, so a driver can turn a
// silently-zero register read into an error.
func (b *MediatedBAR) Failed() bool { return b.lastFail }
