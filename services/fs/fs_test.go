package main

// Host unit test for the FS core: RAM disk in memory, full protocol flow.
import (
	"bytes"
	"testing"
	"unsafe"
)

func unsafe_Add(b *byte, off uintptr) unsafe.Pointer {
	return unsafe.Pointer(uintptr(unsafe.Pointer(b)) + off)
}

const diskSectors = totSec

var ram [diskSectors * Sect]byte

func TestFSFlow(t *testing.T) {
	blkRead = func(lba uint32, buf *byte, count uint32) int32 {
		if uint64(lba)+uint64(count) > diskSectors {
			return -1
		}
		for i := uint32(0); i < count*Sect; i++ {
			*(*byte)(unsafe_Add(buf, uintptr(i))) = ram[uintptr(lba)*Sect+uintptr(i)]
		}
		return 0
	}
	blkWrite = func(lba uint32, buf *byte, count uint32) int32 {
		if uint64(lba)+uint64(count) > diskSectors {
			return -1
		}
		for i := uint32(0); i < count*Sect; i++ {
			ram[uintptr(lba)*Sect+uintptr(i)] = *(*byte)(unsafe_Add(buf, uintptr(i)))
		}
		return 0
	}
	fmtDisk()

	req := func(op int, uid uint32, path string, payload []byte) []byte {
		f := make([]byte, 26+len(path)+len(payload))
		f[0] = byte(op)
		f[4] = byte(uid)
		copy(f[8:24], "tester")
		f[24] = byte(len(path))
		f[25] = byte(len(path) >> 8)
		copy(f[26:], path)
		copy(f[26+len(path):], payload)
		resp, _ := handleReq(f)
		return resp
	}

	text := []byte("hello from the fs unit test")

	rep := req(opOpen, 1, "hello.txt", []byte{0, 0, 0, 0, 1, 0, 0, 0})
	if errnoOf(rep) != 0 {
		ra := loadDir(0)
		t.Logf("root dump: %v", readDir(ra))
		t.Fatalf("open-create: %v", rep)
	}
	fh := g32(rep, 2)

	wp := make([]byte, 8+len(text))
	le32(wp, 0, fh)
	le32(wp, 4, len(text))
	copy(wp[8:], text)
	if rep = req(opWrite, 1, "", wp); errnoOf(rep) != 0 {
		t.Fatalf("write: %v", rep)
	}
	req(opClose, 1, "", []byte{byte(fh), 0, 0, 0})

	rep = req(opOpen, 1, "hello.txt", nil)
	if errnoOf(rep) != 0 {
		t.Fatalf("reopen: %v", rep)
	}
	fh = g32(rep, 2)
	rp := make([]byte, 8)
	le32(rp, 0, fh)
	le32(rp, 4, len(text))
	rep = req(opRead, 1, "", rp)
	if errnoOf(rep) != 0 || !bytes.Equal(rep[6:], text) {
		t.Fatalf("readback: %v", rep)
	}

	// u2 must not see u1's file (namespace rooting)
	rep = req(opStat, 2, "hello.txt", nil)
	if errnoOf(rep) != 44 {
		t.Fatalf("u2 saw u1 file: %v", rep)
	}

	// delete + verify gone
	rep = req(opDel, 1, "hello.txt", nil)
	if errnoOf(rep) != 0 {
		t.Fatalf("del: %v", rep)
	}
	rep = req(opStat, 1, "hello.txt", nil)
	if errnoOf(rep) != 44 {
		t.Fatalf("still exists: %v", rep)
	}

	// mkdir + ls
	if rep = req(opMkdir, 1, "subdir", nil); errnoOf(rep) != 0 {
		t.Fatalf("mkdir: %v", rep)
	}
	rep = req(opLS, 1, "", nil)
	if errnoOf(rep) != 0 || !bytes.Contains(rep, []byte("subdir")) {
		t.Fatalf("ls: %v", rep)
	}
	t.Logf("fs core flow OK")
}
