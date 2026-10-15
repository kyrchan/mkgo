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
	// Own the well-known name first. Mounting means deciding whether the
	// block device is empty, which scans the whole thing, and a client that
	// arrives mid-scan used to find no 'fs' port at all. Datagrams sent after
	// this point queue in the port until the loop below starts draining them.
	fsPort := bindServicePort(lib.Real(), lib.NameFS, nil)
	if fsPort == lib.InvalidHandle {
		os.Stdout.WriteString("[fs] cannot bind fs port\n")
		return
	}
	dev, err := attachDevice()
	if err != nil {
		os.Stdout.WriteString("[fs] no block device\n")
		return
	}
	store, err := mountAuto(dev)
	if err != nil {
		// A real OS does not wipe storage it did not create. Formatting a
		// device that holds another filesystem destroys real data, so the
		// absence of a KFS1 superblock is never on its own a licence to
		// format: it means the image is wrong (FAT where KFS1 was
		// expected) or the volume was never made.
		//
		// Emptiness is the one exception, and it is provable rather than
		// assumed. The kernel provisions a zeroed RAM disk, and a gate image
		// may carry a FAT16 boot volume with no KFS1 volume after it; in
		// both cases there is nothing to lose. Scanning the whole device
		// costs a boot-time read pass and only runs on this path, so the
		// strict rule survives everywhere it matters: a device holding any
		// data is still refused, and mkfs remains available explicitly.
		if blank, berr := deviceIsBlank(dev); berr == nil && blank {
			os.Stdout.WriteString("[fs] block device is empty; creating KFS1 volume\n")
			if ferr := FormatKFS(dev); ferr != nil {
				os.Stdout.WriteString("[fs] create volume failed\n")
				os.Exit(1)
			}
			store, err = mountAuto(dev)
		}
	}
	if err != nil {
		if len(os.Args) > 1 && os.Args[1] == "mkfs" {
			os.Stdout.WriteString("[fs] mkfs: creating fresh KFS1 volume (explicit request)\n")
			if ferr := FormatKFS(dev); ferr != nil {
				os.Stdout.WriteString("[fs] mkfs failed\n")
				os.Exit(1)
			}
			os.Stdout.WriteString("[fs] mkfs ok\n")
			return
		}
		os.Stdout.WriteString("[fs] no KFS1 superblock on block device; refusing to format\n")
		os.Stdout.WriteString("[fs] the device holds data: seed a KFS1 image or run `fs.wasm mkfs` once\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[fs] serving /etc /home /boot/modules\n")
	ServeFSOn(lib.Real(), fsPort, store, ServerOptions{})
}
