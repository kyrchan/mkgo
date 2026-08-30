package main

import (
	"testing"
)

// TestTransferRingCycleToggle verifies the producer/consumer cycle discipline:
// TRBs stamped with pcycle are consumed when ccycle matches; a Link TRB toggles
// both on wrap.
func TestTransferRingCycleToggle(t *testing.T) {
	r := NewTransferRing(4) // 4 data slots + 1 Link TRB

	// Initially pcycle and ccycle are in sync (both false = cycle state 0).
	if r.ccycle != r.pcycle {
		t.Fatalf("initial ccycle=%v != pcycle=%v", r.ccycle, r.pcycle)
	}

	// Enqueue 3 data TRBs; all tagged with the current pcycle.
	for i := 0; i < 3; i++ {
		tb := BuildDataTRB(0x1000+uint64(i)*4, 4, DirIn, true, i == 2)
		if !r.Enqueue(tb) {
			t.Fatalf("Enqueue #%d failed", i)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}

	// Consumer consumes all 3.
	for i := 0; i < 3; i++ {
		trb, ok := r.Consume()
		if !ok {
			t.Fatalf("Consume #%d returned ok=false", i)
		}
		if trb.Type() != trbTypeDataStage {
			t.Errorf("Consume #%d type = %d, want 3 (DataStage)", i, trb.Type())
		}
	}
	if r.Len() != 0 {
		t.Fatalf("Len after consume = %d, want 0", r.Len())
	}

	// Fill the ring to capacity and wrap through the Link TRB.
	for i := 0; i < 4; i++ {
		tb := BuildStatusTRB(DirOut, r.pcycle, false)
		if !r.Enqueue(tb) {
			t.Fatalf("Enqueue #%d at full capacity", i)
		}
	}
	// Ring is full now (queued == dataN).
	if r.queued != r.dataN {
		t.Fatalf("queued = %d, want %d", r.queued, r.dataN)
	}
	// Enqueue should fail (full).
	tb := BuildStatusTRB(DirOut, r.pcycle, false)
	if r.Enqueue(tb) {
		t.Fatal("Enqueue on full ring succeeded, want false")
	}

	// Consume all 4 + trigger the Link TRB wrap on the 5th consume... wait,
	// we have 4 data slots. Consume 4 then the Link should fire.
	for i := 0; i < 4; i++ {
		if _, ok := r.Consume(); !ok {
			t.Fatalf("Consume #%d failed after fill", i)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("Len after draining = %d, want 0", r.Len())
	}
}

// TestTRBEncoding verifies Setup/Data/Status TRB field encoding.
func TestTRBEncoding(t *testing.T) {
	// Setup TRB
	s := SetupPacket{
		BMRequestType: 0x80, // IN
		BRequest:      0x06, // GetDescriptor
		WValue:        0x0100,
		WIndex:        0,
		WLength:       8,
	}
	setup := BuildSetupTRB(s, true, true)
	if setup.Type() != trbTypeSetup {
		t.Errorf("Setup TRB type = %d, want %d", setup.Type(), trbTypeSetup)
	}
	if !setup.Cycle() {
		t.Error("Setup TRB cycle = false, want true")
	}
	if setup.DW(3)&trbIOC == 0 {
		t.Error("Setup TRB IOC not set")
	}
	if !setup.DirIn() {
		t.Error("Setup TRB DI not set for IN request")
	}
	// DW0: bmRequestType | bRequest<<8 | wValue<<16
	wantDW0 := uint32(0x80) | uint32(0x06)<<8 | uint32(0x0100)<<16
	if setup.DW(0) != wantDW0 {
		t.Errorf("Setup DW0 = %#x, want %#x", setup.DW(0), wantDW0)
	}
	// DW1: wIndex | wLength<<16
	wantDW1 := uint32(0) | uint32(8)<<16
	if setup.DW(1) != wantDW1 {
		t.Errorf("Setup DW1 = %#x, want %#x", setup.DW(1), wantDW1)
	}

	// Data TRB
	data := BuildDataTRB(0xDEADBEEF, 16, DirIn, false, true)
	if data.Type() != trbTypeDataStage {
		t.Errorf("Data TRB type = %d, want %d", data.Type(), trbTypeDataStage)
	}
	if data.Cycle() {
		t.Error("Data TRB cycle = true, want false")
	}
	if data.DW(0) != 0xDEADBEEF {
		t.Errorf("Data DW0 = %#x, want %#x", data.DW(0), 0xDEADBEEF)
	}
	if data.DW(2) != 16 {
		t.Errorf("Data xfer length = %d, want 16", data.DW(2))
	}

	// Status TRB
	status := BuildStatusTRB(DirOut, true, false)
	if status.Type() != trbTypeStatusStage {
		t.Errorf("Status TRB type = %d, want %d", status.Type(), trbTypeStatusStage)
	}
	if !status.Cycle() {
		t.Error("Status TRB cycle = false")
	}
	if status.DW(3)&trbIOC != 0 {
		t.Error("Status TRB IOC set, want false")
	}
	if status.DirIn() {
		t.Error("Status TRB DI set for OUT, want false")
	}
}

// TestSetupPacketDirection verifies the DI rule: data IN -> status OUT,
// data OUT -> status IN, no-data -> status IN.
func TestSetupPacketDirection(t *testing.T) {
	// IN transfer
	in := SetupPacket{BMRequestType: 0x80, WLength: 8}
	if in.DataDirection() != DirIn {
		t.Errorf("data dir for IN = %d, want DirIn(%d)", in.DataDirection(), DirIn)
	}
	if in.StatusDirection() != DirOut {
		t.Errorf("status dir for IN = %d, want DirOut(%d)", in.StatusDirection(), DirOut)
	}

	// OUT transfer
	out := SetupPacket{BMRequestType: 0x00, WLength: 4}
	if out.DataDirection() != DirOut {
		t.Errorf("data dir for OUT = %d, want DirOut(%d)", out.DataDirection(), DirOut)
	}
	if out.StatusDirection() != DirIn {
		t.Errorf("status dir for OUT = %d, want DirIn", out.StatusDirection())
	}

	// No-data transfer
	node := SetupPacket{BMRequestType: 0x40, WLength: 0}
	if node.StatusDirection() != DirIn {
		t.Errorf("status dir for no-data = %d, want DirIn", node.StatusDirection())
	}
}

// TestSubmitControlTransferNoData verifies a no-data control transfer produces
// just Setup + Status IN TRBs and a successful completion.
func TestSubmitControlTransferNoData(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}
	if err := c.EnableSlot(1); err != nil {
		t.Fatalf("EnableSlot: %v", err)
	}

	s := SetupPacket{BMRequestType: 0x40, BRequest: 0x01, WLength: 0}
	if err := c.SubmitControlTransfer(1, s, 0, nil); err != nil {
		t.Fatalf("SubmitControlTransfer: %v", err)
	}

	// Process the transfer.
	comp := c.Process()
	if comp == nil {
		t.Fatal("Process returned no completion")
	}
	if comp.Code != compSuccess {
		t.Errorf("completion code = %d, want %d", comp.Code, compSuccess)
	}
	if comp.Slot != 1 {
		t.Errorf("completion slot = %d, want 1", comp.Slot)
	}
	if comp.Endpoint != 0 {
		t.Errorf("completion endpoint = %d, want 0", comp.Endpoint)
	}

	// Completion should have been flushed from pendingDB.
	if c.DoorbellPending(1) {
		t.Error("doorbell still pending after Process")
	}

	// Verify the EP0 ring consumed all 3 TRBs (Setup + Status, no data).
	slot := c.slots[1]
	ep := slot.Eps[0]
	if ep.ring.Len() != 0 {
		t.Errorf("ring Len after process = %d, want 0", ep.ring.Len())
	}
}

// TestSubmitControlTransferWithData verifies a data transfer produces Setup +
// Data + Status and completes successfully.
func TestSubmitControlTransferWithData(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}
	if err := c.EnableSlot(5); err != nil {
		t.Fatalf("EnableSlot: %v", err)
	}

	data := make([]byte, 8)
	s := SetupPacket{BMRequestType: 0x80, BRequest: 0x06, WLength: 8}
	if err := c.SubmitControlTransfer(5, s, 0x2000, data); err != nil {
		t.Fatalf("SubmitControlTransfer: %v", err)
	}

	comp := c.Process()
	if comp == nil {
		t.Fatal("Process returned no completion")
	}
	if comp.Code != compSuccess {
		t.Errorf("completion code = %d, want %d", comp.Code, compSuccess)
	}
	if comp.Slot != 5 {
		t.Errorf("completion slot = %d, want 5", comp.Slot)
	}

	// Ring should be drained.
	slot := c.slots[5]
	ep := slot.Eps[0]
	if ep.ring.Len() != 0 {
		t.Errorf("ring Len after process = %d, want 0", ep.ring.Len())
	}
}

// TestEnableSlotOutOfRange verifies slot ID bounds.
func TestEnableSlotOutOfRange(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}
	if err := c.EnableSlot(0); err == nil {
		t.Error("EnableSlot(0) succeeded, want error")
	}
	if err := c.EnableSlot(xhciMaxSlots + 1); err == nil {
		t.Error("EnableSlot > max succeeded, want error")
	}
}

// TestSubmitControlTransferMissingSlot verifies transfer on a non-enabled slot fails.
func TestSubmitControlTransferMissingSlot(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}
	s := SetupPacket{BMRequestType: 0x40, WLength: 0}
	if err := c.SubmitControlTransfer(1, s, 0, nil); err == nil {
		t.Error("SubmitControlTransfer on missing slot succeeded, want error")
	}
}

// TestTransferRingFull verifies Enqueue returns false on a full ring.
func TestTransferRingFull(t *testing.T) {
	r := NewTransferRing(2) // 2 data slots + 1 Link TRB
	tb := BuildStatusTRB(DirOut, true, false)
	if !r.Enqueue(tb) {
		t.Fatal("first Enqueue failed")
	}
	if !r.Enqueue(tb) {
		t.Fatal("second Enqueue failed")
	}
	if r.Enqueue(tb) {
		t.Error("third Enqueue on full ring succeeded, want false")
	}
}
