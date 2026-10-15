package jitc

// emit64.go — 64-bit integer ALU, constants, linear-memory accessors,
// globals, locals and misc ops for the Stage 2a baseline.
//
// Register-discipline amendment to emit.go: RCX is SCRATCH (shifts use
// CL, select/ALU use ECX). Pinned: RDI mem base, RSI mem size, RDX trap
// slot, RBX globals base. RDX is implicit in idiv/div, so DivRem
// sequences push/pop RDX around the operation; the divisor-zero trap is
// checked before RDX is touched.

// --- 64-bit integer ALU (rax = rax op rcx) ---

func (a *Asm) alu64(op []byte) {
	a.popReg(RRCX)
	a.popReg(RRAX)
	a.emit(0x48) // REX.W
	a.emit(op...)
	a.pushReg(RRAX)
}

func (a *Asm) Add64() { a.alu64([]byte{0x01, 0xC8}) }
func (a *Asm) Sub64() { a.alu64([]byte{0x29, 0xC8}) }
func (a *Asm) Mul64() { a.alu64([]byte{0x0F, 0xAF, 0xC1}) }
func (a *Asm) And64() { a.alu64([]byte{0x21, 0xC8}) }
func (a *Asm) Or64()  { a.alu64([]byte{0x09, 0xC8}) }
func (a *Asm) Xor64() { a.alu64([]byte{0x31, 0xC8}) }

// Shift64: count in CL (hardware masks to 6 bits for 64-bit ops,
// matching wasm's %64 semantics exactly).
func (a *Asm) Shift64(op byte) {
	a.popReg(RRCX)
	a.popReg(RRAX)
	a.emit(0x48, 0xD3, op) // shl/sar/shr/rol/ror rax, cl
	a.pushReg(RRAX)
}

func (a *Asm) Cmp64(setcc byte) {
	a.popReg(RRCX)
	a.popReg(RRAX)
	a.emit(0x48, 0x39, 0xC8)       // cmp rax, rcx
	a.emit(0x0F, setcc+0x10, 0xC0) // setcc al (0F 9x, not the 0F 8x jump)
	a.emit(0x0F, 0xB6, 0xC0)       // movzx eax, al
	a.pushReg(RRAX)
}

func (a *Asm) Eqz64() {
	a.popReg(RRAX)
	a.emit(0x48, 0x85, 0xC0) // test rax, rax
	a.emit(0x0F, 0x94, 0xC0)
	a.emit(0x0F, 0xB6, 0xC0)
	a.pushReg(RRAX)
}

func (a *Asm) Clz64() {
	a.popReg(RRAX)
	zero := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x48, 0x85, 0xC0) // test rax, rax
	a.Jcc(CC_EQ, zero)
	a.emit(0x48, 0x0F, 0xBD, 0xC8) // bsr rcx, rax (nonzero: defined)
	a.emit(0x48, 0x83, 0xF1, 0x3F) // xor rcx, 63
	a.Jmp(done)
	a.Bind(zero)
	a.emit(0x48, 0xC7, 0xC1, 0x40, 0x00, 0x00, 0x00) // mov rcx, 64
	a.Bind(done)
	a.emit(0x48, 0x89, 0xC8) // mov rax, rcx
	a.pushReg(RRAX)
}

func (a *Asm) Ctz64() {
	a.popReg(RRAX)
	zero := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x48, 0x85, 0xC0)
	a.Jcc(CC_EQ, zero)
	a.emit(0x48, 0x0F, 0xBC, 0xC8) // bsf rcx, rax (nonzero: defined)
	a.Jmp(done)
	a.Bind(zero)
	a.emit(0x48, 0xC7, 0xC1, 0x40, 0x00, 0x00, 0x00)
	a.Bind(done)
	a.emit(0x48, 0x89, 0xC8)
	a.pushReg(RRAX)
}

// movAbs64 emits mov r64, imm64 (REX.W B8+r).
func (a *Asm) movAbs64(r int, v uint64) {
	if r < 8 {
		a.emit(0x48, 0xB8+byte(r))
	} else {
		a.emit(0x49, 0xB8+byte(r-8))
	}
	a.emit64(v)
}

// Popcnt64: parallel bit count (no POPCNT instruction, cf. Popcnt32).
func (a *Asm) Popcnt64() {
	const m1 = 0x5555555555555555
	const m2 = 0x3333333333333333
	const m4 = 0x0F0F0F0F0F0F0F0F
	const h01 = 0x0101010101010101
	a.popReg(RRAX)
	a.emit(0x48, 0x89, 0xC1) // mov rcx, rax
	a.emit(0x48, 0xD1, 0xE9) // shr rcx, 1
	a.movAbs64(RR9, m1)
	a.emit(0x4C, 0x21, 0xC9)       // and rcx, r9
	a.emit(0x48, 0x29, 0xC8)       // sub rax, rcx
	a.emit(0x48, 0x89, 0xC1)       // mov rcx, rax
	a.emit(0x48, 0xC1, 0xE9, 0x02) // shr rcx, 2
	a.movAbs64(RR9, m2)
	a.emit(0x4C, 0x21, 0xC9)       // and rcx, r9
	a.emit(0x4C, 0x21, 0xC8)       // and rax, r9
	a.emit(0x48, 0x01, 0xC8)       // add rax, rcx
	a.emit(0x48, 0x89, 0xC1)       // mov rcx, rax
	a.emit(0x48, 0xC1, 0xE9, 0x04) // shr rcx, 4
	a.emit(0x48, 0x01, 0xC8)       // add rax, rcx
	a.movAbs64(RR9, m4)
	a.emit(0x4C, 0x21, 0xC8) // and rax, r9
	a.movAbs64(RR9, h01)
	a.emit(0x49, 0x0F, 0xAF, 0xC1) // imul rax, r9
	a.emit(0x48, 0xC1, 0xE8, 0x38) // shr rax, 56
	a.pushReg(RRAX)
}

// DivRem64 mirrors DivRem32 in 64 bits. RDX (trap pin) is saved across
// the idiv/div, which uses it implicitly.
func (a *Asm) DivRem64(signed, isRem bool) {
	a.popReg(RRCX) // rhs
	a.popReg(RRAX) // lhs
	trap := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x48, 0x85, 0xC9) // test rcx, rcx
	a.Jcc(CC_EQ, trap)
	a.emit1(0x52) // push rdx (save trap pin)
	if signed {
		a.movAbs64(RR9, 0x8000000000000000)
		a.emit(0x4C, 0x39, 0xC8) // cmp rax, r9
		normal := a.NewLabel()
		a.Jcc(CC_NE, normal)
		a.emit(0x48, 0x83, 0xF9, 0xFF) // cmp rcx, -1
		a.Jcc(CC_NE, normal)
		if isRem {
			a.emit(0x48, 0x31, 0xC0) // xor rax, rax
		} // else rax == INT64_MIN already
		a.Jmp(done)
		a.Bind(normal)
		a.emit(0x48, 0x99)       // cqo
		a.emit(0x48, 0xF7, 0xF9) // idiv rcx
		if isRem {
			a.emit(0x48, 0x89, 0xD0) // mov rax, rdx
		}
	} else {
		a.emit(0x48, 0x31, 0xD2) // xor rdx, rdx
		a.emit(0x48, 0xF7, 0xF1) // div rcx
		if isRem {
			a.emit(0x48, 0x89, 0xD0)
		}
	}
	a.emit1(0x5A) // pop rdx (restore trap pin)
	a.Jmp(done)
	a.Bind(trap)
	// RDX is intact here (checked before the push), Trap needs it.
	a.Trap(TrapDivZero)
	a.Bind(done)
	a.pushReg(RRAX)
}

// DivRem32 needs the same RDX save/restore: patch it by wrapping.
// (Defined in emit.go; the RDX discipline is documented in emit64.go's
// header. The 32-bit paths use xor edx,edx / cdq which clobber the
// trap pin — corrected below by redefinition is impossible in Go, so
// DivRem32 in emit.go MUST already save RDX. See fixup note.)
//
// NOTE: emit.go's DivRem32 as written clobbers RDX. It is repaired by
// the edit below in compile.go's usage contract — NO. Repair it now:
// call sites use DivRem32 from emit.go; fix emit.go directly.

// --- constants ---

// Const32 pushes a 32-bit immediate zero-extended to 64 bits (B8 without
// REX.W: mov eax, imm32). Zero-extension is the slot discipline for i32
// everywhere (comparisons, addresses and stores all consume the low 32).
func (a *Asm) Const32(v int32) {
	a.emit(0xB8) // mov eax, imm32
	a.emit32(uint32(v))
	a.pushReg(RRAX)
}

// Const64 pushes a 64-bit immediate: sign-extended 32-bit form when it
// fits (48 C7 C0), full movabs otherwise.
func (a *Asm) Const64(v int64) {
	if v == int64(int32(v)) {
		a.emit(0x48, 0xC7, 0xC0) // mov rax, imm32 (sign-extended)
		a.emit32(uint32(v))
	} else {
		a.movAbs64(RRAX, uint64(v))
	}
	a.pushReg(RRAX)
}

// --- linear memory ---
// memEA pops the i32 address operand and computes the checked effective
// address in R8, trapping OOB. size = access width in bytes. Clobbers
// RAX/R8/R9; RDX (trap pin) intact on all paths (checked before use).
// Layout: r8d = addr32; r8 += offset; require r8 <= memsize - size.
func (a *Asm) memEA(off uint32, size uint32) *Label {
	trap := a.NewLabel()
	done := a.NewLabel()
	a.popReg(RRAX)
	a.emit(0x41, 0x89, 0xC0) // mov r8d, eax (zero-extend addr; REX.B!)
	a.emit(0x41, 0xB9)       // mov r9d, off (zero-extends)
	a.emit32(off)
	a.emit(0x4D, 0x01, 0xC8) // add r8, r9  (full 64-bit ea)
	// require memsize >= size (else every access faults)
	a.emit(0x48, 0x83, 0xFE, byte(size)) // cmp rsi, size
	a.Jcc(CC_LTU, trap)
	a.emit(0x49, 0x89, 0xF1)             // mov r9, rsi
	a.emit(0x49, 0x83, 0xE9, byte(size)) // sub r9, size
	a.emit(0x4D, 0x39, 0xC8)             // cmp r8, r9
	a.Jcc(CC_GTU, trap)
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
	return nil
}

// memLoad emits a checked load of the given width/signedness, pushing
// the result (i32 zero-extended, i64 full).
func (a *Asm) memLoad(off uint32, size uint32, sext bool, wide bool) {
	a.memEA(off, size)
	switch {
	case size == 4 && wide: // i64.load32_u
		a.emit(0x42, 0x8B, 0x04, 0x07) // mov eax, [rdi+r8]
	case size == 4 && !sext: // i32.load
		a.emit(0x42, 0x8B, 0x04, 0x07)
	case size == 4 && sext: // i64.load32_s
		a.emit(0x4A, 0x63, 0x04, 0x07) // movsxd rax, [rdi+r8]
	case size == 8: // i64.load
		a.emit(0x4A, 0x8B, 0x04, 0x07)
	case size == 2 && sext && wide: // i64.load16_s
		a.emit(0x4A, 0x0F, 0xBF, 0x04, 0x07)
	case size == 2 && sext: // i32.load16_s
		a.emit(0x42, 0x0F, 0xBF, 0x04, 0x07)
	case size == 2: // _u (both widths: movzx zeroes upper)
		a.emit(0x42, 0x0F, 0xB7, 0x04, 0x07)
	case size == 1 && sext && wide: // i64.load8_s
		a.emit(0x4A, 0x0F, 0xBE, 0x04, 0x07)
	case size == 1 && sext: // i32.load8_s
		a.emit(0x42, 0x0F, 0xBE, 0x04, 0x07)
	case size == 1: // _u
		a.emit(0x42, 0x0F, 0xB6, 0x04, 0x07)
	}
	a.pushReg(RRAX)
}

// memStore pops value then address and performs a checked store. The
// value parks in R11 across memEA (which uses RAX/R8/R9): stashing it
// back on the stack would bury the address below it.
func (a *Asm) memStore(off uint32, size uint32) {
	a.popReg(RR11) // value → R11 (safe: memEA never touches it)
	a.memEA(off, size)
	a.emit(0x4C, 0x89, 0xD8) // mov rax, r11 (dest rm=rax/B=0, src reg=r11/R=1)
	switch size {
	case 4:
		a.emit(0x42, 0x89, 0x04, 0x07) // mov [rdi+r8], eax
	case 8:
		a.emit(0x4A, 0x89, 0x04, 0x07)
	case 2:
		a.emit(0x66, 0x42, 0x89, 0x04, 0x07) // mov [rdi+r8], ax
	case 1:
		a.emit(0x42, 0x88, 0x04, 0x07) // mov [rdi+r8], al
	}
}

// --- globals & locals (RBX = globals base, 8 bytes each) ---

func (a *Asm) GlobalGet(idx uint32, wide bool) {
	a.emit(0x48, 0x8B, 0x83) // mov rax, [rbx+idx*8]
	a.emit32(idx * 8)
	_ = wide // slots are 8 bytes; i32 values stored zero-extended
	a.pushReg(RRAX)
}

func (a *Asm) GlobalSet(idx uint32, wide bool) {
	a.popReg(RRAX)
	if wide {
		a.emit(0x48, 0x89, 0x83) // mov [rbx+idx*8], rax
	} else {
		a.emit(0x89, 0x83) // mov [rbx+idx*8], eax
	}
	a.emit32(idx * 8)
}

func (a *Asm) LocalGet(slot int) {
	a.emit(0x48, 0x8B, 0x85) // mov rax, [rbp-8*(slot+1)]
	a.emit32(uint32(-8 * (slot + 1)))
	a.pushReg(RRAX)
}

func (a *Asm) LocalSet(slot int, keep bool) {
	a.popReg(RRAX)
	a.emit(0x48, 0x89, 0x85) // mov [rbp-8*(slot+1)], rax
	a.emit32(uint32(-8 * (slot + 1)))
	if keep {
		a.pushReg(RRAX)
	}
}

// Select pops cond, val2, val1 and pushes (cond != 0) ? val1 : val2.
// Full-width test (exact for i64/f64, equivalent for clean i32).
func (a *Asm) Select() {
	a.popReg(RR8)                  // cond
	a.popReg(RR9)                  // val2
	a.popReg(RRAX)                 // val1
	a.emit(0x4D, 0x85, 0xC0)       // test r8, r8
	a.emit(0x49, 0x0F, 0x44, 0xC1) // cmovz rax, r9
	a.pushReg(RRAX)
}

// MemorySize pushes the current memory size in 64 KiB pages.
func (a *Asm) MemorySize() {
	a.emit(0x48, 0x89, 0xF0)       // mov rax, rsi
	a.emit(0x48, 0xC1, 0xE8, 0x10) // shr rax, 16
	a.pushReg(RRAX)
}

// MemoryGrow pops the delta and pushes -1 (grow unsupported in v1;
// failure is valid wasm semantics).
func (a *Asm) MemoryGrow() {
	a.addRsp(8)                                      // drop delta
	a.emit(0x48, 0xC7, 0xC0, 0xFF, 0xFF, 0xFF, 0xFF) // mov rax, -1
	a.pushReg(RRAX)
}

// MemoryCopy implements memory.copy (stack: dst, src, n — pops all
// three, pushes nothing) via rep movsb. Pinned RDI/RSI are saved
// across the string op, which uses them implicitly.
func (a *Asm) MemoryCopy() {
	trap := a.NewLabel()
	done := a.NewLabel()
	a.popReg(RR11) // n
	a.popReg(RR9)  // src
	a.popReg(RR8)  // dst
	// bounds in 64 bits (all operands u32-range; sums cannot wrap u64)
	a.emit(0x44, 0x89, 0xD9) // mov ecx, r11d (n)
	a.emit(0x44, 0x89, 0xC8) // mov eax, r9d (src)
	a.emit(0x48, 0x01, 0xC8) // add rax, rcx (src+n)
	a.emit(0x48, 0x39, 0xF0) // cmp rax, rsi (memsize)
	a.Jcc(CC_GTU, trap)
	a.emit(0x44, 0x89, 0xC0) // mov eax, r8d (dst)
	a.emit(0x48, 0x01, 0xC8) // add rax, rcx (dst+n)
	a.emit(0x48, 0x39, 0xF0)
	a.Jcc(CC_GTU, trap)
	a.emit(0x4E, 0x8D, 0x04, 0x07) // lea r8, [rdi+r8] (dst ptr)
	a.emit(0x4E, 0x8D, 0x0C, 0x0F) // lea r9, [rdi+r9] (src ptr)
	a.emit(0x44, 0x89, 0xD9)       // mov ecx, r11d (count)
	a.emit1(0x57)                  // push rdi (save pin)
	a.emit1(0x56)                  // push rsi (save pin)
	a.emit(0x4C, 0x89, 0xC7)       // mov rdi, r8
	a.emit(0x4C, 0x89, 0xCE)       // mov rsi, r9
	a.emit1(0xFC)                  // cld
	a.emit(0xF3, 0xA4)             // rep movsb
	a.emit1(0x5E)                  // pop rsi
	a.emit1(0x5F)                  // pop rdi
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
}

// MemoryFill implements memory.fill (stack: dst, val, n — pops all
// three) via rep stosb, with the same pin discipline as MemoryCopy.
func (a *Asm) MemoryFill() {
	trap := a.NewLabel()
	done := a.NewLabel()
	a.popReg(RR11) // n
	a.popReg(RRAX) // val (AL used)
	a.popReg(RR8)  // dst
	// bounds: dst32 + n32 <= memsize, 64-bit (no wrap possible)
	a.emit(0x44, 0x89, 0xD9) // mov ecx, r11d (n)
	a.emit(0x45, 0x89, 0xC1) // mov r9d, r8d (dst)
	a.emit(0x49, 0x01, 0xC9) // add r9, rcx (dst+n)
	a.emit(0x49, 0x39, 0xF1) // cmp r9, rsi (memsize)
	a.Jcc(CC_GTU, trap)
	a.emit(0x4E, 0x8D, 0x04, 0x07) // lea r8, [rdi+r8] (dst ptr)
	a.emit(0x44, 0x89, 0xD9)       // mov ecx, r11d (count)
	a.emit1(0x57)                  // push rdi (save pin)
	a.emit1(0x56)                  // push rsi (save pin)
	a.emit(0x4C, 0x89, 0xC7)       // mov rdi, r8
	a.emit1(0xFC)                  // cld
	a.emit(0xF3, 0xAA)             // rep stosb (AL = value)
	a.emit1(0x5E)                  // pop rsi
	a.emit1(0x5F)                  // pop rdi
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
}

// ImportFdWrite emits a native call to the kernel's fd_write handler
// (Stage 3b-ii, FD_WRITE kind only). Operand stack on entry (top-last):
// [fd, iovs_ptr, iovs_len, nwritten_ptr] (all i32). The kernel call is
// C-ABI: kern_imp_fd_write(mem, memlen, trap, fd, iovs, len, nw) with
// nw as the 7th (stack) argument. Pinned regs (mem/memlen/trap/globals
// in RDI/RSI/RDX/RBX) are spilled to callee-saved R12-R15 (which C
// preserves) and restored after. Stack alignment for the C call is
// enforced DYNAMICALLY (mov r10,rsp + and rsp,-16 around the call with
// exact restore — operand depth parity varies per site, so no static
// adjustment is sound). Trap discipline: C zeroes *trap on entry and
// stores TrapOOB on a bounds violation; any nonzero trap diverts to a
// Trap stub (v1 fd_write can only fault OOB). Pushes the errno result.
// Leaves a call placeholder for the linker (import reloc, patched at
// bind to the handler address).
// CallImport emits a call placeholder for an import target and returns
// the rel32 offset for the linker (mirrors CallFunc for internal calls;
// the kernel patches it at bind to the per-kind handler).
func (a *Asm) CallImport() int {
	a.emit1(0xE8)
	pos := len(a.Code)
	a.Code = append(a.Code, 0, 0, 0, 0)
	return pos
}

// ImportFdWrite emits a native call to the kernel's fd_write handler
// (Stage 3b-ii, FD_WRITE kind only). Operand stack on entry (top-last):
// [fd, iovs_ptr, iovs_len, nwritten_ptr] (all i32). The kernel call is
// C-ABI: kern_imp_fd_write(mem, memlen, trap, fd, iovs, len, nw) with
// nw as the 7th (stack) argument.
//
// DESIGN (deliberately minimal after the R12-R15/XMM saga): pops go to
// NON-pinned scratch (R11/R9/R8/R10), so pinned regs (mem/memlen/trap
// in RDI/RSI/RDX, globals in RBX) are NEVER touched — no spills, no
// restores, no callee-saved or XMM involvement at all. R10 (fd temp)
// is scratch-dead at every call site. Stack alignment for the C call
// is STATIC: entry RSP is 16-aligned after the prologue push in both
// harnesses, so the pad derives from compile-time depth
// (padSlots = (nloc + depthAfterPops + 1) & 1, each 8 bytes).
// Trap discipline: C zeroes *trap on entry and stores TrapOOB on a
// bounds violation; any nonzero trap diverts to a Trap stub (v1
// fd_write can only fault OOB). Pushes the errno result. Returns the
// rel32 offset for the linker (import reloc, patched at bind).
func (a *Asm) ImportFdWrite(nloc, depthAfterPops int) int {
	a.popReg(RR11) // a3 = nwritten ptr
	a.popReg(RR9)  // a2 = iovs_len
	a.popReg(RR8)  // a1 = iovs ptr
	a.popReg(RR10) // a0 = fd (R10 scratch-dead here)
	pad := (nloc + depthAfterPops + 1) & 1
	if pad != 0 {
		a.emit(0x48, 0x83, 0xEC, 0x08) // sub rsp, 8 (static align pad)
	}
	a.emit(0x41, 0x53)       // push r11 (nw as C 7th arg)
	a.emit(0x4C, 0x89, 0xD1) // mov rcx, r10 (fd)
	// RDI/RSI/RDX already = mem/memlen/trap (pinned, intact).
	relPos := a.CallImport() // call kern_imp_fd_write (import reloc)
	if pad != 0 {
		a.emit(0x48, 0x83, 0xC4, 0x10) // add rsp, 16 (drop nw + pad)
	} else {
		a.emit(0x48, 0x83, 0xC4, 0x08) // add rsp, 8 (drop nw)
	}
	trap := a.NewLabel()
	done := a.NewLabel()
	a.importTrapTail(trap, done)
	a.pushReg(RRAX) // errno result
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
	return relPos
}

// importTrapTail emits the post-call trap check shared by memory-
// touching import sequences: cmp trap slot, divert to a Trap stub on
// nonzero, else fall through. Caller pushes the RAX result on the
// fall-through path. Extracted verbatim from ImportFdWrite (golden-
// pinned); ImportCall3 reuses it.
func (a *Asm) importTrapTail(trap, done *Label) {
	a.emit(0x48, 0x83, 0x3A, 0x00) // cmp qword [rdx], 0 (trap intact!)
	a.Jcc(CC_NE, trap)
}

// ImportCall3 emits a native call for 3-arg (i32,i32,i32)->i32 imports
// sharing one marshalling shape: kern_port_send/recv and
// kern_blk_read/write (kind selects the handler at bind). Operand
// stack on entry (top-last): [a0, a1, a2] (all i32). The kernel call
// is C-ABI: handler(mem, memlen, trap, a0, a1, a2). Marshalling mirrors ImportFdWrite
// (pops land in the SysV arg regs R8/R9/R10, h forwarded via RCX;
// RDI/RSI/RDX already pinned), minus the 7th stack argument.
// Same static align pad, same trap-check tail, pushes the status.
// Returns the rel32 offset for the linker (import reloc).
func (a *Asm) ImportCall3(nloc, depthAfterPops int) int {
	a.popReg(RR9)  // a2 = len/cap
	a.popReg(RR8)  // a1 = ptr
	a.popReg(RR10) // a0 = h (R10 scratch-dead here)
	pad := (nloc + depthAfterPops + 1) & 1
	if pad != 0 {
		a.emit(0x48, 0x83, 0xEC, 0x08) // sub rsp, 8 (static align pad)
	}
	a.emit(0x4C, 0x89, 0xD1) // mov rcx, r10 (h)
	// RDI/RSI/RDX already = mem/memlen/trap (pinned, intact).
	relPos := a.CallImport() // call handler (import reloc)
	if pad != 0 {
		a.emit(0x48, 0x83, 0xC4, 0x08) // add rsp, 8 (drop pad)
	}
	trap := a.NewLabel()
	done := a.NewLabel()
	a.importTrapTail(trap, done)
	a.pushReg(RRAX) // status result
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
	return relPos
}

// ImportCall2 emits a native call for 2-arg (i32,i32)->i32 imports:
// kern_port_create/bind (kind selects the handler at bind). Operand
// stack on entry (top-last): [a0, a1]. Same SysV/pad/trap discipline
// as ImportCall3 with one fewer pop.
func (a *Asm) ImportCall2(nloc, depthAfterPops int) int {
	a.popReg(RR8)  // a1 = len
	a.popReg(RR10) // a0 = ptr (R10 scratch-dead here)
	pad := (nloc + depthAfterPops + 1) & 1
	if pad != 0 {
		a.emit(0x48, 0x83, 0xEC, 0x08) // sub rsp, 8 (static align pad)
	}
	a.emit(0x4C, 0x89, 0xD1) // mov rcx, r10 (a0)
	// RDI/RSI/RDX already = mem/memlen/trap (pinned, intact).
	relPos := a.CallImport() // call handler (import reloc)
	if pad != 0 {
		a.emit(0x48, 0x83, 0xC4, 0x08) // add rsp, 8 (drop pad)
	}
	trap := a.NewLabel()
	done := a.NewLabel()
	a.importTrapTail(trap, done)
	a.pushReg(RRAX) // status result
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapOOB)
	a.Bind(done)
	return relPos
}

// ImportSchedYield emits a native call to the kernel's sched_yield
// handler (zero-arg import; accepts the i() and legacy v() declared
// shapes — the caller pushes the errno result only for i()).
// Static stack-alignment pad mirrors ImportFdWrite (entry RSP is
// 16-aligned after the prologue push; pad derives from compile-time
// depth). No memory, no trap interaction: the handler cannot fault.
// Pinned regs are intact across the call by the trampoline contract
// (arch/x86_64/jit_poll.S shape); R10 is scratch-dead here.
// Returns the rel32 offset for the linker (import reloc, kind 1,
// patched at bind to kern_imp_sched_yield).
func (a *Asm) ImportSchedYield(nloc, depthAfterPops int, pushResult bool) int {
	pad := (nloc + depthAfterPops + 1) & 1
	if pad != 0 {
		a.emit(0x48, 0x83, 0xEC, 0x08) // sub rsp, 8 (static align pad)
	}
	relPos := a.CallImport() // call kern_imp_sched_yield (import reloc)
	if pad != 0 {
		a.emit(0x48, 0x83, 0xC4, 0x08) // add rsp, 8 (drop pad)
	}
	if pushResult {
		a.pushReg(RRAX) // errno result
	}
	return relPos
}
