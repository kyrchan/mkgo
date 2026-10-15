package main

import (
	"testing"
)

// TestResetHaltsAndClearsHCRST verifies the reset sequence: Run is cleared,
// HCHalted sets, HCRST self-clears, and the controller becomes ready.
func TestResetHaltsAndClearsHCRST(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}

	if err := c.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// After reset: Run=0, HCRST=0, HCHalted=1.
	cmd := c.rdOp32(xhciUsbCmd)
	if cmd&xhciCmdRun != 0 {
		t.Errorf("USBCMD.Run = %d, want 0", cmd&xhciCmdRun)
	}
	if cmd&xhciCmdHcrst != 0 {
		t.Errorf("USBCMD.HCRST = %d, want 0 (self-clearing)", cmd&xhciCmdHcrst)
	}
	sts := c.rdOp32(xhciUsbSts)
	if sts&xhciStsHalted == 0 {
		t.Errorf("USBSTS.HCHalted = 0, want 1 after stop")
	}
	if sts&xhciStsCnr != 0 {
		t.Errorf("USBSTS.CNR = 1, want 0 (ready)")
	}
}

// TestPortStatusDecoding reads a PORTSC with a device connected and checks
// every decoded bit.
func TestPortStatusDecoding(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}

	// Port 1: connected (CCS), powered (PP), not enabled, no over-current.
	// Write through the controller's operational view (base + portSc).
	bar.wr32(c.base+c.portSc(1), xhciPortScCcs|xhciPortScPp)

	st, err := c.PortStatus(1)
	if err != nil {
		t.Fatalf("PortStatus(1): %v", err)
	}
	if !st.ConnectStatus {
		t.Error("ConnectStatus = false, want true (CCS set)")
	}
	if !st.Powered {
		t.Error("Powered = false, want true (PP set)")
	}
	if st.Enabled {
		t.Error("Enabled = true, want false")
	}
	if st.OverCurrent {
		t.Error("OverCurrent = true, want false")
	}
	if st.Raw != xhciPortScCcs|xhciPortScPp {
		t.Errorf("Raw = %#x, want %#x", st.Raw, xhciPortScCcs|xhciPortScPp)
	}
}

// TestPortEnablePowersOnAndRejectsOverCurrent verifies PortEnable sets PP and
// refuses an over-current port.
func TestPortEnablePowersOnAndRejectsOverCurrent(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}

	// Port 2: normal (no over-current).
	if err := c.PortEnable(2); err != nil {
		t.Fatalf("PortEnable(2): %v", err)
	}
	st, err := c.PortStatus(2)
	if err != nil {
		t.Fatalf("PortStatus(2): %v", err)
	}
	if !st.Powered {
		t.Error("port 2 Powered = false, want true after PortEnable")
	}

	// Port 3: over-current → must be refused.
	bar.wr32(c.base+c.portSc(3), xhciPortScOca)
	if err := c.PortEnable(3); err == nil {
		t.Error("PortEnable(3) succeeded, want over-current error")
	}
}

// TestPortOutOfRange verifies 1-based port bounds checking.
func TestPortOutOfRange(t *testing.T) {
	bar := NewMockBAR()
	c, err := NewUsbController(bar)
	if err != nil {
		t.Fatalf("NewUsbController: %v", err)
	}

	if _, err := c.PortStatus(0); err == nil {
		t.Error("PortStatus(0) succeeded, want range error")
	}
	if _, err := c.PortStatus(c.maxPorts + 1); err == nil {
		t.Errorf("PortStatus(%d) succeeded, want range error", c.maxPorts+1)
	}
	if err := c.PortEnable(c.maxPorts + 1); err == nil {
		t.Errorf("PortEnable(%d) succeeded, want range error", c.maxPorts+1)
	}
}
