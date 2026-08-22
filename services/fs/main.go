//go:build wasip1

// fs.wasm entry: locate the §3 block window through devman ENUM (the fs
// session is granted CAP_DEVMAN at spawn), mount-or-format FAT16, then
// serve the "fs" port forever.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o fs.wasm .
package main

import (
	"os"
	"unsafe"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield()

// ptrAt slices the session's own linear memory at an absolute offset
// (wasm32: memory base is 0). This is the sanctioned way to reach
// kernel-assigned windows per ABI preamble ("guests never compute
// absolute addresses, only window offsets").
func ptrAt(off uint64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

func attachBlockWindow() (*BlockWindow, error) {
	k := lib.Real()
	dm, err := lib.BindDevman(k)
	if err != nil {
		return nil, err
	}
	recs, err := dm.Enum()
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.Class == lib.ClassBlock {
			return NewBlockWindow(ptrAt(r.WinOff, bwWindowMin))
		}
	}
	return nil, ErrBlockWindow
}

func main() {
	os.Stdout.WriteString("[fs] up\n")
	win, err := attachBlockWindow()
	if err != nil {
		os.Stdout.WriteString("[fs] no block window\n")
		return
	}
	fat, err := Mount(win)
	if err != nil {
		os.Stdout.WriteString("[fs] formatting fresh volume\n")
		if ferr := Format(win, "SYSDISK"); ferr != nil {
			os.Stdout.WriteString("[fs] format failed\n")
			return
		}
		fat, err = Mount(win)
		if err != nil {
			os.Stdout.WriteString("[fs] mount failed after format\n")
			return
		}
	}
	os.Stdout.WriteString("[fs] serving /etc /home /boot/modules\n")
	ServeFS(lib.Real(), fat, ServerOptions{})
}
