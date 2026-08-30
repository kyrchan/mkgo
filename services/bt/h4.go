// bt: HCI transport layer (AGENTS.md Phase 12).
//
// H4 framing over the legacy UART shim (ABI §12: COM1 0x3F8 delivers bytes
// as input records). This file holds the transport-agnostic H4 codec and
// the UART abstraction so the HCI/ATT layers below can be host-tested
// against MockUART with an exact byte replay of a real controller capture.
package main

import (
	"errors"
	"sync"

	lib "kernel.lane/guests/lib"
)

// H4 packet types (HCI transport over UART, Bluetooth Core Vol 4: HCI
// Transport Protocols). The task names them CMD/ACL/DATA/EVENT; DATA is
// the legacy SCO/ISO transport slot.
const (
	H4Cmd  byte = 0x01 // HCI Command (host -> controller)
	H4Acl  byte = 0x02 // HCI ACL Data
	H4Evt  byte = 0x03 // HCI Event (controller -> host)
	H4Data byte = 0x04 // HCI SCO / ISO Data (framed payload)
)

// UART is the byte-level transport over the legacy UART shim. main.go
// (wasip1) binds it to kern.InputRecv + a UART-TX import; tests bind it
// to MockUART. All methods are non-blocking: Read/Poll never stall the
// session quantum.
type UART interface {
	// Poll reports whether at least one byte is waiting to Read.
	Poll() bool
	// Read returns one byte; ok is false when the receive queue is empty.
	Read() (byte, bool)
	// Write emits one byte toward the controller.
	Write(byte)
	// WriteBytes emits a run of bytes (the H4-encoded command/ACL frame).
	WriteBytes([]byte)
}

// HCICommand is a host->controller HCI command (fields after H4 type).
type HCICommand struct {
	Opcode uint16
	Params []byte
}

// HCIEvent is a controller->host HCI event (fields after H4 type).
type HCIEvent struct {
	Code   uint8
	Params []byte
}

// ACLData is one HCI ACL data frame (host<->controller or to a peer).
type ACLData struct {
	Handle uint16 // 12-bit connection handle
	PB     byte   // 2-bit Packet Boundary flag
	BC     byte   // 2-bit Broadcast flag
	Data   []byte // L2CAP/ATT payload
}

var (
	ErrUARTEOF   = errors.New("bt: uart eof")
	ErrTruncated = errors.New("bt: h4 frame truncated")
	ErrBadH4Type = errors.New("bt: unknown h4 packet type")
)

// --- low-level byte pulls off the UART (LE framing for HCI) ---

// readByte reads one byte; returns ErrUARTEOF if the queue is empty.
func readByte(u UART) (byte, error) {
	if !u.Poll() {
		return 0, ErrUARTEOF
	}
	b, ok := u.Read()
	if !ok {
		return 0, ErrUARTEOF
	}
	return b, nil
}

// readN pulls n bytes, returning ErrTruncated if short.
func readN(u UART, n int) ([]byte, error) {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		b, err := readByte(u)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// --- H4 frame readers (controller -> host side) ---

// readHCIEvent reads one H4 event packet (type must be H4Evt).
func readHCIEvent(u UART) (*HCIEvent, error) {
	t, err := readByte(u)
	if err != nil {
		return nil, err
	}
	if t != H4Evt {
		return nil, ErrBadH4Type
	}
	code, err := readByte(u)
	if err != nil {
		return nil, ErrTruncated
	}
	n, err := readByte(u)
	if err != nil {
		return nil, ErrTruncated
	}
	params, err := readN(u, int(n))
	if err != nil {
		return nil, ErrTruncated
	}
	return &HCIEvent{Code: code, Params: params}, nil
}

// readACL reads one H4 ACL data packet (type must be H4Acl).
func readACL(u UART) (*ACLData, error) {
	t, err := readByte(u)
	if err != nil {
		return nil, err
	}
	if t != H4Acl {
		return nil, ErrBadH4Type
	}
	h, err := readN(u, 2)
	if err != nil {
		return nil, ErrTruncated
	}
	word := lib.Get16(h)
	dlen, err := readN(u, 2)
	if err != nil {
		return nil, ErrTruncated
	}
	n := int(lib.Get16(dlen))
	data, err := readN(u, n)
	if err != nil {
		return nil, ErrTruncated
	}
	return &ACLData{
		Handle: word & 0x0FFF,
		PB:     byte((word >> 12) & 0x3),
		BC:     byte((word >> 14) & 0x3),
		Data:   data,
	}, nil
}

// --- H4 frame builders (host -> controller side) ---

// frameHCICommand wraps a command into an H4 byte stream.
func frameHCICommand(opcode uint16, params []byte) []byte {
	out := make([]byte, 0, 4+len(params))
	out = append(out, H4Cmd)
	out = append(out, byte(opcode), byte(opcode>>8))
	out = append(out, byte(len(params)))
	out = append(out, params...)
	return out
}

// frameHCIEvent wraps an HCI event into an H4 byte stream.
func frameHCIEvent(code uint8, params []byte) []byte {
	out := make([]byte, 0, 3+len(params))
	out = append(out, H4Evt)
	out = append(out, code)
	out = append(out, byte(len(params)))
	out = append(out, params...)
	return out
}

// frameACL wraps an ACL payload for connection handle with PB=first, BC=0.
func frameACL(handle uint16, payload []byte) []byte {
	word := (handle & 0x0FFF) | (1 << 12) // PB=01 (first/non-fragment), BC=00
	out := make([]byte, 0, 5+len(payload))
	out = append(out, H4Acl)
	var h [2]byte
	lib.Put16(h[:], word)
	out = append(out, h[0], h[1])
	var l [2]byte
	lib.Put16(l[:], uint16(len(payload)))
	out = append(out, l[0], l[1])
	out = append(out, payload...)
	return out
}

// MockUART is a deterministic byte-buffer UART for host tests. Feed()
// pre-loads received bytes; Read/Write operate on the shared, mutex-guarded
// buffer. Sent() returns the bytes emitted toward the controller.
type MockUART struct {
	mu  sync.Mutex
	rx  []byte
	tx  []byte
	pos int
}

// NewMockUART returns an empty mock UART.
func NewMockUART() *MockUART { return &MockUART{} }

// Feed pre-loads received bytes (test driver / capture replay).
func (m *MockUART) Feed(b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rx = append(m.rx, b...)
}

// Poll reports whether a byte is waiting to Read.
func (m *MockUART) Poll() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pos < len(m.rx)
}

// Read returns one buffered byte.
func (m *MockUART) Read() (byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pos >= len(m.rx) {
		return 0, false
	}
	b := m.rx[m.pos]
	m.pos++
	return b, true
}

// Write captures one byte toward the controller (TX sink for tests).
func (m *MockUART) Write(b byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tx = append(m.tx, b)
}

// WriteBytes captures a run of bytes toward the controller.
func (m *MockUART) WriteBytes(b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tx = append(m.tx, b...)
}

// Sent returns a copy of the bytes written so far (command assertions).
func (m *MockUART) Sent() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(m.tx))
	copy(cp, m.tx)
	return cp
}
