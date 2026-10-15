package main

import "errors"

// deviceIsBlank reports whether every sector of dev reads as zero.
//
// fs.wasm does not format at boot when the volume merely lacks a superblock,
// because formatting destroys whatever the device actually holds. That rule is
// right, and it is the reason a mistyped image surfaces as a failure instead of
// as silently reinitialised storage.
//
// It is too strict for a device the kernel has just provisioned, though. The
// kernel's RAM disk is allocated zeroed and carries no filesystem at all, and
// 32 of the gate images ship a FAT16 boot volume with no KFS1 volume after it.
// In both cases there is nothing to lose, so refusing to format leaves fs.wasm
// exiting 1 forever and init respawn-looping it.
//
// So the distinction that matters is emptiness, not the absence of a
// superblock: a device proven to hold nothing may be formatted, and a device
// holding ANY data -- another filesystem, a stale superblock, a half-written
// log -- may not. This scans the whole device and stops at the first non-zero
// sector, so the expensive path only runs for a genuinely blank device and a
// device with data fails fast.
//
// Host-testable on purpose: this decides whether fs.wasm destroys a guest's
// storage, which is exactly the kind of decision that must not only be tested
// through a booted image.
func deviceIsBlank(dev BlockDev) (bool, error) {
	bs, numBlocks := dev.Geometry()
	if bs == 0 || numBlocks == 0 {
		return false, errBlankBadGeometry
	}
	var buf []byte
	for lba := uint64(0); lba < numBlocks; lba += blankScanChunk {
		n := uint64(blankScanChunk)
		if rem := numBlocks - lba; rem < n {
			n = rem
		}
		need := int(n) * int(bs)
		if cap(buf) < need {
			buf = make([]byte, need)
		}
		buf = buf[:need]
		if err := dev.Read(lba, buf); err != nil {
			return false, err
		}
		for _, b := range buf {
			if b != 0 {
				return false, nil
			}
		}
	}
	return true, nil
}

// blankScanChunk is how many sectors one Read covers while scanning. The block
// import pair bounds a single transfer, so the chunk has to stay inside that
// limit; it matches the guest transport's own bound.
const blankScanChunk = 8

// errBlankBadGeometry means the device reported no usable size, so blankness
// cannot be established and the safe answer is "not blank".
var errBlankBadGeometry = errors.New("fs: block device reported no geometry")
