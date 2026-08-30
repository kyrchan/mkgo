package main

import (
	"sync"
)

// OffloadTransport is the frame-oriented half-duplex interface to the wifi
// offload module. Frames are 802.11 management or 802.3 data; the wire framing
// (length prefix) is handled by the transport. SendFrame/RecvFrame are
// non-blocking (mirroring the §6 window-drop policy in v1).
type OffloadTransport interface {
	// SendFrame queues an outbound frame; returns false if the transport
	// was full and the frame was dropped (v1: no backpressure).
	SendFrame([]byte) bool
	// RecvFrame returns the next queued inbound frame, ok=false if empty.
	RecvFrame() ([]byte, bool)
}

// OffloadFrameType enumerates the first-payload-byte type tags that select
// interpretation of the frame body.
const (
	FrameTypeMgmt = 0 // 802.11 management frame body follows
	FrameTypeData = 1 // 802.3 Ethernet frame body follows
)

// OffloadMaxPayload is the largest frame body the offload transport accepts
// (4-byte length prefix + 1281-byte payload = 1285-byte wire frame).
const OffloadMaxPayload = 1281

// MockTransport is an in-memory OffloadTransport for host testing. It records
// outbound frames for the test driver and lets the driver inject inbound frames.
type MockTransport struct {
	mu    sync.Mutex
	inbox [][]byte
	out   [][]byte
}

// NewMockTransport returns an empty transport.
func NewMockTransport() *MockTransport { return &MockTransport{} }

// SendFrame records the frame and returns true. The test driver reads queued
// frames via Outgoing.
func (m *MockTransport) SendFrame(f []byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(f) > OffloadMaxPayload {
		return false
	}
	cp := make([]byte, len(f))
	copy(cp, f)
	m.out = append(m.out, cp)
	return true
}

// RecvFrame returns the next driver-injected inbound frame.
func (m *MockTransport) RecvFrame() ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.inbox) == 0 {
		return nil, false
	}
	f := m.inbox[0]
	m.inbox = m.inbox[1:]
	return f, true
}

// Outgoing returns all frames sent to the transport since the last call (or
// since construction) and clears the outbound queue. The test driver uses
// this to inspect what the driver emitted.
func (m *MockTransport) Outgoing() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.out
	m.out = nil
	return out
}

// Inject pushes an inbound frame for the driver's next RecvFrame.
func (m *MockTransport) Inject(f []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(f))
	copy(cp, f)
	m.inbox = append(m.inbox, cp)
}

var _ OffloadTransport = (*MockTransport)(nil)
