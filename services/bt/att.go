// bt: ATT / GATT client (AGANTS.md Phase 12).
//
// ATT runs inside HCI ACL data frames. v1 models a minimal fixed-L2CAP
// shim: the ACL data payload is the ATT PDU directly (no 4-byte L2CAP
// header), since the single-attached-offload model has no channel
// multiplexing yet. The same code drives the real controller and the
// MockUART test path.
package main

import (
	"errors"
	"fmt"

	lib "kernel.lane/guests/lib"
)

// ATT opcodes (Bluetooth Core Vol 3, Part F).
const (
	AttExchangeMtuReq       byte = 0x02
	AttExchangeMtuRsp       byte = 0x03
	AttFindInformationReq   byte = 0x04
	AttFindInformationRsp   byte = 0x05
	AttFindByTypeValReq     byte = 0x06
	AttFindByTypeValRsp     byte = 0x07
	AttReadByTypeReq        byte = 0x08
	AttReadByTypeRsp        byte = 0x09
	AttReadReq              byte = 0x0A
	AttReadRsp              byte = 0x0B
	AttReadBlobReq          byte = 0x0C
	AttReadBlobRsp          byte = 0x0D
	AttReadByGroupTypeReq   byte = 0x10
	AttReadByGroupTypeRsp   byte = 0x11
	AttWriteReq             byte = 0x12
	AttWriteRsp             byte = 0x13
	AttWriteNoResp          byte = 0x52 // Write Without Response (method 0x12 | cmd-flag 0x40)
)

// Well-known GATT attribute UUIDs (16-bit).
const (
	GattUuidPrimaryService   uint16 = 0x2800
	GattUuidCharacteristic   uint16 = 0x2803
	GattSvcGenericAccess     uint16 = 0x1800
	GattSvcBattery           uint16 = 0x180F
	GattCharBatteryLevel     uint16 = 0x2A19
)

// ATT status codes (subset).
const (
	attOk              byte = 0x00
	attAttrNotFound    byte = 0x0A
	attInsufficientLen byte = 0x07
)

// GattService is one primary service handle range.
type GattService struct {
	Start  uint16
	End    uint16
	UUID   uint16
}

// GattChar is one characteristic declaration.
type GattChar struct {
	DeclHandle uint16 // handle of the characteristic declaration
	ValueHandle uint16 // handle of the characteristic value
	Properties byte   // 8-bit property bitfield
	UUID       uint16
}

// ATTError is a protocol-level ATT failure (status code in the range
// 0x01..0xFF; 0x00 is success).
type ATTError struct {
	Code byte
	Msg  string
}

func (e *ATTError) Error() string { return fmt.Sprintf("bt: att %s (0x%02x)", e.Msg, e.Code) }

// ATTClient is the GATT client surface over an ACL connection.
type ATTClient interface {
	// ExchangeMTU negotiates the ATT MTU; returns the server's RX MTU.
	ExchangeMTU() (uint16, error)
	// DiscoverServices finds primary services matching uuid (all if 0).
	DiscoverServices(uuid uint16) ([]GattService, error)
	// DiscoverCharacteristics enumerates chars in [start,end].
	DiscoverCharacteristics(start, end uint16) ([]GattChar, error)
	// ReadCharacteristic reads a char's value by its value handle.
	ReadCharacteristic(handle uint16) ([]byte, error)
	// WriteCharacteristic writes data to a char (with response if
	// withoutResp is false).
	WriteCharacteristic(handle uint16, data []byte, withoutResp bool) error
}

// attClient implements ATTClient over a Controller + connection handle.
type attClient struct {
	c      Controller
	conn   uint16
	mtu    uint16
}

// NewATTClient returns an ATT client over an established connection.
// The MTU defaults to the controller's negotiated ceiling (23 unless
// ExchangeMTU raises it) until the first exchange.
func NewATTClient(c Controller, conn uint16) ATTClient {
	return &attClient{c: c, conn: conn, mtu: c.MTU()}
}

// mtuPayload is the max ATT payload given the negotiated MTU (MTU minus
// the 1-byte L2CAP/ATT overhead we model).
func (a *attClient) mtuPayload() int {
	if a.mtu < 23 {
		a.mtu = 23
	}
	return int(a.mtu - 1)
}

// sendATT wraps an ATT PDU in a minimal L2CAP header, sends it over ACL,
// and reads back the matching response PDU.
func (a *attClient) sendATT(req []byte) ([]byte, error) {
	if len(req) > a.mtuPayload() {
		return nil, fmt.Errorf("bt: att pdu %d > mtu payload %d", len(req), a.mtuPayload())
	}
	a.c.SendACL(a.conn, req)
	fr, err := a.c.RecvACL()
	if err != nil {
		return nil, err
	}
	return fr.Data, nil
}

// waitATT reads ACL frames until the opcode matches want (skipping non-ATT).
func (a *attClient) waitATT(want byte) ([]byte, error) {
	for {
		fr, err := a.c.RecvACL()
		if err != nil {
			return nil, err
		}
		if len(fr.Data) == 0 || fr.Data[0] != want {
			continue
		}
		return fr.Data, nil
	}
}

// ExchangeMTU negotiates MTU; returns the server's RX MTU.
func (a *attClient) ExchangeMTU() (uint16, error) {
	req := make([]byte, 3)
	req[0] = AttExchangeMtuReq
	lib.Put16(req[1:3], a.mtu) // client RX MTU (little-endian)
	resp, err := a.sendATT(req)
	if err != nil {
		return 0, err
	}
	if len(resp) < 3 || resp[0] != AttExchangeMtuRsp {
		return 0, errors.New("bt: att exchange mtu: bad reply")
	}
	srv := lib.Get16(resp[1:3])
	if srv < 23 {
		srv = 23
	}
	a.mtu = srv
	return srv, nil
}

// DiscoverServices finds primary services matching uuid (0 = all).
func (a *attClient) DiscoverServices(uuid uint16) ([]GattService, error) {
	// Read By Group Type Request: opcode(1) + start(2) + end(2) + uuid(2 or 16)
	// primary service (0x2800). Range covers the whole handle space.
	req := make([]byte, 7)
	req[0] = AttReadByGroupTypeReq
	lib.Put16(req[1:3], 0x0001) // start
	lib.Put16(req[3:5], 0xFFFF) // end
	lib.Put16(req[5:7], GattUuidPrimaryService)
	resp, err := a.sendATT(req)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 || resp[0] != AttReadByGroupTypeRsp {
		return nil, errors.New("bt: att discover services: bad reply")
	}
	// response: opcode(1) + attr_len(1) + entries
	alen := int(resp[1])
	body := resp[2:]
	if alen < 6 || len(body) < alen {
		return nil, ErrTruncated
	}
	var out []GattService
	for len(body) >= alen {
		svc := GattService{
			Start: lib.Get16(body[0:2]),
			End:   lib.Get16(body[2:4]),
			UUID:  lib.Get16(body[4:6]),
		}
		out = append(out, svc)
		body = body[alen:]
		if uuid != 0 && svc.UUID != uuid {
			// filter: drop non-matching, keep scanning
		}
	}
	if uuid != 0 {
		filtered := out[:0]
		for _, s := range out {
			if s.UUID == uuid {
				filtered = append(filtered, s)
			}
		}
		out = filtered
	}
	return out, nil
}

// DiscoverCharacteristics enumerates chars in [start,end] (Read By Type
// for UUID 0x2803 characteristic declaration).
func (a *attClient) DiscoverCharacteristics(start, end uint16) ([]GattChar, error) {
	req := make([]byte, 7)
	req[0] = AttReadByTypeReq
	lib.Put16(req[1:3], start)
	lib.Put16(req[3:5], end)
	lib.Put16(req[5:7], GattUuidCharacteristic)
	resp, err := a.sendATT(req)
	if err != nil {
		return nil, err
	}
	if len(resp) < 2 || resp[0] != AttReadByTypeRsp {
		return nil, errors.New("bt: att discover chars: bad reply")
	}
	alen := int(resp[1])
	body := resp[2:]
	if alen < 7 {
		return nil, ErrTruncated
	}
	var out []GattChar
	for len(body) >= alen {
		ch := GattChar{
			DeclHandle: lib.Get16(body[0:2]),
			Properties: body[2],
			ValueHandle: lib.Get16(body[3:5]),
			UUID:       lib.Get16(body[5:7]),
		}
		out = append(out, ch)
		body = body[alen:]
	}
	return out, nil
}

// ReadCharacteristic reads a char value by its value handle.
func (a *attClient) ReadCharacteristic(handle uint16) ([]byte, error) {
	req := make([]byte, 5)
	req[0] = AttReadReq
	lib.Put16(req[1:3], handle) // little-endian
	lib.Put16(req[3:5], 0)      // offset 0
	resp, err := a.sendATT(req)
	if err != nil {
		return nil, err
	}
	if len(resp) < 1 {
		return nil, ErrTruncated
	}
	if resp[0] == 0x01 { // Error Response
		if len(resp) < 5 {
			return nil, errors.New("bt: att error reply truncated")
		}
		return nil, &ATTError{Code: resp[4], Msg: "read"}
	}
	if resp[0] != AttReadRsp {
		return nil, fmt.Errorf("bt: att read: unexpected opcode 0x%02x", resp[0])
	}
	return resp[1:], nil
}

// WriteCharacteristic writes data (with or without response).
func (a *attClient) WriteCharacteristic(handle uint16, data []byte, withoutResp bool) error {
	if withoutResp {
		if len(data) > a.mtuPayload()-3 {
			return errors.New("bt: write-without-response: payload too big")
		}
		req := make([]byte, 0, 3+len(data))
		req = append(req, AttWriteNoResp)
		var h [2]byte
		lib.Put16(h[:], handle)
		req = append(req, h[0], h[1])
		req = append(req, data...)
		a.c.SendACL(a.conn, req)
		return nil
	}
	req := make([]byte, 0, 5+len(data))
	req = append(req, AttWriteReq)
	var h [2]byte
	lib.Put16(h[:], handle)
	req = append(req, h[0], h[1])
	req = append(req, data...)
	resp, err := a.sendATT(req)
	if err != nil {
		return err
	}
	if len(resp) < 1 || resp[0] != AttWriteRsp {
		return errors.New("bt: att write: bad reply")
	}
	return nil
}
