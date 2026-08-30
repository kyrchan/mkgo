// xHCI transfer-ring driver: TRB encoding, transfer-ring cycle discipline, and
// the control-transfer (Setup/Data/Status) submission path (AGENTS.md Phase 12,
// device model §8). Core logic is plain Go — runs under `go test` against the
// MockBAR/TransferRing and unmodified as wasm via main.go.
//
// Layout follows the xHCI spec (USB 3.0 eXtensible Host Controller Interface):
// TRB control = DW3; type field = DW3[15:10], TC/cycle = DW3 bit 0, IOC = bit 1,
// DI (direction) = DW3 bits 17:16. TRB types: Normal=1, Setup=2, Data=3,
// Status=4, Link=6. Completion code Success=1 (xHCI spec §6.4.5).
//
// Cycle discipline: the producer stamps each data TRB with its current cycle
// state; the consumer (HC) consumes only TRBs whose cycle bit matches its own.
// A trailing Link TRB (TC set) marks the lap boundary: whoever reaches it
// rewrites it with the current cycle and toggles. This mock models that
// discipline with a queued-counter + cycle bit; on real hardware the xHCI
// host controller performs the equivalent steps over the mapped BAR window.

package main

import (
	"errors"
	"fmt"

	lib "kernel.lane/guests/lib"
)

// XferType classifies an endpoint transfer type (matches the EP-context DCI
// type field; EP0 is always Control).
const (
	XferTypeControl   uint8 = 0
	XferTypeIsoch     uint8 = 1
	XferTypeBulk      uint8 = 2
	XferTypeInterrupt uint8 = 3
)

// Direction is the data direction of a transfer stage.
type Direction uint8

const (
	DirOut Direction = 0 // host -> device
	DirIn  Direction = 1 // device -> host
)

// xHCI TRB type codes (DW3 bits 15:10).
const (
	trbTypeNormal      uint8 = 1
	trbTypeSetup       uint8 = 2
	trbTypeDataStage   uint8 = 3
	trbTypeStatusStage uint8 = 4
	trbTypeLink        uint8 = 6
)

// xHCI completion code (xHCI spec §6.4.5). Success = 1.
const compSuccess uint8 = 1

// TRB Control (DW3) field bits.
const (
	trbTC        uint32 = 1 << 0 // Cycle (TC)
	trbIOC       uint32 = 1 << 1 // Interrupt on Completion
	trbTypeShift uint   = 10
	trbTypeMask  uint32 = 0x3f
	trbDirIn     uint32 = 1 << 16 // DI field: IN
)

// TRB is one 16-byte Transfer Request Block (4 little-endian dwords).
type TRB struct {
	b [16]byte
}

func (t *TRB) DW(dw int) uint32        { return lib.Get32(t.b[dw*4:]) }
func (t *TRB) SetDW(dw int, v uint32) { lib.Put32(t.b[dw*4:], v) }

// SetCycle sets/clears the cycle (TC) bit.
func (t *TRB) SetCycle(on bool) {
	d := t.DW(3)
	if on {
		d |= trbTC
	} else {
		d &^= trbTC
	}
	t.SetDW(3, d)
}

// Cycle reports the cycle (TC) bit.
func (t *TRB) Cycle() bool { return t.DW(3)&trbTC != 0 }

// Type returns the TRB type code.
func (t *TRB) Type() uint8 { return uint8((t.DW(3) >> trbTypeShift) & trbTypeMask) }

// SetType sets the TRB type field.
func (t *TRB) SetType(typ uint8) {
	d := t.DW(3)
	d &^= trbTypeMask << trbTypeShift
	d |= uint32(typ) << trbTypeShift
	t.SetDW(3, d)
}

// DirIn reports whether the DI field marks an IN transfer.
func (t *TRB) DirIn() bool { return t.DW(3)&trbDirIn != 0 }

// SetupPacket is the 8-byte USB 2.0/3.0 SETUP packet.
type SetupPacket struct {
	BMRequestType byte
	BRequest      byte
	WValue        uint16
	WIndex        uint16
	WLength       uint16
}

// Direction returns the data direction implied by bmRequestType.
func (s SetupPacket) Direction() Direction {
	if s.BMRequestType&0x80 != 0 {
		return DirIn
	}
	return DirOut
}

// DataDirection returns the data-stage direction (DirOut if no data).
func (s SetupPacket) DataDirection() Direction {
	if s.WLength == 0 {
		return DirOut
	}
	return s.Direction()
}

// StatusDirection is opposite to the data direction (USB §8.5.1 rule).
func (s SetupPacket) StatusDirection() Direction {
	if s.WLength == 0 {
		return DirIn // no data stage -> status is IN
	}
	return s.opposite(s.Direction())
}

func (s SetupPacket) opposite(d Direction) Direction {
	if d == DirIn {
		return DirOut
	}
	return DirIn
}

// BuildSetupTRB builds a Setup Stage TRB (type 2) carrying the 8-byte setup
// packet in DW0/DW1.
func BuildSetupTRB(s SetupPacket, cycle, ioc bool) TRB {
	var t TRB
	t.SetDW(0, uint32(s.BMRequestType)|uint32(s.BRequest)<<8|uint32(s.WValue)<<16)
	t.SetDW(1, uint32(s.WIndex)|uint32(s.WLength)<<16)
	t.SetDW(2, 0)
	ctrl := uint32(trbTypeSetup) << trbTypeShift
	if cycle {
		ctrl |= trbTC
	}
	if ioc {
		ctrl |= trbIOC
	}
	if s.Direction() == DirIn {
		ctrl |= trbDirIn
	}
	t.SetDW(3, ctrl)
	return t
}

// BuildDataTRB builds a Data Stage TRB (type 3) for a buffer at addr.
func BuildDataTRB(addr uint64, xferLen uint16, dir Direction, cycle, ioc bool) TRB {
	var t TRB
	t.SetDW(0, uint32(addr))
	t.SetDW(1, uint32(addr>>32))
	t.SetDW(2, uint32(xferLen)) // TRB Transfer Length (bits 0-21)
	ctrl := uint32(trbTypeDataStage) << trbTypeShift
	if cycle {
		ctrl |= trbTC
	}
	if ioc {
		ctrl |= trbIOC
	}
	if dir == DirIn {
		ctrl |= trbDirIn
	}
	t.SetDW(3, ctrl)
	return t
}

// BuildStatusTRB builds a Status Stage TRB (type 4).
func BuildStatusTRB(dir Direction, cycle, ioc bool) TRB {
	var t TRB
	t.SetDW(0, 0)
	t.SetDW(1, 0)
	t.SetDW(2, 0)
	ctrl := uint32(trbTypeStatusStage) << trbTypeShift
	if cycle {
		ctrl |= trbTC
	}
	if ioc {
		ctrl |= trbIOC
	}
	if dir == DirIn {
		ctrl |= trbDirIn
	}
	t.SetDW(3, ctrl)
	return t
}

// BuildLinkTRB builds a Link TRB (type 6) pointing to slot 0, TC set so the
// consumer toggles its cycle on wrap.
func BuildLinkTRB(cycle bool) TRB {
	var t TRB
	t.SetDW(0, 0) // target slot 0
	t.SetDW(1, 0)
	t.SetDW(2, 0)
	ctrl := uint32(trbTypeLink) << trbTypeShift
	if cycle {
		ctrl |= trbTC
	}
	t.SetDW(3, ctrl)
	return t
}

// TransferRing is a host-testable xHCI transfer ring. The last slot is a Link
// TRB (TC set) marking the lap boundary; the producer rewrites it on wrap and
// toggles pcycle, the consumer toggles ccycle on traversal.
type TransferRing struct {
	trbs    []TRB
	pcycle  bool // producer cycle state
	ccycle  bool // consumer (HC) expected cycle
	enq     int
	deq     int
	queued  int  // data TRBs pending consumer consumption
	dataN   int  // count of usable data slots (== linkIdx)
	linkIdx int  // index of the Link TRB
}

// NewTransferRing allocates a ring with dataSlots data TRBs + one trailing
// Link TRB (size = dataSlots+1).
func NewTransferRing(dataSlots int) *TransferRing {
	if dataSlots < 2 {
		dataSlots = 2
	}
	n := dataSlots + 1
	r := &TransferRing{
		trbs:    make([]TRB, n),
		dataN:   dataSlots,
		linkIdx: dataSlots,
	}
	r.trbs[r.linkIdx] = BuildLinkTRB(false)
	return r
}

// Len reports the number of unconsumed data TRBs.
func (r *TransferRing) Len() int { return r.queued }

// Enqueue appends a data TRB; returns false if the ring is full.
func (r *TransferRing) Enqueue(t TRB) bool {
	if r.queued >= r.dataN {
		return false // full — ring must be drained before more
	}
	if r.enq == r.linkIdx {
		// lap wrap: rewrite the Link TRB with the current cycle, toggle.
		r.trbs[r.linkIdx] = BuildLinkTRB(r.pcycle)
		r.pcycle = !r.pcycle
		r.enq = 0
	}
	t.SetCycle(r.pcycle)
	r.trbs[r.enq] = t
	r.enq++
	r.queued++
	return true
}

// Consume returns the next ready data TRB (traversing a Link TRB if the
// dequeue has reached the lap boundary). ok=false when no data TRB is ready.
func (r *TransferRing) Consume() (TRB, bool) {
	if r.queued == 0 {
		return TRB{}, false
	}
	t := r.trbs[r.deq]
	if t.Type() == trbTypeLink {
		// boundary: follow the link and toggle the consumer cycle.
		r.ccycle = !r.ccycle
		r.deq = int(t.DW(0)) // link target (slot 0)
		t = r.trbs[r.deq]
	}
	if t.Type() == trbTypeLink {
		return TRB{}, false // safety: no consecutive links
	}
	if t.Cycle() != r.ccycle {
		return TRB{}, false // not owned by consumer yet
	}
	r.deq = (r.deq + 1) % len(r.trbs)
	r.queued--
	return t, true
}

// EnqueuedCycle returns the producer's current cycle (for assertions).
func (r *TransferRing) EnqueuedCycle() bool { return r.pcycle }

// Completion is one Transfer Event the mock HC produces (xHCI spec §4.8.2).
type Completion struct {
	Slot       int
	Endpoint   int
	Code       uint8 // completion code (1 = success)
	XferLength uint16 // bytes transferred
}

// XhciEndpoint is one endpoint's transfer ring.
type XhciEndpoint struct {
	ID   int
	Type uint8 // XferType
	ring *TransferRing
}

// XhciSlot models one enabled device slot.
type XhciSlot struct {
	ID  int
	Eps map[int]*XhciEndpoint
}

func newXhciSlot(slotID int) *XhciSlot {
	return &XhciSlot{ID: slotID, Eps: map[int]*XhciEndpoint{}}
}

// EnableSlot creates (or fetches) a device slot with an EP0 control ring.
func (c *UsbController) EnableSlot(slotID int) error {
	if slotID < 1 || slotID > xhciMaxSlots {
		return fmt.Errorf("usb: slot id %d out of range", slotID)
	}
	if _, ok := c.slots[slotID]; ok {
		return nil
	}
	s := newXhciSlot(slotID)
	s.Eps[0] = &XhciEndpoint{ID: 0, Type: XferTypeControl, ring: NewTransferRing(8)}
	c.slots[slotID] = s
	return nil
}

// ringDoorbell records a doorbell in MMIO (if in range) and marks a pending
// target for the mock scheduler's Process().
func (c *UsbController) ringDoorbell(slotID, endpoint int) {
	if c.dbOff > 0 {
		c.bar.wr32(c.dbOff+xhciDbStride*slotID, uint32(endpoint))
	}
	c.pendingDB[slotID] = endpoint
}

// RingDoorbell notifies the host that endpoint ring `endpoint` of `slotID` has
// pending TRBs. On real hardware this wakes the xHCI; the mock scheduler's
// Process() consumes it.
func (c *UsbController) RingDoorbell(slotID, endpoint int) {
	c.ringDoorbell(slotID, endpoint)
}

// SubmitControlTransfer builds the Setup[/Data]/Status TRBs into the EP0
// ring of slotID and rings the doorbell. dataAddr is the guest address of the
// data buffer (ignored by the host mock, which synthesizes a success). On real
// hardware main.go supplies a window-backed buffer.
func (c *UsbController) SubmitControlTransfer(slotID int, s SetupPacket, dataAddr uint64, data []byte) error {
	slot, ok := c.slots[slotID]
	if !ok {
		return fmt.Errorf("usb: slot %d not enabled", slotID)
	}
	ep, ok := slot.Eps[0]
	if !ok {
		return fmt.Errorf("usb: slot %d ep0 missing", slotID)
	}
	if ep.Type != XferTypeControl {
		return fmt.Errorf("usb: ep0 not control (type %d)", ep.Type)
	}
	r := ep.ring
	xferLen := uint16(0)
	if s.WLength > 0 && len(data) > 0 {
		if uint16(len(data)) > s.WLength {
			xferLen = s.WLength
		} else {
			xferLen = uint16(len(data))
		}
	}
	ioc := true
	if !r.Enqueue(BuildSetupTRB(s, r.pcycle, ioc)) {
		return errors.New("usb: transfer ring full")
	}
	if s.WLength > 0 {
		if !r.Enqueue(BuildDataTRB(dataAddr, xferLen, s.DataDirection(), r.pcycle, ioc)) {
			return errors.New("usb: transfer ring full")
		}
	}
	if !r.Enqueue(BuildStatusTRB(s.StatusDirection(), r.pcycle, ioc)) {
		return errors.New("usb: transfer ring full")
	}
	c.ringDoorbell(slotID, 0) // EP0 doorbell (RType=transfer, endpoint 0)
	return nil
}

// Process drives one mock transfer: consumes the EP0 ring of a doorbell-pending
// slot up to the Status Stage of the current Transfer Descriptor and records a
// Completion. On real hardware the xHCI performs this work; this exists for
// host tests.
func (c *UsbController) Process() *Completion {
	if len(c.pendingDB) == 0 {
		return nil
	}
	var slotID, endpoint int
	for sid, ep := range c.pendingDB {
		slotID, endpoint = sid, ep
		break
	}
	slot := c.slots[slotID]
	ep := slot.Eps[endpoint]
	r := ep.ring

	var lastData TRB
	haveData := false
	seenSetup := false
	for {
		t, okc := r.Consume()
		if !okc {
			break
		}
		switch t.Type() {
		case trbTypeSetup:
			seenSetup = true
			haveData = false
		case trbTypeDataStage:
			haveData = true
			lastData = t
		case trbTypeStatusStage:
			if !seenSetup {
				break
			}
			xl := uint16(0)
			if haveData && lastData.DirIn() {
				xl = uint16(r.queued) // mock: device returns queued-residual; 0 on full
			}
			delete(c.pendingDB, slotID)
			return &Completion{
				Slot: slotID, Endpoint: endpoint,
				Code: compSuccess, XferLength: xl,
			}
		}
	}
	return nil
}

// Completions returns (and clears) the completion log.
func (c *UsbController) Completions() []*Completion {
	out := c.completions
	c.completions = nil
	return out
}

// DoorbellPending reports whether a slot's doorbell is outstanding.
func (c *UsbController) DoorbellPending(slotID int) bool {
	_, ok := c.pendingDB[slotID]
	return ok
}
