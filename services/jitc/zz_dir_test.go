package jitc

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestF78Direct(t *testing.T) {
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
	c := a.Code
	pages := (len(c) + 0xFFF) &^ 0xFFF
	code, err := syscall.Mmap(-1, 0, pages,
		syscall.PROT_READ|syscall.PROT_WRITE|syscall.PROT_EXEC,
		syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	copy(code, c)
	defer syscall.Munmap(code)
	var trap uint64
	globals := make([]uint64, len(m.Globals))
	var gptr unsafe.Pointer
	if len(globals) > 0 {
		gptr = unsafe.Pointer(&globals[0])
	}
	args := []uint64{1}
	aptr := unsafe.Pointer(&args[0])
	mem := make([]byte, 64*65536)
	mptr := unsafe.Pointer(&mem[0])
	cptr := unsafe.Pointer(&code[0])
	v := execCode(cptr, mptr, uint64(len(mem)), &trap, gptr, aptr, len(args))
	t.Logf("value=%x trap=%d trapPtr=%p", v, trap, unsafe.Pointer(&trap))
	t.Logf("mem[0:16] = % x", mem[0:16])
	syscall.Munmap(code)
}
