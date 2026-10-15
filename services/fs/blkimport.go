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

//go:wasmimport kernel kern_blk_geometry
func kern_blk_geometry(outPtr *byte) int32

// blkDevMaxChunks bounds one import call (mirrors §3's ≤8-sector spirit).
const blkDevMaxSectors = 8 // must match kernel virtio_blk_rw limit

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

// ramDiskBlocks is the kernel's fixed RAM-disk size (16 MiB, see
// core/devblk.cc). Matches the kernel's 16*1024*1024 / BLK_SECT.
const ramDiskBlocks = 32768

// defaultNumBlocks is the fallback disk size when no geometry can be
// detected. The kernel allocates a 16 MiB RAM disk; we use the full size
// so KFS can use every available sector.
const defaultNumBlocks = ramDiskBlocks

// detectDiskSize reads the boot sector and, if it carries a FAT16 BPB,
// returns the total sector count from there. Otherwise returns 0
// (caller falls back to defaultNumBlocks).
// queryDiskSize asks the kernel how many sectors the block device actually
// has. The previous implementation read the FAT16 BPB at LBA 0 and returned
// its total-sectors field, which reports the size of that FILESYSTEM rather
// than of the device. On a disk that also carries a KFS1 data volume after the
// boot volume that halves the reported size, the derived split boundary moves
// with it, and the data volume becomes unreachable -- which is exactly how
// test-p18 lost its seeded volume. Capacity is a property of the device, so
// ask the device.
//
// Capacity is read through the kern_blk_geometry import, which is capability
// checked like kern_blk_read/write; on failure we fall back to the RAM-disk
// default so an unprivileged fs still mounts something.
func queryDiskSize(dev BlockDev) uint32 {
	var g [16]byte
	if rc := kern_blk_geometry(&g[0]); rc != 0 {
		return 0
	}
	sectors := uint32(0)
	for i := 0; i < 4; i++ {
		sectors |= uint32(g[i]) << (8 * i)
	}
	return sectors
}

// attachDevice returns the block transport for this session: kernel
// imports on wasm. On a fresh boot the kernel exposes a zeroed RAM disk;
// when virtio-blk is present the QEMU disk (FAT16 seed volume) is visible
// and attachDevice reports its real size so KFS can import into it.
func attachDevice() (BlockDev, error) {
	dev := newImportedBlock(defaultNumBlocks)
	if n := queryDiskSize(dev); n > 0 && n != defaultNumBlocks {
		return newImportedBlock(n), nil
	}
	return dev, nil
}

var _ = lib.FSOK // keep lib linked for shared constants
