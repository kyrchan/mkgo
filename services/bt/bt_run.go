// bt: end-to-end LE flow orchestration (AGANTS.md Phase 12 gate).
//
// Run() is shared by main.go (wasip1, real inputUART) and bt_test.go (MockUART
// fed a controller capture), so the gate is identical under both. It drives:
// HCI_RESET -> READ_BD_ADDR -> LE_SCAN -> LE_CONNECT -> ATT_GATT_READ and
// reports milestones to w as "bt: <milestone> ok" lines.
package main

import (
	"fmt"
	"io"
	"time"
)

// scanTimeout is the LE scan window the host grants the controller.
const scanTimeout = 2 * time.Second

// connectTimeout bounds the LE connection-establishment wait.
const connectTimeout = 3 * time.Second

// Run drives one Bluetooth LE discovery+read cycle over u, writing gate
// milestones to w. Returns a non-zero exit code on a hard failure so the
// wasip1 entry point can propagate it.
func Run(u UART, w io.Writer) int {
	c := NewController(u)

	if err := c.Reset(); err != nil {
		fmt.Fprintln(w, "bt: reset failed:", err)
		return 1
	}

	addr, err := c.BDAddr()
	if err != nil {
		fmt.Fprintln(w, "bt: read bd_addr failed:", err)
		return 1
	}
	// BD_ADDR is 48-bit little-endian on the wire; the canonical MAC display
	// reverses the six bytes (LSB first -> MSB first).
	fmt.Fprintf(w, "bt: bd_addr %02x:%02x:%02x:%02x:%02x:%02x\n",
		addr[5], addr[4], addr[3], addr[2], addr[1], addr[0])

	reports, err := c.Scan(scanTimeout)
	if err != nil {
		fmt.Fprintln(w, "bt: scan failed:", err)
		return 1
	}
	if len(reports) == 0 {
		fmt.Fprintln(w, "bt: hci_le_scan: no reports")
		return 1
	}
	fmt.Fprintln(w, "bt: hci_le_scan ok")

	conn, err := c.Connect(reports[0].Addr, connectTimeout)
	if err != nil {
		fmt.Fprintln(w, "bt: connect failed:", err)
		return 1
	}

	ac := NewATTClient(c, conn.Handle)
	if _, err := ac.ExchangeMTU(); err != nil {
		fmt.Fprintln(w, "bt: exchange mtu failed:", err)
		return 1
	}
	svcs, err := ac.DiscoverServices(GattSvcBattery)
	if err != nil || len(svcs) == 0 {
		fmt.Fprintln(w, "bt: discover services failed:", err)
		return 1
	}
	chars, err := ac.DiscoverCharacteristics(svcs[0].Start, svcs[0].End)
	if err != nil || len(chars) == 0 {
		fmt.Fprintln(w, "bt: discover characteristics failed:", err)
		return 1
	}
	if _, err := ac.ReadCharacteristic(chars[0].ValueHandle); err != nil {
		fmt.Fprintln(w, "bt: gatt read failed:", err)
		return 1
	}
	fmt.Fprintln(w, "bt: gatt_read ok")

	fmt.Fprintf(w, "bt: connected handle=0x%04x\n", conn.Handle)
	return 0
}
