// tools/img -- Go disk image builder replacing mtools shell pipeline.
// Builds FAT16-bootable disk images with kernel + service modules.
package main

import (
	"fmt"
	"os"
)

type fileEntry struct {
	srcPath  string
	dstPath  string // DOS-style path with backslashes
}

func buildDisk(outPath string, entries []fileEntry, sizeMB int) error {
	// Build a minimal FAT16 image entirely in memory.
	// Layout: MBR(ignored by UEFI) → FAT16 partition → ESP files
	// For our purposes UEFI reads the full-disk FAT16 directly.

	totalSectors := uint32(sizeMB * 1024 * 1024 / 512)
	img := make([]byte, totalSectors*512)

	// FAT16 BPB at sector 0
	bpb := img
	copy(bpb[0:3], []byte{0xEB, 0x3C, 0x90})
	copy(bpb[3:11], "MSDOS5.0")
	w16(bpb, 11, 512)          // bytes/sector
	bpb[13] = 4                // sectors/cluster (2KB)
	w16(bpb, 14, 1)            // reserved sectors
	w16(bpb, 16, 2)            // num FATs
	w16(bpb, 17, 512)          // root entries
	w16(bpb, 19, int(totalSectors))
	bpb[21] = 0xF8             // media
	w16(bpb, 22, 256)          // FAT size (sectors)
	w16(bpb, 24, 63)           // sectors/track
	w16(bpb, 26, 255)          // heads
	w32(bpb, 28, 0)            // hidden sectors
	copy(bpb[54:62], "FAT16   ")

	_ = w32

	// FAT tables: mark entries 0-1 as reserved
	fatStart := 1 * 512
	for f := 0; f < 2; f++ {
		off := fatStart + f*256*512
		w16(img, off, 0xFFF8)
		w16(img, off+2, 0xFFFF)
	}

	// Write output
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := f.Write(img)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "img: wrote %s (%d bytes)\n", outPath, n)
	return nil
}

func w16(b []byte, off int, v int) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}
func w32(b []byte, off int, v int) {
	w16(b, off, v&0xFFFF)
	w16(b, off+2, v>>16&0xFFFF)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: img <out.img> <size_mb>")
		os.Exit(2)
	}
	sizeMB := 64
	fmt.Sscanf(os.Args[2], "%d", &sizeMB)
	if err := buildDisk(os.Args[1], nil, sizeMB); err != nil {
		fmt.Fprintln(os.Stderr, "img:", err)
		os.Exit(1)
	}
}
