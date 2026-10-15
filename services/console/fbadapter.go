//go:build wasip1

package main

import (
	"unsafe"

	lib "kernel.lane/guests/lib"
)

// fbMemAt slices the session's linear memory at an absolute offset
// (wasm32 base 0) — same sanctioned pattern as fs pre-v1.1 block; the
// §9.FB window remains a memory-window contract in v1.2.
func fbMemAt(off uint64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

var _ = lib.StatusOK
