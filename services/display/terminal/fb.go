package terminal

// Framebuffer window client — abi/ABI.md v1.2 §9.FB:
//
//	0x00 u32 magic 'FBW'
//	0x04 u32 width    0x08 u32 height   0x0c u32 bpp (=32 XRGB)
//	0x10 u64 fb_off                     0x18 u32 caps
//	-- control mailbox (§3 request/completion shape, polled) --
//	0x20 u32 op   (1=SET_MODE 2=FLIP 3=UPDATE_RECT)
//	0x24 u32 next_req_id        0x28 u32 done_req_id    0x2c i32 status
//	0x30 u32 args[3]            (SET_MODE w,h,bpp | UPDATE_RECT x,y,w,h)
//
// Pixel data lives at fb_off; stride = width*4; format XRGB (byte order
// in memory: B,G,R,X on little-endian machines).
//
// v1 rules honored here: single-buffer default (FLIP tolerated as
// no-op), width==0 means "no display attached" and every op degrades to
// a silent no-op so callers never special-case headless boots.

import (
	"errors"
	"runtime"
	"sync"

	lib "kernel.lane/guests/lib"
)

const (
	FBWindowMin           = 0x40 + DefaultCols*GlyphW*DefaultRows*GlyphH*4
	fbMagic        uint32 = 0x57424642 // 'FBW' LE
	fbPixBase             = 0x40       // pixels start after mailbox args
	FBBpp                 = 32
	fbOpSetMode           = 1
	fbOpFlip              = 2
	fbOpUpdateRect        = 3

	FBCapDoubleBuffer uint32 = 1 << 0
	FBCapDamageRects  uint32 = 1 << 1
)

// ErrNoDisplay reports a missing/invalid framebuffer window.
var ErrNoDisplay = errors.New("display: bad framebuffer window")

var ErrFBWindow = ErrNoDisplay

// FBWindow drives one §9.FB window mapped at mem[0:].
type FBWindow struct {
	mem []byte
	mu  *sync.Mutex
	req uint32
}

func NewFBWindow(mem []byte) (*FBWindow, error) {
	if len(mem) < fbPixBase {
		return nil, ErrFBWindow
	}
	if lib.Get32(mem[0x00:]) != fbMagic {
		return nil, ErrFBWindow
	}
	return &FBWindow{mem: mem, mu: &sync.Mutex{}}, nil
}

// Attached reports whether a display is present (width != 0).
func (w *FBWindow) Attached() bool { return w.Width() != 0 }

func (w *FBWindow) Width() uint32  { return lib.Get32(w.mem[0x04:]) }
func (w *FBWindow) Height() uint32 { return lib.Get32(w.mem[0x08:]) }
func (w *FBWindow) Caps() uint32   { return lib.Get32(w.mem[0x18:]) }

// FBOffset returns the pixel-area window offset.
func (w *FBWindow) FBOffset() uint64 { return lib.Get64(w.mem[0x10:]) }

// Pixels exposes the framebuffer pixel bytes for direct rendering
// (wasm: own linear memory at fb_off; host harness: backing slice).
// Returns nil when no display is attached.
func (w *FBWindow) Pixels() []byte {
	if !w.Attached() {
		return nil
	}
	off := w.FBOffset()
	stride := uint64(w.Width()) * 4
	size := stride * uint64(w.Height())
	if int(off+size) > len(w.mem) {
		return nil
	}
	return w.mem[int(off):int(off+size)]
}

// request submits one mailbox op with three arg slots and polls done.
func (w *FBWindow) request(op uint32, args [3]uint32) int32 {
	w.mu.Lock()
	lib.Put32(w.mem[0x20:], op)
	lib.Put32(w.mem[0x30:], args[0])
	lib.Put32(w.mem[0x34:], args[1])
	lib.Put32(w.mem[0x38:], args[2])
	next := lib.Get32(w.mem[0x24:]) + 1
	lib.Put32(w.mem[0x24:], next)
	w.mu.Unlock()
	w.req = next

	for {
		runtime.Gosched()
		w.mu.Lock()
		done := lib.Get32(w.mem[0x28:])
		st := int32(lib.Get32(w.mem[0x2c:]))
		w.mu.Unlock()
		if done == w.req {
			return st
		}
	}
}

// SetMode requests a geometry change; no-op when headless.
func (w *FBWindow) SetMode(width, height uint32) int32 {
	if !w.Attached() {
		return 0
	}
	return w.request(fbOpSetMode, [3]uint32{width, height, FBBpp})
}

// Flip presents the back buffer (no-op unless double-buffered).
func (w *FBWindow) Flip() int32 {
	if !w.Attached() {
		return 0
	}
	return w.request(fbOpFlip, [3]uint32{})
}

// UpdateRect marks a damaged region (requires caps.bit1; harmless else).
func (w *FBWindow) UpdateRect(x, y, wd, ht uint32) int32 {
	if !w.Attached() {
		return 0
	}
	return w.request(fbOpUpdateRect, [3]uint32{x, y, wd})
}

// fbWindowMin is the smallest sane window: header + one default screen.
const fbWindowMin = 0x40 + DefaultCols*GlyphW*DefaultRows*GlyphH*4
