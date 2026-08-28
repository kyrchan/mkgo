//go:build wasip1

// fs.wasm entry: mount-or-format KFS1 (log-structured) over the kernel
// block imports (ABI v1.1 managed-runtime transport), then serve the
// "fs" port forever. FAT16 remains in-tree as an import/export filter.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o fs.wasm .
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield()

func main() {
	os.Stdout.WriteString("[fs] up\n")
	dev, err := attachDevice()
	if err != nil {
		os.Stdout.WriteString("[fs] no block device\n")
		return
	}
	store, err := MountKFS(dev)
	if err != nil {
		os.Stdout.WriteString("[fs] formatting fresh KFS volume\n")
		if ferr := FormatKFS(dev); ferr != nil {
			os.Stdout.WriteString("[fs] format failed\n")
			return
		}
		store, err = MountKFS(dev)
		if err != nil {
			os.Stdout.WriteString("[fs] mount failed after format\n")
			return
		}
	}
	os.Stdout.WriteString("[fs] serving /etc /home /boot/modules\n")
	ServeFS(lib.Real(), store, ServerOptions{})
}
