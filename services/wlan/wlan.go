package main

import (
	"errors"
	"io"
	"time"

	lib "kernel.lane/guests/lib"
)

// WLAN is the high-level wifi offload interface used by Run.
type WLAN interface {
	// Scan returns BSSs discovered from beacon frames.
	Scan() ([]BSS, error)
	// Associate attempts to join the network identified by SSID.
	Associate(ssid string) error
	// GetAddress returns the assigned IP/MAC (zero IP if not yet configured).
	GetAddress() (IP4, MAC)
}

// BSS is one discovered network.
type BSS struct {
	BSSID MAC
	SSID  string
	Chan  uint8
}

// wifiDriver drives the offload module via 802.11 management + 802.3 data
// frames. It owns the association state machine and DHCP.
type wifiDriver struct {
	off    OffloadTransport
	k      lib.Kernel
	w      io.Writer
	mac    MAC
	bss    []BSS
	assoc  *BSS
	lease  DhcpResult
}

// NewWifiDriver creates a driver over an OffloadTransport and kernel handle.
func newWifiDriver(off OffloadTransport, k lib.Kernel, w io.Writer, mac MAC) *wifiDriver {
	return &wifiDriver{
		off:   off,
		k:     k,
		w:     w,
		mac:   mac,
	}
}

// scan drains the transport and collects unique BSSes from beacon frames.
func (d *wifiDriver) Scan() ([]BSS, error) {
	d.bss = nil
	seen := make(map[string]bool)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f, ok := d.off.RecvFrame(); ok {
			if len(f) >= 1 && f[0] == FrameTypeMgmt {
				m, err := parseMgmt(f[1:])
				if err != nil {
					continue
				}
				if m.Subtype() == MgmtBeacon {
					ssid, ch, ok := parseBeacon(m.Body)
					if !ok {
						continue
					}
					key := m.Addr2.String()
					if !seen[key] {
						seen[key] = true
						d.bss = append(d.bss, BSS{
							BSSID: m.Addr2,
							SSID:  ssid,
							Chan:  ch,
						})
					}
				}
			}
		}
		d.k.Yield()
	}
	if len(d.bss) == 0 {
		return nil, errors.New("wlan: no beacons received")
	}
	return d.bss, nil
}

// Associate sends an association request for the given SSID and waits for
// a response.
func (d *wifiDriver) Associate(ssid string) error {
	for _, b := range d.bss {
		if b.SSID == ssid {
			d.assoc = &b
			break
		}
	}
	if d.assoc == nil {
		return errors.New("wlan: ssid not found in scan")
	}
	body := make([]byte, 4)
	body = append(body, EIE_SSID, byte(len(ssid)))
	body = append(body, ssid...)
	frame := d.buildMgmtFrame(d.assoc.BSSID, body)
	d.off.SendFrame(frame)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f, ok := d.off.RecvFrame(); ok {
			if len(f) >= 1 && f[0] == FrameTypeMgmt {
				m, err := parseMgmt(f[1:])
				if err != nil {
					continue
				}
				if m.Subtype() == MgmtAssocResp {
					status, _, ok := parseAssocResp(m.Body)
					if !ok {
						continue
					}
					if status == AssocSuccess {
						d.w.Write([]byte("[wlan] associated with " +
							d.assoc.BSSID.String() + "\n"))
						return nil
					}
					return errors.New("wlan: association rejected")
				}
			}
		}
		d.k.Yield()
	}
	return errors.New("wlan: association timeout")
}

// GetAddress returns the current assigned IP and MAC.
func (d *wifiDriver) GetAddress() (IP4, MAC) {
	return d.lease.IP, d.mac
}

// buildMgmtFrame constructs an 802.11 management frame: type tag + 24-byte
// header + body bytes.
func (d *wifiDriver) buildMgmtFrame(bssid MAC, body []byte) []byte {
	hdr := make([]byte, 24)
	lib.Put16(hdr[0:2], 0x0000) // FC: type=mgmt, subtype=assoc-req
	lib.Put16(hdr[2:4], 0)      // duration
	copy(hdr[4:10], bssid[:])   // DA = BSSID
	copy(hdr[10:16], d.mac[:])  // SA
	copy(hdr[16:22], bssid[:])  // Addr3 = BSSID
	out := make([]byte, 1, 1+len(hdr)+len(body))
	out[0] = FrameTypeMgmt
	out = append(out, hdr...)
	out = append(out, body...)
	return out
}

var _ WLAN = (*wifiDriver)(nil)