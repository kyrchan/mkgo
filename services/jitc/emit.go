package jitc

import "fmt"

// emit.go — minimal x86_64 assembler for the Stage 2a baseline JIT.
//
// Custom native calling convention (host-test harness AND the future
// kernel loader agree on this; it is NOT SysV, and emitted code never
// calls SysV functions, so the usual ABI constraints do not apply):
//
//	RDI = linear memory base (pinned, never written)
//	RSI = linear memory size in bytes (pinned)
//	RDX = trap-flag pointer (pinned; 0 = running)
//	RBX = globals base, 8 bytes per global (pinned)
//	R10 = args pointer (set by caller before CALL, read once at entry)
//	R11 = nargs (set by caller before CALL, read once at entry)
//	RAX, R8, R9, R11 = scratch; native stack = wasm operand stack
//	    (8-byte slots; i32 values zero-extended by discipline)
//	RCX = scratch (shifts use CL; NOT pinned)
//	RBP = frame pointer; locals at [RBP-8*(i+1)]
//
// RDX discipline: idiv/div use RDX implicitly, so DivRem sequences push
// RDX on entry and pop it before joining; the divisor-zero trap is
// checked before RDX is touched, so Trap always sees the pin intact.
//
// Traps (unreachable, OOB, div-by-zero, bad indirect/import call) reset
// RSP to RBP, store a nonzero code at [RDX], restore RBP and return.
// The Go trampoline (call_amd64.s) reports RAX + the trap slot.
//
// Alignment note: CALLs happen with arbitrary operand-stack depth, so
// RSP is not 16-aligned at call boundaries. Harmless here: emitted
// code issues no aligned-SSE memory ops and calls no SysV code.

// Register numbers.
const (
	RRAX = 0
	RRCX = 1
	RRDX = 2
	RRBX = 3
	RRSP = 4
	RRBP = 5
	RRSI = 6
	RRDI = 7
	RR8  = 8
	RR9  = 9
	RR10 = 10
	RR11 = 11
)

// Condition codes for Jcc (rel32 form 0F xx).
const (
	CC_EQ  = 0x84 // zr
	CC_NE  = 0x85 // nz
	CC_LTU = 0x82 // unsigned <
	CC_GEU = 0x83 // unsigned >=
	CC_LEU = 0x86 // unsigned <=
	CC_GTU = 0x87 // unsigned >
	CC_LTS = 0x8C // signed <
	CC_GES = 0x8D // signed >=
	CC_LES = 0x8E // signed <=
	CC_GTS = 0x8F // signed >
)

// Trap codes reported in the trap slot.
const (
	TrapUnreachable = 1
	TrapOOB         = 2
	TrapDivZero     = 3
	TrapBadCall     = 4
	TrapConv        = 5 // float→int conversion failure (NaN/range)
)

// Label is a bindable jump target within one emitted function.
type Label struct {
	pos   int
	bound bool
}

// fixup is a rel32 site patched when its label binds.
type fixup struct {
	pos int
	lbl *Label
}

// CallSite is a cross-function call patched by the linker once every
// function has a base offset in the executable region.
type CallSite struct {
	Pos    int    // offset of the rel32 word in this buffer
	Target uint32 // callee function index (module-wide, imports incl.)
}

// Asm is a single function's code buffer plus its fixups.
type Asm struct {
	Code  []byte
	fix   []fixup
	Calls []CallSite
	// Polls holds buffer-relative offsets of safe-point rel32 words
	// emitted by Safepoint (linkers resolve them to absolute).
	Polls []int
}

func (a *Asm) emit1(b byte)    { a.Code = append(a.Code, b) }
func (a *Asm) emit(bs ...byte) { a.Code = append(a.Code, bs...) }
func (a *Asm) emit32(v uint32) {
	a.Code = append(a.Code, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
func (a *Asm) emit64(v uint64) {
	a.Code = append(a.Code, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}
func (a *Asm) Pos() int { return len(a.Code) }

func (a *Asm) NewLabel() *Label { return &Label{pos: -1} }

// patchAt writes the rel32 from relPos (offset of the rel32 word) to
// the label position.
func (a *Asm) patchAt(relPos, lblPos int) {
	rel := int32(lblPos - (relPos + 4))
	a.Code[relPos] = byte(rel)
	a.Code[relPos+1] = byte(rel >> 8)
	a.Code[relPos+2] = byte(rel >> 16)
	a.Code[relPos+3] = byte(rel >> 24)
}

// Bind resolves a label to the current position and patches all
// pending jumps to it. Double-bind is a compiler bug.
func (a *Asm) Bind(l *Label) error {
	if l.bound {
		return fmt.Errorf("jitc: label double-bind")
	}
	l.bound = true
	l.pos = len(a.Code)
	kept := a.fix[:0]
	for _, f := range a.fix {
		if f.lbl == l {
			a.patchAt(f.pos, l.pos)
		} else {
			kept = append(kept, f)
		}
	}
	a.fix = kept
	return nil
}

func (a *Asm) jmpTo(op []byte, l *Label) {
	for _, b := range op {
		a.Code = append(a.Code, b)
	}
	pos := len(a.Code)
	a.Code = append(a.Code, 0, 0, 0, 0)
	if l.bound {
		// Backward jump: the target is already known, patch now.
		// (Bind only patches fixups pending AT bind time, so without
		// this, jumps to loop tops would dangle forever.)
		a.patchAt(pos, l.pos)
		return
	}
	a.fix = append(a.fix, fixup{pos: pos, lbl: l})
}

// Jmp emits an unconditional jump to l.
func (a *Asm) Jmp(l *Label) { a.jmpTo([]byte{0xE9}, l) }

// Jcc emits a conditional jump (CC_* opcode 0F xx) to l.
func (a *Asm) Jcc(cc byte, l *Label) { a.jmpTo([]byte{0x0F, cc}, l) }

// CallFunc emits a call placeholder patched by Link with the callee's
// offset. target is the module-wide function index.
func (a *Asm) CallFunc(target uint32) {
	a.emit1(0xE8)
	a.Calls = append(a.Calls, CallSite{Pos: len(a.Code), Target: target})
	a.Code = append(a.Code, 0, 0, 0, 0)
}

// Safepoint emits a preemption-poll call placeholder. The linker (host
// harness or kernel bind) patches the rel32 to the poll trampoline.
// Emission points (compileFunc: function entry, loop tops) are op
// BOUNDARIES: the only live state is pinned regs (RDI/RSI/RDX mem,
// memlen, trap; RBX globals) plus memory slots. The trampoline ABI
// preserves pinned regs, RSP, RBP, R12-R15 and may clobber RAX, RCX,
// R8-R11 (dead at every emission point by the depth-tracked stack
// model; XMM is op-transient, spilled to slots between ops).
// Returns the buffer-relative rel32 offset for the poll table.
func (a *Asm) Safepoint() int {
	a.emit1(0xE8)
	pos := len(a.Code)
	a.Code = append(a.Code, 0, 0, 0, 0)
	a.Polls = append(a.Polls, pos)
	return pos
}

// --- stack slots (operand stack = native stack) ---

func (a *Asm) pushReg(r int) {
	if r < 8 {
		a.emit1(0x50 + byte(r))
	} else {
		a.emit(0x41, 0x50+byte(r-8))
	}
}

func (a *Asm) popReg(r int) {
	if r < 8 {
		a.emit1(0x58 + byte(r))
	} else {
		a.emit(0x41, 0x58+byte(r-8))
	}
}

// addRsp emits rsp += n (n >= 0).
func (a *Asm) addRsp(n int) {
	if n == 0 {
		return
	}
	if n < 128 {
		a.emit(0x48, 0x83, 0xC4, byte(n))
	} else {
		a.emit(0x48, 0x81, 0xC4)
		a.emit32(uint32(n))
	}
}

// --- frame ---

// Prologue reserves nloc 8-byte local slots and sets up the frame.
// Parameter zeroing and non-parameter zeroing are split because CopyParams
// reads from the args bounce area which may overlap a non-parameter slot
// when locals >= 5 — zeroing must happen AFTER CopyParams reads the args.
func (a *Asm) Prologue(nloc int) {
	a.emit1(0x55)            // push rbp
	a.emit(0x48, 0x89, 0xE5) // mov rbp, rsp
	if nloc > 0 {
		a.emit(0x48, 0x81, 0xEC) // sub rsp, nloc*8
		a.emit32(uint32(nloc * 8))
	}
}

// ZeroNonParamLocals zeros local slots from index nparams to nloc-1.
// Must be called AFTER CopyParams, since the args bounce area (passed
// via R10 by the caller) may overlap a non-parameter local slot, and
// zeroing it before CopyParams reads it would clobber the parameter.
func (a *Asm) ZeroNonParamLocals(nloc, nparams int) {
	if nparams >= nloc {
		return
	}
	a.emit(0x31, 0xC0) // xor eax, eax
	for i := nparams; i < nloc; i++ {
		// mov [rbp-8*(i+1)], rax
		a.emit(0x48, 0x89, 0x85)
		a.emit32(uint32(-8 * (i + 1)))
	}
}

// CopyParams moves nargs 8-byte args from [R10] (caller set R10 = rsp
// at call time, so arg i sits at [R10+8*(nargs-1-i)]) into frame slots
// 0..nargs-1. Must run before any CALL (which would clobber R10).
// types[i] selects the load width: i32/f32 params load 32 bits
// (zero-extending — kills upper dirt at the boundary, so every i32
// slot in the function starts clean); i64/f64 load full 64 bits.
func (a *Asm) CopyParams(types []byte) {
	for i, t := range types {
		if t == 0x7F || t == 0x7D {
			// mov eax, [r10+8*(nargs-1-i)] (zero-extends)
			a.emit(0x41, 0x8B, 0x82)
			a.emit32(uint32(8 * (len(types) - 1 - i)))
		} else {
			// mov rax, [r10+8*(nargs-1-i)]
			a.emit(0x49, 0x8B, 0x82)
			a.emit32(uint32(8 * (len(types) - 1 - i)))
		}
		// mov [rbp-8*(i+1)], rax
		a.emit(0x48, 0x89, 0x85)
		a.emit32(uint32(-8 * (i + 1)))
	}
}

// Epilogue restores the frame and returns (result already in RAX).
func (a *Asm) Epilogue() {
	a.emit(0x48, 0x89, 0xEC) // mov rsp, rbp
	a.emit1(0x5D)            // pop rbp
	a.emit1(0xC3)            // ret
}

// Trap emits: [rdx] = code (dword store; slot pre-zeroed), unwind, ret.
func (a *Asm) Trap(code uint32) {
	a.emit(0xC7, 0x02) // mov dword [rdx], imm32
	a.emit32(code)
	a.emit(0x48, 0x89, 0xEC) // mov rsp, rbp
	a.emit1(0x5D)            // pop rbp
	a.emit(0x31, 0xC0)       // xor eax, eax (defined RAX on trap)
	a.emit1(0xC3)            // ret
}

// Result1 loads the top-of-stack value into RAX for a 1-result return.
func (a *Asm) Result1() {
	a.emit1(0x58) // pop rax
}

// --- 32-bit integer ALU (eax = eax op ecx; operands popped) ---

func (a *Asm) pop2() {
	a.popReg(RRCX) // rhs
	a.popReg(RRAX) // lhs
}

func (a *Asm) alu32(op []byte) {
	a.pop2()
	a.emit(op...)
	a.pushReg(RRAX)
}

func (a *Asm) Add32() { a.alu32([]byte{0x01, 0xC8}) } // add eax, ecx
func (a *Asm) Sub32() { a.alu32([]byte{0x29, 0xC8}) } // sub eax, ecx
func (a *Asm) Mul32() { a.alu32([]byte{0x0F, 0xAF, 0xC1}) }
func (a *Asm) And32() { a.alu32([]byte{0x21, 0xC8}) }
func (a *Asm) Or32()  { a.alu32([]byte{0x09, 0xC8}) }
func (a *Asm) Xor32() { a.alu32([]byte{0x31, 0xC8}) }

// Shift32: count in ECX (hardware masks to 5 bits for 32-bit ops,
// matching wasm's %32 semantics exactly).
func (a *Asm) Shift32(op byte) {
	a.pop2()
	a.emit(0xD3, op) // shl/sar/shr/rol/ror eax, cl
	a.pushReg(RRAX)
}

// Cmp32 compares eax vs ecx and pushes 0/1 per the condition. cc is a
// CC_* jump code (0F 8x); the SETcc form is exactly cc+0x10 (0F 9x).
func (a *Asm) Cmp32(setcc byte) {
	a.pop2()
	a.emit(0x39, 0xC8)             // cmp eax, ecx
	a.emit(0x0F, setcc+0x10, 0xC0) // setcc al
	a.emit(0x0F, 0xB6, 0xC0)       // movzx eax, al
	a.pushReg(RRAX)
}

// Eqz32 pushes (eax == 0).
func (a *Asm) Eqz32() {
	a.popReg(RRAX)
	a.emit(0x85, 0xC0)       // test eax, eax
	a.emit(0x0F, 0x94, 0xC0) // setz al
	a.emit(0x0F, 0xB6, 0xC0) // movzx eax, al
	a.pushReg(RRAX)
}

// Clz32/Ctz32 via bsr/bsf. The bit-scan is emitted AFTER the zero
// branch: bsr/bsf overwrite ZF (and leave dest undefined on zero
// input), so branching on an earlier TEST past the scan is wrong on
// CPUs where bsr(0) clears ZF. Branch first, scan only nonzero.
func (a *Asm) Clz32() {
	a.popReg(RRAX)
	zero := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x85, 0xC0) // test eax, eax
	a.Jcc(CC_EQ, zero)
	a.emit(0x0F, 0xBD, 0xC8) // bsr ecx, eax (nonzero: defined)
	a.emit(0x83, 0xF1, 0x1F) // xor ecx, 31
	a.Jmp(done)
	a.Bind(zero)
	a.emit(0xB9, 0x20, 0x00, 0x00, 0x00) // mov ecx, 32
	a.Bind(done)
	a.emit(0x89, 0xC8) // mov eax, ecx
	a.pushReg(RRAX)
}

func (a *Asm) Ctz32() {
	a.popReg(RRAX)
	zero := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x85, 0xC0) // test eax, eax
	a.Jcc(CC_EQ, zero)
	a.emit(0x0F, 0xBC, 0xC8) // bsf ecx, eax (nonzero: defined)
	a.Jmp(done)
	a.Bind(zero)
	a.emit(0xB9, 0x20, 0x00, 0x00, 0x00) // mov ecx, 32
	a.Bind(done)
	a.emit(0x89, 0xC8) // mov eax, ecx
	a.pushReg(RRAX)
}

// Popcnt32: Hacker's Delight parallel count (avoids the POPCNT
// instruction so the baseline runs on any x86_64 host).
func (a *Asm) Popcnt32() {
	a.popReg(RRAX)
	a.emit(0x89, 0xC1)                         // mov ecx, eax
	a.emit(0xD1, 0xE9)                         // shr ecx, 1
	a.emit(0x81, 0xE1, 0x55, 0x55, 0x55, 0x55) // and ecx, 0x55555555
	a.emit(0x29, 0xC8)                         // sub eax, ecx
	a.emit(0x89, 0xC1)                         // mov ecx, eax
	a.emit(0xC1, 0xE9, 0x02)                   // shr ecx, 2
	a.emit(0x81, 0xE1, 0x33, 0x33, 0x33, 0x33) // and ecx, 0x33333333
	a.emit(0x81, 0xE0, 0x33, 0x33, 0x33, 0x33) // and eax, 0x33333333
	// NOTE: modrm E0 (rm=000=eax); E4 would address ESP and destroy
	// the stack. Golden test pins the full Popcnt32 sequence.
	a.emit(0x01, 0xC8)                         // add eax, ecx
	a.emit(0x89, 0xC1)                         // mov ecx, eax
	a.emit(0xC1, 0xE9, 0x04)                   // shr ecx, 4
	a.emit(0x01, 0xC8)                         // add eax, ecx
	a.emit(0x81, 0xE0, 0x0F, 0x0F, 0x0F, 0x0F) // and eax, 0x0F0F0F0F
	a.emit(0x69, 0xC0, 0x01, 0x01, 0x01, 0x01) // imul eax, 0x01010101
	a.emit(0xC1, 0xE8, 0x18)                   // shr eax, 24
	a.pushReg(RRAX)
}

// DivRem32: signed=div_s/rem_s (idiv), unsigned=div_u/rem_u.
// Wasm traps on divisor 0; INT_MIN/-1 wraps (div result INT_MIN,
// rem result 0) instead of faulting, so idiv's #DE cases are
// pre-checked and the normal path always completes.
func (a *Asm) DivRem32(signed, isRem bool) {
	a.pop2() // eax=lhs, ecx=rhs
	trap := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x85, 0xC9) // test ecx, ecx
	a.Jcc(CC_EQ, trap)
	a.emit1(0x52) // push rdx (save trap pin across idiv/div)
	if signed {
		// INT_MIN / -1 special case: skip idiv.
		a.emit(0x3D, 0x00, 0x00, 0x00, 0x80) // cmp eax, 0x80000000
		normal := a.NewLabel()
		a.Jcc(CC_NE, normal)
		a.emit(0x83, 0xF9, 0xFF) // cmp ecx, -1
		a.Jcc(CC_NE, normal)
		if isRem {
			a.emit(0x31, 0xC0) // xor eax, eax (rem result 0)
		} // else eax already holds INT_MIN (div wraps)
		a.Jmp(done)
		a.Bind(normal)
		a.emit(0x99)       // cdq
		a.emit(0xF7, 0xF9) // idiv ecx
		if isRem {
			a.emit(0x89, 0xD0) // mov eax, edx
		}
	} else {
		a.emit(0x31, 0xD2) // xor edx, edx
		a.emit(0xF7, 0xF1) // div ecx
		if isRem {
			a.emit(0x89, 0xD0) // mov eax, edx
		}
	}
	a.emit1(0x5A) // pop rdx (restore trap pin)
	a.Jmp(done)
	a.Bind(trap)
	a.Trap(TrapDivZero)
	a.Bind(done)
	a.pushReg(RRAX)
}
