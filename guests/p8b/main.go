//go:build wasip1

// guests/p8b -- the Phase 8 persistence probe.
//
// ONE guest, run TWICE against the same persist.img, that decides what to do
// by what it finds. That self-description is the point: there are no
// arguments, so the two boots cannot disagree about which is which, and the
// gate gets both halves of Phase 8's gate (a) -- write, reset, read back --
// from a single module.
//
//	boot 1: no magic at the probe LBA -> write the pattern, "[p8b] wrote"
//	boot 2: magic present           -> verify it,       "[p8b] readback ok"
//
// It goes through kern_blk_read/write (ABI section 3), NOT the block window:
// managed-runtime guests use the imports. Those imports are whole-disk raw
// access and require CAP_FS_ADMIN (section 7 bit 4) -- the kernel audits and
// returns -1 for anyone else. So this guest ALSO proves the capability gate:
// without the bit it prints "FAIL read lba rc=-1" and exits 1, which is a
// different, observable outcome from a successful write.
package main

import (
	"os"
	"unsafe"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport kernel kern_blk_read
func kern_blk_read(lba int32, ptr *byte, count int32) int32

//go:wasmimport kernel kern_blk_write
func kern_blk_write(lba int32, ptr *byte, count int32) int32

// The probe lives at a fixed LBA out of the way of any filesystem a real
// module might place on this volume. p8b writes nothing else, so the offset
// is arbitrary but must be STABLE across boots -- it is the entire contract
// between the two runs.
const (
	probeLBA     = 8
	probeSectors = 1
	sectorSize   = 512
)

// magic marks a sector as ours. It includes a version-ish tag so a future
// format change is a deliberate edit rather than a silent reinterpretation.
var magic = [8]byte{'K', 'F', 'S', '1', 'P', '8', 'B', '1'}

// pattern fills the sector deterministically after the magic, so the second
// boot verifies real content and not merely eight bytes.
func fillPayload(b []byte) {
	copy(b, magic[:])
	for i := len(magic); i < sectorSize; i++ {
		b[i] = byte(i*7 + 13) // arbitrary but fixed: it must round-trip exactly
	}
}

func payloadValid(b []byte) bool {
	for i := range magic {
		if b[i] != magic[i] {
			return false
		}
	}
	for i := len(magic); i < sectorSize; i++ {
		if b[i] != byte(i*7+13) {
			return false
		}
	}
	return true
}

func main() {
	lib.Real() // instantiate the guest-lib bindings the kernel expects

	buf := make([]byte, sectorSize)
	ptr := (*byte)(unsafe.Pointer(&buf[0]))

	rc := kern_blk_read(probeLBA, ptr, probeSectors)
	if rc != 0 {
		// -1 here is almost always CAP_FS_ADMIN missing, and saying so is
		// the difference between a 30-second diagnosis and an afternoon.
		msg := "[p8b] FAIL read lba " + itoa(probeLBA) + " rc=" + itoa(int(rc))
		if rc == -1 {
			msg += " (no CAP_FS_ADMIN? kern_blk_read requires section 7 bit 4)"
		}
		os.Stdout.WriteString(msg + "\n")
		os.Exit(1)
	}

	if payloadValid(buf) {
		// Second boot: the pattern came back through a full reboot.
		os.Stdout.WriteString("[p8b] readback ok at lba=" + itoa(probeLBA) + "\n")
		os.Exit(0)
	}

	fillPayload(buf)
	if rc := kern_blk_write(probeLBA, ptr, probeSectors); rc != 0 {
		os.Stdout.WriteString("[p8b] FAIL write lba " + itoa(probeLBA) + " rc=" + itoa(int(rc)) + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p8b] wrote at lba=" + itoa(probeLBA) + "\n")
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
