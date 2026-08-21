package main

// Guest VM: restricted ISA (MOV/LEA/SUB/JZ scalar core + AVX2 vector unit).
// Encoding mirrors kernel/vm/isa.h; programs arrive as .vbin blobs.

import "unsafe"

const (
	opMOV         = 1
	opLEA         = 2
	opSUB         = 3
	opJZ          = 4
	opVMOVDQU     = 5
	opVPBROADCAST = 6
	opVPSUB       = 7
	opVPCMPEQ     = 8
	opHALT        = 0xFF

	consoleOff = 0xF000
	minMem     = 0x10000
)

type insn struct {
	op, dst, src, aux byte
	_                 uint32
	imm               uint64
}

type vm struct {
	r  [16]uint64 // r0 hardwired zero
	v  [16][32]byte
	pc uint64
	zf bool

	code    []byte
	mem     []byte
	memBase uint64 // physical address of mem[0], for the console window check
}

func widthBytes(wsel byte) uint64 { return [...]uint64{1, 2, 4, 8}[wsel&3] }

func runVM(blob []byte) {
	if len(blob) < 32 || string(blob[0:4]) != "VBIN" {
		puts("[vm] bad image magic\n")
		return
	}
	u32 := func(off uint) uint32 {
		return uint32(blob[off]) | uint32(blob[off+1])<<8 | uint32(blob[off+2])<<16 | uint32(blob[off+3])<<24
	}
	entry := u32(8)
	codeOff, codeLen := u32(12), u32(16)
	dataOff, dataLen := u32(20), u32(24)
	if int(codeOff)+int(codeLen) > len(blob) || int(dataOff)+int(dataLen) > len(blob) ||
		entry >= codeLen || entry%16 != 0 {
		puts("[vm] malformed image\n")
		return
	}

	m := &vm{}
	m.code = blob[codeOff : codeOff+codeLen]
	memLen := uint(dataLen)
	if memLen < minMem {
		memLen = minMem
	}
	m.mem = make([]byte, memLen)
	copy(m.mem, blob[dataOff:dataOff+dataLen])
	m.pc = uint64(entry)

	rc := m.run()
	puts("[vm] guest rc=")
	putdec(uint64(-rc))
	puts("\n")
}

func (m *vm) load(ea, w uint64) uint64 {
	var v uint64
	for i := uint64(0); i < w; i++ {
		v |= uint64(m.mem[ea+i]) << (8 * i)
	}
	return v
}

// returns true when the store hit the console window
func (m *vm) store(ea, w, val uint64) bool {
	if ea == consoleOff {
		puts("[vm] out ")
		puthex(val)
		puts("\n")
		return true
	}
	if ea == consoleOff+8 {
		putc(byte(val))
		return true
	}
	for i := uint64(0); i < w; i++ {
		m.mem[ea+i] = byte(val >> (8 * i))
	}
	return false
}

func (m *vm) run() int {
	for {
		if m.pc+16 > uint64(len(m.code)) {
			puts("[vm] pc out of range\n")
			return -1
		}
		in := (*insn)(unsafe.Pointer(&m.code[m.pc]))
		switch in.op {
		case opMOV:
			switch in.aux & 7 {
			case 0:
				if in.dst != 0 {
					m.r[in.dst] = in.imm
				}
			case 1:
				if in.dst != 0 {
					m.r[in.dst] = m.r[in.src]
				}
			case 2:
				ea := m.r[in.src] + in.imm
				if ea+widthBytes(in.aux>>4) > uint64(len(m.mem)) {
					goto fault
				}
				if in.dst != 0 {
					m.r[in.dst] = m.load(ea, widthBytes(in.aux>>4))
				}
			case 3:
				ea := m.r[in.dst] + in.imm
				if ea+widthBytes(in.aux>>4) > uint64(len(m.mem)) {
					goto fault
				}
				m.store(ea, widthBytes(in.aux>>4), m.r[in.src])
			}
			m.pc += 16

		case opLEA:
			if in.dst != 0 {
				m.r[in.dst] = m.r[in.src] + in.imm
			}
			m.pc += 16

		case opSUB:
			var res uint64
			if in.aux&7 == 0 {
				res = m.r[in.dst] - in.imm
			} else {
				res = m.r[in.dst] - m.r[in.src]
			}
			if in.dst != 0 {
				m.r[in.dst] = res
			}
			m.zf = res == 0
			m.pc += 16

		case opJZ:
			if m.zf {
				m.pc = in.imm
			} else {
				m.pc += 16
			}

		case opVMOVDQU:
			ea := m.r[in.src] + in.imm
			if ea+32 > uint64(len(m.mem)) {
				goto fault
			}
			p := unsafe.Pointer(&m.mem[ea])
			if in.aux&0x80 != 0 {
				vecStore(p, unsafe.Pointer(&m.v[in.dst]))
			} else {
				vecLoad(p, unsafe.Pointer(&m.v[in.dst]))
			}
			m.pc += 16

		case opVPBROADCAST:
			vecBcast(m.r[in.src], uint64(in.aux&3), unsafe.Pointer(&m.v[in.dst]))
			m.pc += 16

		case opVPSUB:
			vecSub(unsafe.Pointer(&m.v[in.src]), unsafe.Pointer(&m.v[(in.aux>>4)&15]),
				uint64(in.aux&3), unsafe.Pointer(&m.v[in.dst]))
			m.pc += 16

		case opVPCMPEQ:
			m.zf = vecCmpEqAll(unsafe.Pointer(&m.v[in.src]), unsafe.Pointer(&m.v[(in.aux>>4)&15]),
				uint64(in.aux&3), unsafe.Pointer(&m.v[in.dst]))
			m.pc += 16

		case opHALT:
			puts("[vm] halt at pc=")
			puthex(m.pc)
			puts("\n")
			return 0

		default:
			puts("[vm] illegal op\n")
			return -2
		}
		continue
fault:
		puts("[vm] memfault pc=")
		puthex(m.pc)
		puts("\n")
		return -1
	}
}
