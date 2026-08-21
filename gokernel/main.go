package main

// go-kernel: microkernel proper, running on the baremetal Go runtime.
// Firmware handshake (UEFI memmap/ExitBootServices) is done by the tiny C
// shim, which jumps into _rt0_amd64_baremetal with a *bootInfo.

import "unsafe"

type bootInfo struct {
	Magic     uint64
	SerialOK  uint64
	MmapDesc  uint64
	MmapCount uint64
	MmapDSize uint64
	Prog      uint64
	ProgLen   uint64
	FreeBase  uint64
	FreeEnd   uint64
	TscKhz    uint64
}

func bootInfoPtr() *bootInfo {
	return (*bootInfo)(unsafe.Pointer(bootInfoRaw()))
}

func banner() {
	puts("\n[gokern] go microkernel booting\n")
}

func main() {
	banner()

	enableAVX2()
	puts("[kern] avx2=")
	if cpuidAvx2() {
		puts("1")
	} else {
		puts("0")
	}
	puts("\n")

	bi := bootInfoPtr()
	if bi == nil || bi.Magic != 0x424D5442 {
		panic("gokernel: bad boot info")
	}
	puts("[kern] boot info ok: prog=")
	puthex(bi.Prog)
	puts(" len=")
	puthex(bi.ProgLen)
	puts("\n")

	if bi.Prog != 0 && bi.ProgLen >= 32 {
		code := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(bi.Prog))), bi.ProgLen)
		runVM(code)
	} else {
		puts("[kern] no guest program\n")
	}

	puts("[kern] KERNEL-OK go microkernel ran guest\n")
	for {
		halt()
	}
}
