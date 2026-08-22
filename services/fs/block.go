package main

import (
	"errors"
	"runtime"
	"sync"

	lib "kernel.lane/guests/lib"
)

// Block window (abi/ABI.md §3). Concrete v1 byte layout — the field
// ORDER is the ABI's; offsets are naturally aligned and mirrored by the
// kernel RAM-disk shim (see services/ABI-NOTES.md):
//
//	0x00 u32 magic 'BLKW'      0x04 u32 blk_size (=512)
//	0x08 u64 num_blocks        0x10 u32 next_req_id   0x14 pad u32
//	-- request mailbox --      0x18 u64 op   0x20 u64 lba
//	0x28 u32 count             0x2c pad u32           0x30 u64 off
//	-- completion slot --      0x38 u32 done_req_id   0x3c i32 status
//
// Data lands at window offset `off`; guests pick a scratch area beyond
// the header. Window size ≥ bwWindowMin.
const (
	bwMagic      uint32 = 0x574B4C42 // 'BLKW' little-endian
	bwBlkSize    uint32 = 512
	bwMaxSectors        = 8
	bwDataOff           = 0x1000
	bwWindowMin         = bwDataOff + bwMaxSectors*int(bwBlkSize)

	bwOpRead  uint64 = 1
	bwOpWrite uint64 = 2
)

var ErrBlockWindow = errors.New("fs: bad block window")

// BlockWindow drives one §3 window mapped into linear memory at mem[0:].
// On wasm, mem is the session's own linear memory starting at the
// win_off reported by devman ENUM; on the host it is a simulated window
// completed by RamDisk — identical mailbox semantics both ways.
// The mailbox is shared memory: on the host we serialize guest/backend
// access with a mutex (the kernel's real barriers don't exist in Go's
// memory model; -race demands the lock).
type BlockWindow struct {
	mem []byte
	mu  *sync.Mutex
	req uint32 // last issued request id
}

// NewBlockWindow validates the header and adopts the window.
func NewBlockWindow(mem []byte) (*BlockWindow, error) {
	if len(mem) < bwWindowMin {
		return nil, ErrBlockWindow
	}
	if lib.Get32(mem) != bwMagic || lib.Get32(mem[4:]) != bwBlkSize {
		return nil, ErrBlockWindow
	}
	if mem[0x14] == 0xFF && mem[0x15] == 0xFF { // marker unused; reserved
		_ = mem
	}
	w := &BlockWindow{mem: mem, mu: &sync.Mutex{}}
	w.req = lib.Get32(mem[0x10:])
	return w, nil
}

// Geometry returns the device shape.
func (w *BlockWindow) Geometry() (blkSize uint32, numBlocks uint64) {
	return lib.Get32(w.mem[4:]), lib.Get64(w.mem[8:])
}

// Request submits op(1=read|2=write)/lba/count and polls until the
// completion slot matches our request id, returning the backend status.
func (w *BlockWindow) Request(op uint64, lba uint64, count uint32) (int32, error) {
	if count == 0 || count > bwMaxSectors {
		return -1, errors.New("fs: bad sector count")
	}
	numBlocks := lib.Get64(w.mem[8:])
	if lba+uint64(count) > numBlocks {
		return -1, errors.New("fs: lba out of range")
	}
	w.mu.Lock()
	lib.Put64(w.mem[0x18:], op)
	lib.Put64(w.mem[0x20:], lba)
	lib.Put32(w.mem[0x28:], count)
	lib.Put64(w.mem[0x30:], bwDataOff)
	next := lib.Get32(w.mem[0x10:]) + 1
	lib.Put32(w.mem[0x10:], next)
	w.mu.Unlock()
	w.req = next

	for {
		runtime.Gosched() // §3: single outstanding request, polled
		w.mu.Lock()
		done := lib.Get32(w.mem[0x38:])
		st := int32(lib.Get32(w.mem[0x3c:]))
		w.mu.Unlock()
		if done == w.req {
			return st, nil
		}
	}
}

// Read copies sectors into buf (chunked through the scratch area).
func (w *BlockWindow) Read(lba uint64, buf []byte) error {
	bs, _ := w.Geometry()
	done := uint32(0)
	for done < uint32(len(buf)) {
		count := uint32(len(buf)) - done
		sectors := (count + bs - 1) / bs
		if sectors > bwMaxSectors {
			sectors = bwMaxSectors
		}
		st, err := w.Request(bwOpRead, lba+uint64(done/bs), sectors)
		if err != nil {
			return err
		}
		if st < 0 {
			return errors.New("fs: block read failed")
		}
		n := sectors * bs
		copy(buf[done:], w.mem[bwDataOff:bwDataOff+int(n)])
		done += n
	}
	return nil
}

// Write stages buf through scratch in ≤8-sector chunks; buf must be
// sector-aligned.
func (w *BlockWindow) Write(lba uint64, buf []byte) error {
	bs, _ := w.Geometry()
	if len(buf)%int(bs) != 0 {
		return errors.New("fs: write buffer must be sector-aligned")
	}
	done := uint32(0)
	for done < uint32(len(buf)) {
		chunk := uint32(len(buf)) - done
		if chunk > bwMaxSectors*bs {
			chunk = bwMaxSectors * bs
		}
		copy(w.mem[bwDataOff:bwDataOff+int(chunk)], buf[done:])
		st, err := w.Request(bwOpWrite, lba+uint64(done/bs), chunk/bs)
		if err != nil {
			return err
		}
		if st < 0 {
			return errors.New("fs: block write failed")
		}
		done += chunk
	}
	return nil
}

// Scratch exposes the transfer area for direct staging before Write.
func (w *BlockWindow) Scratch() []byte {
	return w.mem[bwDataOff : bwDataOff+bwMaxSectors*int(bwBlkSize)]
}

// ---- host-side §3 backend simulation ----

// RamDisk is the host stand-in for the kernel's RAM-disk block backend:
// it watches next_req_id and completes requests exactly as the shim
// would, staging data inside the window's scratch area.
type RamDisk struct {
	Mu   sync.Mutex
	Disk []byte // numBlocks * 512
}

// NewRamDisk builds a zeroed disk of n blocks plus a live §3 window
// served by a completion goroutine.
func NewRamDisk(numBlocks int) (*RamDisk, *BlockWindow, error) {
	rd := &RamDisk{Disk: make([]byte, numBlocks*int(bwBlkSize))}
	mem := make([]byte, bwWindowMin)
	mu := &sync.Mutex{}
	lib.Put32(mem[0x00:], bwMagic)
	lib.Put32(mem[0x04:], bwBlkSize)
	lib.Put64(mem[0x08:], uint64(numBlocks))
	go rd.serveWindow(mem, mu)
	w, err := NewBlockWindow(mem)
	if err != nil {
		return nil, nil, err
	}
	w.mu = mu // share the backend's lock
	return rd, w, nil
}

func (rd *RamDisk) serveWindow(mem []byte, mu *sync.Mutex) {
	var served uint32
	for {
		mu.Lock()
		next := lib.Get32(mem[0x10:])
		if next == served {
			mu.Unlock()
			runtime.Gosched()
			continue
		}
		served = next
		op := lib.Get64(mem[0x18:])
		lba := lib.Get64(mem[0x20:])
		count := int(lib.Get32(mem[0x28:]))
		off := int(lib.Get64(mem[0x30:]))
		st := int32(0)
		switch op {
		case bwOpRead:
			copy(mem[off:off+count*int(bwBlkSize)],
				rd.Disk[int(lba)*int(bwBlkSize):(int(lba)+count)*int(bwBlkSize)])
		case bwOpWrite:
			copy(rd.Disk[int(lba)*int(bwBlkSize):(int(lba)+count)*int(bwBlkSize)],
				mem[off:off+count*int(bwBlkSize)])
		default:
			st = -1
		}
		lib.Put32(mem[0x38:], next)
		lib.Put32(mem[0x3c:], uint32(st))
		mu.Unlock()
	}
}
