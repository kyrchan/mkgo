package main

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// FuzzFatReader exercises the independent FAT16 reader (readback.go) against
// arbitrary byte input, locking the "no panic on any input" invariant that
// practice #4 requires for binary parsers.
//
// The reader has three layers of slicing that can panic on corrupt input:
//   - fat(n) reads two bytes at fr.fatOff + n*2 without bounds-checking n
//   - chain() slices fr.raw at dataOff + (cl-2)*cb; a corrupt cluster number
//     from a directory entry can index past the image end
//   - listDir() follows the same chain() path for sub-directories
//
// newFatReader validates the BPB but cannot prevent a well-formed header from
// carrying a directory entry whose start-cluster points to a corrupt FAT chain.
// The fuzzer finds those cases by mutation.
//
// Invariants asserted:
//  1. newFatReader never panics (returns error for malformed input).
//  2. When parsing succeeds, listDir(0) never panics.
//  3. find + readFile on every readable entry never panics.
func FuzzFatReader(f *testing.F) {
	// Seed: a valid 4 MiB FAT16 image with one file.
	f.Add(validFat16Seed())
	// Seed: all-zeros (fails magic check).
	f.Add(make([]byte, 512))
	// Seed: has magic + version but wrong fstype string (fails fstype check).
	f.Add(truncatedFat16Seed())
	// Seed: valid BPB fields but tiny image — exercises out-of-bounds guards
	// in listDir/chain/fat. newFatReader accepts it; downstream must not panic.
	f.Add(minimalFat16Seed())
	// Seed: valid BPB + boot sig but root entry's start-cluster is corrupt,
	// driving chain() out of bounds if unchecked. newFatReader accepts it;
	// listDir/find/readFile must reject it gracefully.
	f.Add(corruptChainSeed())

	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := newFatReader(data)
		if err != nil {
			return
		}
		entries, err := fr.listDir(0)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.attr&attrDir != 0 {
				continue
			}
			// find and readFile must not panic even if the entry's
			// start cluster is garbage.
			_, _, _ = fr.find("/" + e.name)
			_, _ = fr.readFile("/" + e.name)
		}
	})
}

// minimalFat16Seed is a 512-byte image with valid BPB fields (boot signature,
// FAT16 fstype) but too small for the root/FAT/data regions. This exercises
// the bounds-checking path: newFatReader accepts it, but downstream functions
// must reject out-of-bounds access gracefully rather than panicking.
func minimalFat16Seed() []byte {
	b := make([]byte, 512)
	b[510] = 0x55
	b[511] = 0xAA
	b[13] = 1 // spc = 1
	// reserved sectors (offset 14, uint16) = 1
	binary.LittleEndian.PutUint16(b[14:], 1)
	b[16] = 2                                  // nfats = 2
	binary.LittleEndian.PutUint16(b[17:], 16)  // rootEnts = 16 (multiple of 16)
	binary.LittleEndian.PutUint16(b[22:], 1)   // fatSecs = 1
	binary.LittleEndian.PutUint16(b[32:], 512) // totSecs = 512
	copy(b[54:], "FAT16   ")
	return b
}

// validFat16Seed builds a minimal valid FAT16 image with one file entry
// so the fuzzer can explore from a known-good starting point. Uses 4 MiB
// (not the full 64 MiB) to keep mutation cheap in the fuzzer.
func validFat16Seed() []byte {
	const seedSize = 4 << 20
	dev := NewMemBlock(seedSize)
	v, _ := NewVolume(dev, seedSize, "SEED")
	if err := v.Add(FileSource{Name: "/etc/motd", Data: []byte("hello\n"),
		Mtime: time.Unix(0, 0)}); err != nil {
		panic(err)
	}
	if err := v.Flush(); err != nil {
		panic(err)
	}
	return dev.Bytes()
}

// truncatedFat16Seed has the magic bytes and boot signature but BPB
// fields are all zeros, failing the implausibility check.
func truncatedFat16Seed() []byte {
	b := make([]byte, 512)
	b[510] = 0x55
	b[511] = 0xAA
	return b
}

// corruptChainSeed has a valid BPB and boot signature but the first
// root-directory entry's start-cluster field is overwritten with a value
// that would drive chain() out of bounds if unchecked.
func corruptChainSeed() []byte {
	b := validFat16Seed()
	// 4 MiB: spc=1, fatSecs=32, root at sector 1+64=65.
	rootEntStart := 1 + 2*32
	entOff := rootEntStart*512 + 26 // start-cluster field in first 32-byte entry
	if entOff+2 <= len(b) {
		b[entOff] = 0xFF
		b[entOff+1] = 0xFF
	}
	return b
}

// TestFatReaderDeterminism is a property-style test: given the same image,
// repeated reads must produce identical output (no state leakage between
// reads). This was originally a Fuzz target but is a correctness assertion
// rather than a bug-finding fuzz target, so it lives as a regular test to
// keep -fuzz=. unambiguous for FuzzFatReader.
func TestFatReaderDeterminism(t *testing.T) {
	seeds := [][]byte{validFat16Seed(), truncatedFat16Seed(), corruptChainSeed()}
	for i, data := range seeds {
		fr, err := newFatReader(data)
		if err != nil {
			continue
		}
		first, err1 := fr.listDir(0)
		if err1 != nil {
			continue
		}
		fr2, err := newFatReader(data)
		if err != nil {
			continue
		}
		second, err2 := fr2.listDir(0)
		if err2 != nil {
			continue
		}
		if !dirsEqual(first, second) {
			t.Fatalf("seed %d: non-deterministic read: %d vs %d entries",
				i, len(first), len(second))
		}
	}
}

func dirsEqual(a, b []rdEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].name != b[i].name || a[i].attr != b[i].attr ||
			a[i].start != b[i].start || a[i].size != b[i].size ||
			!bytes.Equal(a[i].short[:], b[i].short[:]) {
			return false
		}
	}
	return true
}
