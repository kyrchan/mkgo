package main

import (
	"errors"
	"testing"
)

// memDev is an in-memory BlockDev so blankness can be tested against exact
// byte patterns rather than a real image.
type memDev struct {
	data    []byte
	blkSize uint32
	reads   int
}

func newMemDev(sectors int) *memDev {
	return &memDev{data: make([]byte, sectors*512), blkSize: 512}
}

func (m *memDev) Read(lba uint64, b []byte) error {
	m.reads++
	off := lba * uint64(m.blkSize)
	if off+uint64(len(b)) > uint64(len(m.data)) {
		return errors.New("out of range")
	}
	copy(b, m.data[off:off+uint64(len(b))])
	return nil
}

func (m *memDev) Write(lba uint64, b []byte) error {
	off := lba * uint64(m.blkSize)
	copy(m.data[off:off+uint64(len(b))], b)
	return nil
}

func (m *memDev) Geometry() (uint32, uint64) { return m.blkSize, uint64(len(m.data) / int(m.blkSize)) }

func TestDeviceIsBlankOnZeroedDevice(t *testing.T) {
	d := newMemDev(64)
	blank, err := deviceIsBlank(d)
	if err != nil {
		t.Fatalf("deviceIsBlank: %v", err)
	}
	if !blank {
		t.Fatal("a fully zeroed device must report blank")
	}
}

// The whole point of the rule: a device holding a filesystem is never blank,
// so fs.wasm refuses to format it and nothing is destroyed.
func TestDeviceIsBlankRefusesRealFilesystem(t *testing.T) {
	d := newMemDev(64)
	// A FAT16 boot signature at sector 0: the exact case that must not be
	// silently reformatted.
	copy(d.data, []byte{0xeb, 0x3c, 0x90})
	blank, err := deviceIsBlank(d)
	if err != nil {
		t.Fatalf("deviceIsBlank: %v", err)
	}
	if blank {
		t.Fatal("device carrying a filesystem must not report blank")
	}
}

// Data anywhere in the device counts, including at the very end. A scan that
// only looked at the first sectors would call this blank and wipe it.
func TestDeviceIsBlankDetectsDataAtEnd(t *testing.T) {
	d := newMemDev(64)
	d.data[len(d.data)-1] = 0xff
	blank, err := deviceIsBlank(d)
	if err != nil {
		t.Fatalf("deviceIsBlank: %v", err)
	}
	if blank {
		t.Fatal("non-zero byte in the final sector must not report blank")
	}
}

// Scan must stop at the first non-zero sector: proving a device is empty
// means reading all of it, but proving it is NOT empty should be cheap.
func TestDeviceIsBlankFailsFastOnData(t *testing.T) {
	d := newMemDev(4096)
	d.data[512] = 1 // sector 1
	blank, err := deviceIsBlank(d)
	if err != nil {
		t.Fatalf("deviceIsBlank: %v", err)
	}
	if blank {
		t.Fatal("expected not blank")
	}
	if d.reads > 4 {
		t.Fatalf("scan should stop early, took %d reads", d.reads)
	}
}

// A blank scan must read the WHOLE device, otherwise "blank" is a guess.
func TestDeviceIsBlankScansEntireDevice(t *testing.T) {
	d := newMemDev(4096)
	if _, err := deviceIsBlank(d); err != nil {
		t.Fatalf("deviceIsBlank: %v", err)
	}
	if got, want := d.reads, 4096/blankScanChunk; got != want {
		t.Fatalf("reads=%d want %d: blankness needs a full scan", got, want)
	}
}

func TestDeviceIsBlankWithoutGeometry(t *testing.T) {
	d := newMemDev(0)
	if _, err := deviceIsBlank(d); err == nil {
		t.Fatal("expected an error when the device reports no blocks")
	}
}
