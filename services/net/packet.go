package main

// Packet windows (abi/ABI.md §6): two identically-shaped rings, RX and
// TX, each
//
//	u32 head (consumer idx)   u32 tail (producer idx)
//	N slots of {u32 len; u8 data[1526]}, N=256, slot stride 1536
//
// Producer writes slot at tail%N, bumps tail; consumer mirrors with head.
// The QEMU virtio-net shim drains TX / fills RX. This file defines the
// transport-neutral interfaces the stack codes against plus a byte-exact
// in-memory ring so tests (and the future adapter) exercise identical
// semantics.

import (
	"errors"
	"sync"

	lib "kernel.lane/guests/lib"
)

const (
	WinSlots      = 256
	SlotDataLen   = 1526
	SlotStride    = 1536 // u32 len + data, padded to stride
	RingHeaderLen = 8    // head u32 + tail u32
	RingSize      = RingHeaderLen + WinSlots*SlotStride
)

// MaxFrame is the largest frame that fits one slot.
const MaxFrame = SlotDataLen

// PacketSource yields received frames (non-blocking).
type PacketSource interface {
	// Recv returns the next queued frame, or ok=false when empty.
	Recv() (frame []byte, ok bool)
}

// PacketSink accepts outgoing frames.
type PacketSink interface {
	// Send queues a frame; ok=false means the ring was full and the
	// frame was dropped (v1 policy: no backpressure over windows).
	Send(frame []byte) bool
}

// PacketFeed is a bidirectional wire a Stack attaches to.
type PacketFeed interface {
	PacketSource
	PacketSink
}

var (
	_ PacketSource = (*WindowRing)(nil)
	_ PacketSink   = (*WindowRing)(nil)
)

// WindowRing drives one §6 window mapped at mem[0:]. On wasm, mem is the
// session's linear memory slice at the devman-reported window offset; on
// host it is any backing buffer of RingSize bytes. Byte layout is exactly
// the ABI's so the same logic serves both sides.
type WindowRing struct{ mem []byte }

func NewWindowRing(mem []byte) (*WindowRing, error) {
	if len(mem) < RingSize {
		return nil, errors.New("net: window too small")
	}
	return &WindowRing{mem: mem}, nil
}

func (w *WindowRing) head() uint32     { return lib.Get32(w.mem[0:4]) }
func (w *WindowRing) tail() uint32     { return lib.Get32(w.mem[4:8]) }
func (w *WindowRing) setHead(v uint32) { lib.Put32(w.mem[0:4], v) }
func (w *WindowRing) setTail(v uint32) { lib.Put32(w.mem[4:8], v) }
func (w *WindowRing) slotBase(n uint32) int {
	return RingHeaderLen + int(n%WinSlots)*SlotStride
}

// Recv implements PacketSource (consumer side: advances head).
func (w *WindowRing) Recv() ([]byte, bool) {
	h, t := w.head(), w.tail()
	if h == t {
		return nil, false
	}
	base := w.slotBase(h)
	n := int(lib.Get32(w.mem[base:]))
	if n > SlotDataLen {
		n = SlotDataLen // corrupt slot: clamp, never overflow
	}
	out := make([]byte, n)
	copy(out, w.mem[base+4:base+4+n])
	w.setHead(h + 1)
	return out, true
}

// Send implements PacketSink (producer side: advances tail). Full-ring
// sends are dropped with ok=false — no-backpressure v1.
func (w *WindowRing) Send(frame []byte) bool {
	if len(frame) == 0 || len(frame) > SlotDataLen {
		return false
	}
	h, t := w.head(), w.tail()
	if t-h >= WinSlots {
		return false
	}
	base := w.slotBase(t)
	lib.Put32(w.mem[base:], uint32(len(frame)))
	copy(w.mem[base+4:base+4+len(frame)], frame)
	w.setTail(t + 1)
	return true
}

// ---- loopback transports for tests and in-process wiring ----

// Wire is an unbounded in-memory PacketFeed shared by endpoints of one
// virtual segment. Safe for concurrent use (-race clean).
type Wire struct {
	mu     sync.Mutex
	frames [][]byte
}

func NewWire() *Wire { return &Wire{} }

func (w *Wire) Send(frame []byte) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, len(frame))
	copy(cp, frame)
	w.frames = append(w.frames, cp)
	return true
}

func (w *Wire) Recv() ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.frames) == 0 {
		return nil, false
	}
	f := w.frames[0]
	w.frames = w.frames[1:]
	return f, true
}

// Segment models a shared ethernet segment between two named ports:
// frames sent on either port are delivered to BOTH ports' receive sides,
// like a real bus (stacks filter foreign destinations themselves).
type Segment struct {
	mu    sync.Mutex
	next  int
	ports []*SegmentPort
}

// SegmentPort is one endpoint's feed on a Segment.
type SegmentPort struct {
	seg   *Segment
	mu    sync.Mutex
	inbox [][]byte
}

func NewSegment() *Segment { return &Segment{} }

func (s *Segment) Attach() *SegmentPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &SegmentPort{seg: s}
	s.ports = append(s.ports, p)
	return p
}

func (p *SegmentPort) Send(frame []byte) bool {
	p.seg.mu.Lock()
	peers := make([]*SegmentPort, len(p.seg.ports))
	copy(peers, p.seg.ports)
	p.seg.mu.Unlock()
	for _, q := range peers {
		q.deliver(frame)
	}
	return true
}

func (p *SegmentPort) deliver(frame []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(frame))
	copy(cp, frame)
	p.inbox = append(p.inbox, cp)
}

func (p *SegmentPort) Recv() ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.inbox) == 0 {
		return nil, false
	}
	f := p.inbox[0]
	p.inbox = p.inbox[1:]
	return f, true
}

var (
	_ PacketFeed = (*SegmentPort)(nil)
	_ PacketFeed = (*Wire)(nil)
)
