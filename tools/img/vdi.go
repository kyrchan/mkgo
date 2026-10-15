package main

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"os"
)

// VirtualBox fixed-image VDI writer for the Phase-12 hypervisor matrix.
//
// Layout follows the VDI header as consumed by VirtualBox (VBOXHDD.h,
// VDIIMAGEHEADER) and qemu's block/vdi.c reader:
//
//	0x000  64B   pre-header text "<<< Oracle VM VirtualBox Disk Image >>>\n"
//	0x040  u32   signature        0xBEDA1070
//	0x044  u32   version          0x00010001
//	0x048  u32   header size      0x190 (VBox/qemu convention)
//	0x04C  u32   image type       1=dynamic 2=fixed
//	0x050  i32   flags            0
//	0x054 256B   comment          zeros
//	0x154  u32   offset of blocks map — fixed images need none: 0xFFFFFFFF
//	0x158  u32   offset of data   0x200 (payload starts right after headers)
//	0x15C  u32   cylinders        CHS from size, 16 heads / 63 spt
//	0x160  u32   heads
//	0x164  u32   sectors per track
//	0x168  u32   sector size      512
//	0x16C  u32   block size       0x100000 (1 MiB, informational for fixed)
//	0x170  u32   total blocks     ceil(disk / block)
//	0x174  u32   blocks allocated = total for fixed images
//	0x178 128B   uuid create/modify/link/parent (md5 of content, deterministic)
//
// For FIXED images VirtualBox maps guests reads linearly at offData, so the
// payload must be the raw disk verbatim. Self-tested by conv_test.go; boot
// behavior is asserted in Phase 12's `make test-hv`.

const (
	vdiSignature   = 0xBEDA1070
	vdiVersion11   = 0x00010001
	vdiHeaderSize  = 0x190
	vdiTypeFixed   = 2
	vdiDataOffset  = 0x200
	vdiBlockSize   = 0x100000
	vdiSectorSize  = 512
	vdiHeads       = 16
	vdiSectorsTrk  = 63
	vdiMaxCyls     = 16383
	vdiNoBlocksMap = 0xFFFFFFFF
)

func vdiCylinders(size int64) uint32 {
	c := uint32(size / (vdiSectorSize * vdiHeads * vdiSectorsTrk))
	if c < 1 {
		c = 1
	}
	if c > vdiMaxCyls {
		c = vdiMaxCyls
	}
	return c
}

// vdiHeader builds the full 512-byte pre-header + disk header region.
func vdiHeader(diskSize int64, uuid [16]byte) []byte {
	h := make([]byte, vdiDataOffset) // zero-filled
	copy(h, "<<< Oracle VM VirtualBox Disk Image >>>\n")
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(h[off:], v) }
	totalBlocks := uint32((diskSize + vdiBlockSize - 1) / vdiBlockSize)
	put32(0x040, vdiSignature)
	put32(0x044, vdiVersion11)
	put32(0x048, vdiHeaderSize)
	put32(0x04C, vdiTypeFixed)
	put32(0x050, 0)
	put32(0x154, vdiNoBlocksMap)
	put32(0x158, vdiDataOffset)
	put32(0x15C, vdiCylinders(diskSize))
	put32(0x160, vdiHeads)
	put32(0x164, vdiSectorsTrk)
	put32(0x168, vdiSectorSize)
	put32(0x16C, vdiBlockSize)
	put32(0x170, totalBlocks)
	put32(0x174, totalBlocks)
	copy(h[0x178:], uuid[:]) // create
	copy(h[0x188:], uuid[:]) // modify
	copy(h[0x198:], uuid[:]) // linkage
	copy(h[0x1A8:], uuid[:]) // parent
	return h
}

// WriteVDI converts a raw image into a fixed-layout VirtualBox VDI.
func WriteVDI(rawPath, outPath string) error {
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		return err
	}
	uuid := md5.Sum(raw)
	out := append(vdiHeader(int64(len(raw)), uuid), raw...)
	if int64(len(out)) != vdiDataOffset+int64(len(raw)) {
		return fmt.Errorf("img: vdi length mismatch")
	}
	return os.WriteFile(outPath, out, 0o644)
}
