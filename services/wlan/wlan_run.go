package main

import (
	"errors"
	"io"
	"time"

	lib "kernel.lane/guests/lib"
)

// wlanTargetSSID is the network to associate with (overridable via argv[1]
// in the wasip1 entry point).
const wlanTargetSSID = "testnet"

// Run is the shared orchestration entry point: bind net, scan, associate,
// DHCP, then bridge traffic. Host-testable: pass any OffloadTransport +
// Kernel (lib.Bus suffices). Returns a process exit code.
func Run(k lib.Kernel, off OffloadTransport, w io.Writer, ssid string) int {
	mac := defaultMAC()

	w.Write([]byte("[wlan] up\n"))

	nc, err := lib.BindNet(k, "wlan")
	if err != nil {
		w.Write([]byte("[wlan] bindnet: " + err.Error() + "\n"))
		return 1
	}
	nc.SetBudget(50000)

	d := newWifiDriver(off, k, w, mac)

	if _, err := d.Scan(); err != nil {
		w.Write([]byte("[wlan] scan: " + err.Error() + "\n"))
		return 1
	}
	w.Write([]byte("[wlan] found " + itoa(len(d.bss)) + " bss\n"))

	target := ssid
	if target == "" {
		target = wlanTargetSSID
	}
	if err := d.Associate(target); err != nil {
		w.Write([]byte("[wlan] assoc: " + err.Error() + "\n"))
		return 1
	}

	// DHCP.
	w.Write([]byte("[wlan] dhcp...\n"))
	dhcp := newDhcpClient(off, mac)
	lease, err := dhcp.Run()
	if err != nil {
		w.Write([]byte("[wlan] dhcp: " + err.Error() + "\n"))
		return 1
	}
	d.lease = lease
	w.Write([]byte("[wlan] dhcp ok: " + lease.IP.String() + "\n"))

	// Bridge.
	apMAC := d.assoc.BSSID
	nb, err := newNetBridge(nc, off, mac, apMAC, lease.IP, lease.GW)
	if err != nil {
		w.Write([]byte("[wlan] bridge: " + err.Error() + "\n"))
		return 1
	}
	defer nb.Close()
	w.Write([]byte("[wlan] bridged\n"))

	// Main loop: forward packets until something fatal happens.
	deadline := time.Now().Add(time.Minute)
	for {
		if time.Now().After(deadline) {
			w.Write([]byte("[wlan] timeout, exiting\n"))
			return 0
		}
		nb.ForwardOnce()
		k.Yield()
	}
}

// defaultMAC returns a deterministic MAC for wlan.wasm (admin-assigned).
func defaultMAC() MAC {
	return MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
}

// ErrNoNetService is returned when the net port cannot be bound.
var ErrNoNetService = errors.New("wlan: net service unavailable")
