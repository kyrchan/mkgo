package jitc

import (
	"os"
	"testing"
	"unsafe"
)

func TestF78TrapSlot(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CompileOne(m, 78)
	if err != nil {
		t.Fatal(err)
	}
	// execCode runs the code in-place; the Go slice backing array is in
	// the heap, which is not executable on Linux without an RWX mapping.
	// Use NewRunner (which mmaps RWX) instead so the trampoline can
	// actually fetch the emitted bytes.
	img := &Image{Funcs: map[uint32]int{78: 0}, Region: a.Code, Polls: a.Polls}
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var trap uint64
	globals := make([]uint64, len(m.Globals))
	var gptr unsafe.Pointer
	if len(globals) > 0 {
		gptr = unsafe.Pointer(&globals[0])
	}
	args := []uint64{1}
	aptr := unsafe.Pointer(&args[0])
	mem := r.Mem()
	cptr := unsafe.Pointer(&r.code[0])
	v := execCode(cptr, unsafe.Pointer(&mem[0]), uint64(len(mem)), &trap,
		gptr, aptr, len(args))
	t.Logf("value=%x trap=%d trapAddr=%p", v, trap, unsafe.Pointer(&trap))
	t.Logf("mem[0:8] = % x", mem[0:8])
}
