// bt: HCI command layer (AGANTS.md Phase 12).
//
// Controller wraps the H4 UART with HCI command/response semantics:
// send a command, block (cooperatively) for its Command Complete. The
// same code drives the real wasip1 inputUART and the MockUART in tests —
// the transport is abstracted by the UART interface in h4.go.
package main

import (
	"errors"
	"fmt"
	"time"

	lib "kernel.lane/guests/lib"
)

// HCI opcodes (little-endian on the wire; value here is the 16-bit opcode).
// OGF=0x03 (Host Baseband): Reset, OGF=0x06 (Information): Read_BD_ADDR,
// OGF=0x08 (LE): scan/connect. Opcode(word) = (OGF<<10) | OCF.
const (
	HciOpReset            = 0x0C03 // OGF=3 OCF=3
	HciOpReadBdAddr       = 0x1801 // OGF=6 OCF=1
	HciLeSetScanParams    = 0x200B // OGF=8 OCF=0x0B
	HciLeSetScanEnable    = 0x200C // OGF=8 OCF=0x0C
	HciLeCreateConn       = 0x200D // OGF=8 OCF=0x0D
	HciLeCreateConnCancel = 0x200E
)

// HCI event codes (Bluetooth Core Vol 4, Part E, §7.7).
const (
	EvtCmdComplete uint8 = 0x0E // Command Complete
	EvtCmdStatus   uint8 = 0x0F // Command Status (async command accepted)
	EvtLeMeta      uint8 = 0x3E // LE Meta Event
)

// LE Meta subevent codes.
const (
	LeMetaConnComplete uint8 = 0x01
	LeMetaAdvReport    uint8 = 0x02
	LeMetaScanTimeout  uint8 = 0x07
)

// AdvAddressType is the 1-byte address type in a legacy advertising report.
const (
	AdvAddrPublic   byte = 0x00
	AdvAddrRandom   byte = 0x01
	AdvAddrNoAddress byte = 0xFF
)

// HCI status: 0 = success (core spec).
const hciStatusOK byte = 0

// AdvReport is one decoded LE advertising report.
type AdvReport struct {
	AddrType byte    // adv packet type / address type
	Addr     [6]byte // BD_ADDR / random address
	RSSI     int8    // received signal strength (2's complement)
	Data     []byte  // advertising data (may be empty)
}

// HCIConnection is a completed LE connection.
type HCIConnection struct {
	Handle   uint16
	Role     byte   // 0=central, 1=peripheral
	PeerAddr [6]byte
	Interval uint16
	Latency  uint16
	Timeout  uint16
}

// Controller is the HCI command/response surface over a UART.
type Controller interface {
	// Reset issues HCI_RESET and waits for Command Complete.
	Reset() error
	// BDAddr reads the local controller BD_ADDR.
	BDAddr() ([6]byte, error)
	// SetMTU updates the negotiated ATT MTU ceiling (used by the ATT layer).
	SetMTU(mtu uint16)
	// MTU returns the current ATT MTU ceiling.
	MTU() uint16
	// Scan issues LE scan and returns advertising reports observed.
	Scan(timeout time.Duration) ([]AdvReport, error)
	// Connect initiates an LE connection to addr and returns it.
	Connect(addr [6]byte, timeout time.Duration) (*HCIConnection, error)
	// SendACL emits an ACL data frame (L2CAP/ATT payload) for conn.
	SendACL(conn uint16, payload []byte)
	// RecvACL reads one ACL data frame from the controller.
	RecvACL() (*ACLData, error)
}

// hciController is the reference Controller backed by a UART.
type hciController struct {
	u   UART
	mtu uint16 // negotiated ATT MTU (default 23 until ExchangeMTU)
}

// NewController returns a Controller over u.
func NewController(u UART) Controller {
	return &hciController{u: u, mtu: 23}
}

func (c *hciController) SetMTU(mtu uint16) {
	if mtu >= 23 {
		c.mtu = mtu
	}
}

func (c *hciController) MTU() uint16 { return c.mtu }

// SendACL emits an ACL data frame carrying an L2CAP/ATT payload.
func (c *hciController) SendACL(conn uint16, payload []byte) {
	c.u.WriteBytes(frameACL(conn, payload))
}

// RecvACL reads one ACL data frame (blocking on UART availability).
func (c *hciController) RecvACL() (*ACLData, error) {
	return readACL(c.u)
}

// commandFull sends an HCI command and waits for its Command Complete,
// returning the status byte and the full return-parameter tail (everything
// after num_hci_command_pkts + opcode + status).
func (c *hciController) commandFull(opcode uint16, params []byte) (byte, []byte, error) {
	c.u.WriteBytes(frameHCICommand(opcode, params))
	for {
		ev, err := readHCIEvent(c.u)
		if err != nil {
			return hciStatusOK, nil, err
		}
		if ev.Code != EvtCmdComplete {
			continue
		}
		if len(ev.Params) < 4 {
			return hciStatusOK, nil, ErrTruncated
		}
		if lib.Get16(ev.Params[1:3]) != opcode {
			continue // another command's completion; keep draining
		}
		st := ev.Params[3]
		ret := ev.Params[4:] // return parameters
		return st, ret, nil
	}
}

// Command sends an HCI command and waits for Command Complete, returning
// only the status byte (async Command Status is tolerated then skipped).
func (c *hciController) Command(opcode uint16, params []byte) (byte, error) {
	st, _, err := c.commandFull(opcode, params)
	return st, err
}

// Reset issues HCI_RESET and waits for completion.
func (c *hciController) Reset() error {
	st, err := c.Command(HciOpReset, nil)
	if err != nil {
		return err
	}
	if st != hciStatusOK {
		return fmt.Errorf("bt: reset status=%d", st)
	}
	return nil
}

// BDAddr issues HCI_READ_BD_ADDR and returns the address.
func (c *hciController) BDAddr() ([6]byte, error) {
	st, ret, err := c.commandFull(HciOpReadBdAddr, nil)
	if err != nil {
		return [6]byte{}, err
	}
	if st != hciStatusOK {
		return [6]byte{}, fmt.Errorf("bt: read bd_addr status=%d", st)
	}
	if len(ret) < 6 {
		return [6]byte{}, ErrTruncated
	}
	var a [6]byte
	copy(a[:], ret[:6])
	return a, nil
}

// Scan issues LE Set Scan Parameters + LE Set Scan Enable, then collects
// advertising reports until a scan-timeout event (or the deadline). The
// scan-enable commands are synchronous (Command Complete); reports and the
// scan-timeout arrive as LE Meta subevents.
func (c *hciController) Scan(timeout time.Duration) ([]AdvReport, error) {
	// Active scanning on the random/address slot.
	var scanParams [7]byte
	scanParams[0] = 0x01                 // scan type = active
	lib.Put16(scanParams[1:3], 0x0010)   // scan interval
	lib.Put16(scanParams[3:5], 0x0010)   // scan window
	scanParams[5] = 0x01                 // scan address type: random
	scanParams[6] = 0x00                 // unused padding
	if st, err := c.Command(HciLeSetScanParams, scanParams[:]); err != nil {
		return nil, err
	} else if st != hciStatusOK {
		return nil, fmt.Errorf("bt: le_set_scan_params status=%d", st)
	}
	var enableParams [2]byte
	enableParams[0] = 0x01 // enable scanning
	enableParams[1] = 0x00 // don't filter duplicates
	if st, err := c.Command(HciLeSetScanEnable, enableParams[:]); err != nil {
		return nil, err
	} else if st != hciStatusOK {
		return nil, fmt.Errorf("bt: le_set_scan_enable status=%d", st)
	}

	deadline := time.Now().Add(timeout)
	var reports []AdvReport
	for time.Now().Before(deadline) {
		ev, err := readHCIEvent(c.u)
		if err != nil {
			return reports, nil // nothing buffered this quantum
		}
		if ev.Code != EvtLeMeta || len(ev.Params) < 1 {
			continue
		}
		switch ev.Params[0] {
		case LeMetaAdvReport:
			if r, ok := parseAdvReports(ev.Params[1:]); ok {
				reports = append(reports, r...)
			}
		case LeMetaScanTimeout:
			return reports, nil
		}
	}
	return reports, nil
}

// parseAdvReports decodes the LE Advertising Report subevent body:
// num_reports(1) + per-report {evt_type(1), addr_type(1), addr(6),
// data_len(1), data[data_len], rssi(1)}.
func parseAdvReports(body []byte) ([]AdvReport, bool) {
	if len(body) < 1 {
		return nil, false
	}
	n := int(body[0])
	body = body[1:]
	out := make([]AdvReport, 0, n)
	for i := 0; i < n; i++ {
		if len(body) < 9 {
			return out, false
		}
		r := AdvReport{AddrType: body[1]}
		copy(r.Addr[:], body[2:8])
		dlen := int(body[8])
		if len(body) < 9+dlen+1 {
			return out, false
		}
		r.Data = append([]byte(nil), body[9:9+dlen]...)
		r.RSSI = int8(body[9+dlen])
		body = body[9+dlen+1:]
		out = append(out, r)
	}
	return out, true
}

// Connect issues LE_CREATE_CONNECTION for addr and waits for the LE
// Connection Complete subevent, returning the connection. LE Create
// Connection is async: the controller first emits a Command Status
// (status 0 = pending), then, when linked, a LE Connection Complete.
func (c *hciController) Connect(addr [6]byte, timeout time.Duration) (*HCIConnection, error) {
	params := make([]byte, 0, 18)
	var two [2]byte
	lib.Put16(two[:], 0x0020) // scan interval
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x0020) // scan window
	params = append(params, two[0], two[1])
	params = append(params, AdvAddrRandom) // peer address type
	params = append(params, addr[:]...)
	lib.Put16(two[:], 0x0018) // conn interval min
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x0028) // conn interval max
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x0000) // latency
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x01F4) // supervision timeout
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x0018) // min ce
	params = append(params, two[0], two[1])
	lib.Put16(two[:], 0x0024) // timeout ce
	params = append(params, two[0], two[1])

	c.u.WriteBytes(frameHCICommand(HciLeCreateConn, params))

	// 1. Expect a Command Status for this opcode (async command accepted).
	deadline := time.Now().Add(timeout)
	accepted := false
	for time.Now().Before(deadline) && !accepted {
		ev, err := readHCIEvent(c.u)
		if err != nil {
			return nil, errors.New("bt: connection: command status not observed")
		}
		if ev.Code == EvtCmdStatus && len(ev.Params) >= 4 &&
			lib.Get16(ev.Params[1:3]) == HciLeCreateConn {
			if ev.Params[3] != hciStatusOK {
				return nil, fmt.Errorf("bt: le_create_conn command-status=%d", ev.Params[3])
			}
			accepted = true
			break
		}
	}
	if !accepted {
		return nil, errors.New("bt: connection: command status timeout")
	}

	// 2. Wait for the LE Connection Complete subevent.
	for time.Now().Before(deadline) {
		ev, err := readHCIEvent(c.u)
		if err != nil {
			return nil, errors.New("bt: connection: complete event lost")
		}
		if ev.Code == EvtLeMeta && len(ev.Params) >= 1 && ev.Params[0] == LeMetaConnComplete {
			conn, ok := parseConnComplete(ev.Params[1:])
			if !ok {
				return nil, ErrTruncated
			}
			return conn, nil
		}
	}
	return nil, errors.New("bt: connection complete timeout")
}

// parseConnComplete decodes the LE Connection Complete subevent body:
// status(1), conn_handle(2), role(1), peer_addr_type(1), peer_addr(6),
// conn_interval(2), conn_latency(2), supervision_timeout(2), ...
func parseConnComplete(b []byte) (*HCIConnection, bool) {
	if len(b) < 17 {
		return nil, false
	}
	if b[0] != hciStatusOK {
		return nil, false
	}
	conn := &HCIConnection{
		Handle:   lib.Get16(b[1:3]),
		Role:     b[3],
		Interval: lib.Get16(b[11:13]),
		Latency:  lib.Get16(b[13:15]),
		Timeout:  lib.Get16(b[15:17]),
	}
	copy(conn.PeerAddr[:], b[5:11]) // b[4] is peer_addr_type
	return conn, true
}
