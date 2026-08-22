//go:build wasip1

package main

// Managed-runtime block transport (ABI v1.1 changelog): Go guests cannot
// safely reserve a fixed address range against their own heap, so fs.wasm
// reaches the kernel's block backends through imports instead of mapping
// the §3 window. The same kernel backends (RAM-disk now, virtio-blk in
// Phase 8) serve both transports; backend swap stays invisible.
//
//	imports: kern_blk_read(lba, ptr, count) -> i32  // 0 ok | -1 err
//	         kern_blk_write(lba, ptr, count) -> i32
import (
	"errors"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport kernel kern_blk_read
func kern_blk_read(lba int32, ptr *byte, count int32) int32

//go:wasmimport kernel kern_blk_write
func kern_blk_write(lba int32, ptr *byte, count int32) int32

// blkDevMaxChunks bounds one import call (mirrors §3's ≤8-sector spirit).
const blkDevMaxSectors = 8

// importedBlock is a BlockDev backed by the kernel block imports.
type importedBlock struct {
	numBlocks uint32
}

var errBlkIO = errors.New("fs: block import failed")

func newImportedBlock(numBlocks uint32) BlockDev {
	return &importedBlock{numBlocks: numBlocks}
}

func (b *importedBlock) Read(lba uint64, buf []byte) error {
	return b.xfer(kern_blk_read, lba, buf)
}

func (b *importedBlock) Write(lba uint64, buf []byte) error {
	return b.xfer(kern_blk_write, lba, buf)
}

func (b *importedBlock) xfer(
	fn func(int32, *byte, int32) int32,
	lba uint64,
	buf []byte,
) error {
	if len(buf)%int(bwBlkSize) != 0 || len(buf) == 0 {
		return errors.New("fs: buffer must be sector-aligned")
	}
	done := uint64(0)
	for done < uint64(len(buf)) {
		chunk := uint64(len(buf)) - done
		if max := uint64(blkDevMaxSectors) * uint64(bwBlkSize); chunk > max {
			chunk = max
		}
		slice := buf[done : done+chunk]
		lbaNow := lba + done/uint64(bwBlkSize)
		if rc := fn(int32(lbaNow), &slice[0], int32(chunk/uint64(bwBlkSize))); rc != 0 {
			return errBlkIO
		}
		done += chunk
	}
	return nil
}

func (b *importedBlock) Geometry() (uint32, uint64) { return bwBlkSize, uint64(b.numBlocks) }

var _ BlockDev = (*importedBlock)(nil)

// defaultNumBlocks is the v1 disk size from AGENTS.md (8 MiB). The v1.1
// imports carry no geometry probe; until one exists (v2 note) the boot
// volume size is fixed by convention.
const defaultNumBlocks = 16384

// attachDevice returns the block transport for this session: kernel
// imports on wasm.
func attachDevice() (BlockDev, error) {
	return newImportedBlock(defaultNumBlocks), nil
}

var _ = lib.FSOK // keep lib linked for shared constants
