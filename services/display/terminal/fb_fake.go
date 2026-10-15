package terminal

// FakeFB is the host-side §9.FB backend harness: completes mailbox
// requests exactly like the kernel shim would, backing pixels in an
// in-memory buffer. Mirrors the fs RamDisk pattern so `go test` drives
// identical window semantics to the wasm path.

import (
	"runtime"
	"sync"

	lib "kernel.lane/guests/lib"
)

type FakeFB struct {
	Mu    sync.Mutex
	Pix   []byte // width*height*4
	Mode  [3]uint32
	Flips int
	Rects [][4]uint32
}

// NewFakeFB builds a live window (width×height, caps) plus its client.
// Pass width=0 to model a headless boot.
func NewFake(width, height uint32, caps uint32) (*FakeFB, *FBWindow, error) {
	fbSize := int(width) * int(height) * 4
	total := fbPixBase + fbSize + 64 // slack after pixel area
	mem := make([]byte, total)
	lib.Put32(mem[0x00:], fbMagic)
	lib.Put32(mem[0x04:], width)
	lib.Put32(mem[0x08:], height)
	lib.Put32(mem[0x0c:], FBBpp)
	lib.Put64(mem[0x10:], uint64(fbPixBase)) // pixels right after header
	lib.Put32(mem[0x18:], caps)
	mu := &sync.Mutex{}
	ff := &FakeFB{Pix: mem[fbPixBase : fbPixBase+fbSize]}
	go ff.serve(mem, mu)
	w, err := NewFBWindow(mem)
	if err != nil {
		return nil, nil, err
	}
	w.mu = mu // shared: guest writes + backend completion serialize
	return ff, w, nil
}

func (ff *FakeFB) serve(mem []byte, mu *sync.Mutex) {
	var served uint32
	for {
		mu.Lock()
		next := lib.Get32(mem[0x24:])
		if next == served {
			mu.Unlock()
			runtime.Gosched()
			continue
		}
		served = next
		op := lib.Get32(mem[0x20:])
		a := lib.Get32(mem[0x30:])
		b := lib.Get32(mem[0x34:])
		c := lib.Get32(mem[0x38:])
		st := int32(0)
		ff.Mu.Lock()
		switch op {
		case fbOpSetMode:
			if a == 0 || b == 0 || a*b*4 > uint32(len(ff.Pix)) {
				st = -1
			} else {
				lib.Put32(mem[0x04:], a)
				lib.Put32(mem[0x08:], b)
				ff.Mode = [3]uint32{a, b, c}
			}
		case fbOpFlip:
			ff.Flips++
		case fbOpUpdateRect:
			ff.Rects = append(ff.Rects, [4]uint32{a, b, c, lib.Get32(mem[0x3c:])})
		default:
			st = -1
		}
		ff.Mu.Unlock()
		lib.Put32(mem[0x28:], next)
		lib.Put32(mem[0x2c:], uint32(st))
		mu.Unlock()
	}
}
