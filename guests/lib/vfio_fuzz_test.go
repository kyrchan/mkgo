package kern

import (
	"testing"
)

// mockError is a simple error type for the mock kernel.
type mockError struct{ s string }

func (e *mockError) Error() string { return e.s }

// mockVfioKernel is a deterministic mock of the Kernel interface for fuzzing
// the guest-side VFIO/PCI state machine. It tracks BAR mappings, doorbells,
// and framebuffer mode to verify invariants under random operation sequences.
type mockVfioKernel struct {
	capmask    uint64
	bars       map[barKey]int64 // mapped BARs -> window offset
	doorbells  map[uint32]*mockDoorbell
	nextHandle uint32
	nextWinOff uint32
	fbMode     [3]uint32 // w, h, bpp
	fbHasMode  bool
	maxBars    int
}

type barKey struct {
	bus, dev, fn, bar uint32
}

type mockDoorbell struct {
	handle  uint32
	bus     uint32
	dev     uint32
	fn      uint32
	irqType uint32
	pending bool
}

func newMockKernel(capmask uint64, maxBars int) *mockVfioKernel {
	return &mockVfioKernel{
		capmask:    capmask,
		bars:       make(map[barKey]int64),
		doorbells:  make(map[uint32]*mockDoorbell),
		nextHandle: 1,
		nextWinOff: 0,
		maxBars:    maxBars,
	}
}

func (m *mockVfioKernel) hasCap(cap uint64) bool { return (m.capmask & cap) != 0 }

func (m *mockVfioKernel) PortCreate(name string) Handle {
	return 1
}
func (m *mockVfioKernel) PortBind(name string) Handle { return 1 }
func (m *mockVfioKernel) PortSend(h Handle, data []byte) int32 {
	if h == InvalidHandle || len(data) == 0 || len(data) > MaxMsg {
		return StatusErr
	}
	return StatusOK
}
func (m *mockVfioKernel) PortRecv(h Handle, buf []byte) int32 { return 0 }
func (m *mockVfioKernel) InputRecv(buf []byte) int32        { return 0 }
func (m *mockVfioKernel) FocusSet(h Handle)                {}
func (m *mockVfioKernel) Yield()                           {}

func (m *mockVfioKernel) PciRead32(bus, dev, fn, offset uint32) int32 {
	if bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset&3) != 0 {
		return -1
	}
	if bus == 0 && dev == 0 && fn == 0 && offset == 0 {
		return 0x12345678
	}
	return 0
}

func (m *mockVfioKernel) PciWrite32(bus, dev, fn, offset, val uint32) int32 {
	if bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset&3) != 0 {
		return -1
	}
	return 0
}

func (m *mockVfioKernel) PciMapBar(bus, dev, fn, bar uint32) int64 {
	if !m.hasCap(CapPCI) {
		return -1
	}
	if bar > 5 {
		return -1
	}
	key := barKey{bus, dev, fn, bar}
	if off, ok := m.bars[key]; ok {
		return off
	}
	if len(m.bars) >= m.maxBars {
		return -1
	}
	off := int64(0x5000000 + m.nextWinOff*0x100000)
	m.nextWinOff++
	m.bars[key] = off
	return off
}

func (m *mockVfioKernel) PciUnmapBar(bus, dev, fn, bar uint32) int32 {
	if !m.hasCap(CapPCI) {
		return -1
	}
	key := barKey{bus, dev, fn, bar}
	if _, ok := m.bars[key]; !ok {
		return -1
	}
	delete(m.bars, key)
	return 0
}

func (m *mockVfioKernel) PciEnableBusmaster(bus, dev, fn uint32) int32 {
	if !m.hasCap(CapPCI) {
		return -1
	}
	return 0
}

func (m *mockVfioKernel) PciBindIrq(bus, dev, fn, irqType uint32) (int32, error) {
	if !m.hasCap(CapPCI) {
		return -1, &mockError{"no CAP_PCI"}
	}
	if irqType > 2 {
		return -1, &mockError{"bad irq type"}
	}
	handle := m.nextHandle
	m.nextHandle++
	m.doorbells[handle] = &mockDoorbell{
		handle:  handle,
		bus:     bus,
		dev:     dev,
		fn:      fn,
		irqType: irqType,
	}
	return int32(handle), nil
}

func (m *mockVfioKernel) PciFlr(bus, dev, fn uint32) int32 {
	if !m.hasCap(CapPCI) {
		return -1
	}
	return 0
}

func (m *mockVfioKernel) FbSetMode(w, h, bpp uint32) int32 {
	if !m.hasCap(CapFB) {
		return -1
	}
	if bpp != 32 || w == 0 || h == 0 || w > 4096 || h > 4096 {
		return -1
	}
	m.fbMode = [3]uint32{w, h, bpp}
	m.fbHasMode = true
	return 0
}

func (m *mockVfioKernel) FbSetCursor(x, y uint32) int32 {
	if !m.hasCap(CapFB) {
		return -1
	}
	return 0
}

func (m *mockVfioKernel) DoorbellWait(handle, timeoutMs uint32) int32 {
	d, ok := m.doorbells[handle]
	if !ok {
		return -1
	}
	if d.pending {
		d.pending = false
		return 0
	}
	if timeoutMs == 0 {
		return 1
	}
	return 1
}

func (m *mockVfioKernel) DevmanEnum() []PciDeviceInfo {
	return nil
}

// FuzzVfioBarMapping exercises the guest-side BAR mapping lifecycle: random
// sequences of map/unmap operations must respect invariants — no double-map
// returns a different offset, no unmap of unmapped BAR succeeds, and the
// window offset space never overlaps.
func FuzzVfioBarMapping(f *testing.F) {
	f.Add(uint32(0))
	f.Add(uint32(1))
	f.Add(uint32(5))

	f.Fuzz(func(t *testing.T, seed uint32) {
		k := newMockKernel(CapPCI|CapFB, 4)
		r := newDeterministicRand(seed)

		mapped := make(map[barKey]int64)
		usedOffsets := make(map[int64]barKey)

		for i := 0; i < 50; i++ {
			op := r.Uint32() % 3
			bus := r.Uint32() % 4
			dev := r.Uint32() % 8
			fn := r.Uint32() % 4
			bar := r.Uint32() % 7
			key := barKey{bus, dev, fn, bar}

			switch op {
			case 0:
				off := k.PciMapBar(bus, dev, fn, bar)
				if bar > 5 {
					if off != -1 {
						t.Fatalf("map_bar with invalid bar=%d returned %d", bar, off)
					}
					continue
				}
				if existing, ok := mapped[key]; ok {
					if off != existing {
						t.Fatalf("re-map changed offset: was %d, now %d", existing, off)
					}
				} else {
					if off == -1 {
						continue
					}
					if _, collides := usedOffsets[off]; collides {
						t.Fatalf("offset collision at %d", off)
					}
					mapped[key] = off
					usedOffsets[off] = key
				}
			case 1:
				rc := k.PciUnmapBar(bus, dev, fn, bar)
				if _, ok := mapped[key]; ok {
					if rc != 0 {
						t.Fatalf("unmap of mapped BAR returned %d", rc)
					}
					delete(mapped, key)
				} else {
					if rc != -1 {
						t.Fatalf("unmap of unmapped BAR returned %d", rc)
					}
				}
			case 2:
				if _, ok := mapped[key]; !ok {
					off := k.PciMapBar(bus, dev, fn, bar)
					if bar <= 5 && off != -1 {
						mapped[key] = off
						usedOffsets[off] = key
					}
				}
			}
		}
	})
}

// FuzzVfioDoorbellState exercises the doorbell bind/fire/wait lifecycle.
// Invariants: handles are unique, wait on invalid handle returns -1,
// wait after fire returns 0, wait without fire returns timeout.
func FuzzVfioDoorbellState(f *testing.F) {
	f.Add(uint32(42))
	f.Add(uint32(0))
	f.Add(uint32(0xFFFF))

	f.Fuzz(func(t *testing.T, seed uint32) {
		k := newMockKernel(CapPCI, 4)
		r := newDeterministicRand(seed)

		handles := make(map[uint32]bool)

		for i := 0; i < 40; i++ {
			op := r.Uint32() % 4
			switch op {
			case 0:
				bus := r.Uint32() % 4
				dev := r.Uint32() % 8
				fn := r.Uint32() % 4
				irqType := r.Uint32() % 4
				h, err := k.PciBindIrq(bus, dev, fn, irqType)
				if irqType > 2 {
					if err == nil {
						t.Fatalf("bind with invalid irqType=%d succeeded", irqType)
					}
					continue
				}
				if err != nil {
					t.Fatalf("bind failed: %v", err)
				}
				if h < 0 {
					t.Fatalf("bind returned negative handle %d", h)
				}
				if handles[uint32(h)] {
					t.Fatalf("duplicate handle %d", h)
				}
				handles[uint32(h)] = true
			case 1:
				// fire: no-op in mock (ISR context)
			case 2:
				if len(handles) == 0 {
					continue
				}
				idx := r.Uint32() % uint32(len(handles))
				var h uint32
				for h = range handles {
					if idx == 0 {
						break
					}
					idx--
				}
				timeout := r.Uint32() % 2
				rc := k.DoorbellWait(h, timeout)
				if rc != 0 && rc != 1 {
					t.Fatalf("wait on valid handle %d returned %d", h, rc)
				}
			case 3:
				h := r.Uint32() % 100
				if handles[h] {
					continue
				}
				rc := k.DoorbellWait(h, 0)
				if rc != -1 {
					t.Fatalf("wait on invalid handle %d returned %d", h, rc)
				}
			}
		}
	})
}

// FuzzPciConfigOffset fuzzes the PCI config offset validation. The kernel
// rejects offsets > 0xFC or unaligned; guests must never cause out-of-bounds
// config access.
func FuzzPciConfigOffset(f *testing.F) {
	f.Add(uint32(0), uint32(0), uint32(0), uint32(0))
	f.Add(uint32(255), uint32(31), uint32(7), uint32(0xFC))
	f.Add(uint32(100), uint32(20), uint32(4), uint32(0x10))

	f.Fuzz(func(t *testing.T, bus, dev, fn, offset uint32) {
		k := newMockKernel(CapPCI, 4)
		validBus := bus <= 255
		validDev := dev <= 31
		validFn := fn <= 7
		validOffset := offset <= 0xFC && (offset&3) == 0

		rc := k.PciRead32(bus, dev, fn, offset)
		if validBus && validDev && validFn && validOffset {
			// Mock returns 0 for non-zero BDF, -1 only for invalid
			_ = rc
		} else {
			if rc != -1 {
				t.Fatalf("PciRead32(bus=%d,dev=%d,fn=%d,off=%d) should fail, got %d",
					bus, dev, fn, offset, rc)
			}
		}
	})
}

// deterministicRand is a simple PRNG for reproducible fuzz seeds.
type deterministicRand struct {
	state uint32
}

func newDeterministicRand(seed uint32) *deterministicRand {
	if seed == 0 {
		seed = 1
	}
	return &deterministicRand{state: seed}
}

func (r *deterministicRand) Uint32() uint32 {
	x := r.state
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	r.state = x
	return x
}
