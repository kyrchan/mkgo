//go:build wasip1

// fs.wasm entry: mount-or-format FAT16 over the kernel block imports
// (ABI v1.1 managed-runtime transport), then serve the "fs" port forever.
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
	fat, err := Mount(dev)
	if err != nil {
		os.Stdout.WriteString("[fs] formatting fresh volume\n")
		if ferr := Format(dev, "SYSDISK"); ferr != nil {
			os.Stdout.WriteString("[fs] format failed\n")
			return
		}
		fat, err = Mount(dev)
		if err != nil {
			os.Stdout.WriteString("[fs] mount failed after format\n")
			return
		}
	}
	os.Stdout.WriteString("[fs] serving /etc /home /boot/modules\n")
	ServeFS(lib.Real(), fat, ServerOptions{})
}
