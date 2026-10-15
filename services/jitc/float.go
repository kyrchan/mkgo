package jitc

// float.go — f32/f64 baseline (Stage 2b). SSE2 everywhere except
// ceil/floor/trunc/nearest, which use roundss/roundsd (SSE4.1 — same
// baseline discipline as the kernel's existing -mavx2 vector code;
// QEMU runs -cpu max and every dev host is post-2008).
//
// Slot discipline: f32 rides as u32 bits (zero-extended), f64 as u64
// bits. Moves (const/load/store/global/local/set/tee, reinterpret)
// are type-agnostic bit transport and need no FPU.
//
// Correctness-critical deviations from naive SSE lowering, all handled:
//   - relops: unordered (NaN) folds into the comparison — every relop
//     pairs its setcc with setnp (eq also needs it: ucomiss sets ZF on
//     unordered, but wasm eq(NaN) = false).
//   - min/max: unordered → canonical NaN (x86 would return an operand);
//     all-zero inputs need sign fixup (wasm min(-0,+0) = -0 via OR,
//     max via AND; x86 returns the second operand either way).
//   - trunc_f (trapping): NaN and out-of-range trap EXACTLY (boundaries
//     checked before cvtt, whose saturation value is indistinguishable
//     from a valid INT_MIN). u64 targets use the x87-fild+bias path,
//     which is exact for the full range (see U64FromFloat).
//   - trunc_sat_f (FC 00-07): no traps; NaN → 0, clamping, then convert.

const (
	f32PosZeroBits = 0x00000000
	f32SignBit     = 0x80000000
	f32AbsMask     = 0x7FFFFFFF
	f32CanonNaN    = 0x7FC00000
	f64AbsMask     = 0x7FFFFFFFFFFFFFFF
	f64SignBit     = 0x8000000000000000
	f64CanonNaN    = 0x7FF8000000000000
)

// popF32pair pops (b, a) into (xmm1, xmm0) as f32 lanes.
func (a *Asm) popF32pair() {
	a.popReg(RRCX)                 // b
	a.popReg(RRAX)                 // a
	a.emit(0x66, 0x0F, 0x6E, 0xC0) // movd xmm0, eax
	a.emit(0x66, 0x0F, 0x6E, 0xC9) // movd xmm1, ecx
}

// popF64pair pops (b, a) into (xmm1, xmm0) as f64 lanes.
func (a *Asm) popF64pair() {
	a.popReg(RRCX)
	a.popReg(RRAX)
	a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC0) // movq xmm0, rax
	a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC9) // movq xmm1, rcx
}

// pushF32 moves xmm0's low lane to the stack slot.
func (a *Asm) pushF32() {
	a.emit(0x66, 0x0F, 0x7E, 0xC0) // movd eax, xmm0
	a.pushReg(RRAX)
}

// pushF64 moves xmm0 to the stack slot.
func (a *Asm) pushF64() {
	a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0) // movq rax, xmm0
	a.pushReg(RRAX)
}

// FloatArith emits a binary f32/f64 op (add/sub/mul/div/min/max).
// subop selects the opcode byte; min/max get NaN + signed-zero fixups.
func (a *Asm) FloatArith(wide bool, subop byte) {
	if wide {
		a.popF64pair()
	} else {
		a.popF32pair()
	}
	prefix := byte(0xF3)
	if wide {
		prefix = 0xF2
	}
	if subop == 0x5D || subop == 0x5F {
		a.floatMinMax(wide, subop, subop == 0x5D)
		return
	}
	a.emit(prefix, 0x0F, subop, 0xC1) // op xmm0, xmm1
	if wide {
		a.pushF64()
	} else {
		a.pushF32()
	}
}

// floatMinMax: min/max with canonical-NaN-on-unordered and the
// all-zeros sign rule. Entry: xmm0=a, xmm1=b. Only RAX/RCX/R8/R9
// scratch are used (never the RDX trap pin or pinned regs).
// isMin selects min (zero-case OR → -0 wins) vs max (AND → +0 wins).
func (a *Asm) floatMinMax(wide bool, subop byte, isMin bool) {
	prefix := byte(0xF3)
	if wide {
		prefix = 0xF2
	}
	isZero := a.NewLabel()
	isNaN := a.NewLabel()
	done := a.NewLabel()
	if wide {
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0) // movq rax, xmm0 (a bits)
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC9) // movq rcx, xmm1 (b bits)
		a.emit(0x49, 0x89, 0xC0)             // mov r8, rax
		if isMin {
			a.emit(0x49, 0x09, 0xC8) // or r8, rcx (src ecx: R=0!)
		} else {
			a.emit(0x49, 0x21, 0xC8) // and r8, rcx
		}
		a.emit(0x4D, 0x89, 0xC1)             // mov r9, r8
		a.emit(0x49, 0xD1, 0xE1)             // shl r9, 1
		a.Jcc(CC_EQ, isZero)                 // (a|b)<<1==0 ⟺ both ±0
		a.emit(0x66, 0x0F, 0x2E, 0xC1)       // ucomisd xmm0, xmm1
		a.Jcc(0x8A, isNaN)                   // jp: unordered → canonical NaN
		a.emit(prefix, 0x0F, subop, 0xC1)    // min/maxsd xmm0, xmm1
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0) // movq rax, xmm0
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(isZero)
		a.emit(0x4C, 0x89, 0xC0) // mov rax, r8
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(isNaN)
		a.movAbs64(RRAX, f64CanonNaN)
		a.pushReg(RRAX)
		a.Bind(done)
	} else {
		a.emit(0x66, 0x0F, 0x7E, 0xC0) // movd eax, xmm0
		a.emit(0x66, 0x0F, 0x7E, 0xC9) // movd ecx, xmm1
		a.emit(0x41, 0x89, 0xC0)       // mov r8d, eax (dest rm needs B!)
		if isMin {
			a.emit(0x41, 0x09, 0xC8) // or r8d, ecx (src ecx: R=0!)
		} else {
			a.emit(0x41, 0x21, 0xC8) // and r8d, ecx
		}
		a.emit(0x45, 0x89, 0xC1) // mov r9d, r8d
		a.emit(0x45, 0xD1, 0xE1) // shl r9d, 1
		a.Jcc(CC_EQ, isZero)
		a.emit(0x0F, 0x2E, 0xC1) // ucomiss xmm0, xmm1
		a.Jcc(0x8A, isNaN)
		a.emit(prefix, 0x0F, subop, 0xC1)
		a.emit(0x66, 0x0F, 0x7E, 0xC0) // movd eax, xmm0
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(isZero)
		a.emit(0x44, 0x89, 0xC0) // mov eax, r8d
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(isNaN)
		a.emit(0xB8) // mov eax, canonical NaN
		a.emit32(f32CanonNaN)
		a.pushReg(RRAX)
		a.Bind(done)
	}
}

// FloatCmp emits an f32/f64 comparison with exact NaN semantics:
// eq needs ZF&!PF (ucomiss sets ZF on unordered too), needs fold PF in,
// ordered relations mask with !PF. swap computes b<a instead of a>b
// (used for gt/ge, avoiding a second ucomiss form).
func (a *Asm) FloatCmp(wide, swap bool, setcc byte, needNP, orNP bool) {
	if wide {
		a.popF64pair()
	} else {
		a.popF32pair()
	}
	if swap {
		// xmm0=b, xmm1=a: reload swapped (pops already consumed).
		// Achieved by re-moving: xmm0<->xmm1 via RAX/RCX.
		if wide {
			a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0) // movq rax, xmm0 (=b)
			a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC9) // movq rcx, xmm1 (=a)
			a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC1) // movq xmm0, rcx (=a)
			a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC8) // movq xmm1, rax (=b)
		} else {
			a.emit(0x66, 0x0F, 0x7E, 0xC0)
			a.emit(0x66, 0x0F, 0x7E, 0xC9)
			a.emit(0x66, 0x0F, 0x6E, 0xC1)
			a.emit(0x66, 0x0F, 0x6E, 0xC8)
		}
	}
	if wide {
		a.emit(0x66, 0x0F, 0x2E, 0xC1) // ucomisd xmm0, xmm1
	} else {
		a.emit(0x0F, 0x2E, 0xC1) // ucomiss xmm0, xmm1
	}
	a.emit(0x0F, setcc, 0xC0) // setcc al
	if needNP {
		// and-case (eq/lt/le): mask with !PF (0x9B setnp) to exclude
		// unordered. or-case (ne): fold PF in (0x9A setp) to include
		// unordered. Mixing these up inverts NaN results.
		if orNP {
			a.emit(0x41, 0x0F, 0x9A, 0xC0) // setp r8b
			a.emit(0x44, 0x08, 0xC0)       // or al, r8b
		} else {
			a.emit(0x41, 0x0F, 0x9B, 0xC0) // setnp r8b
			a.emit(0x44, 0x20, 0xC0)       // and al, r8b
		}
	}
	a.emit(0x0F, 0xB6, 0xC0) // movzx eax, al
	a.pushReg(RRAX)
}

// FloatSqrt emits sqrtss/sqrtsd (hardware: NaN/negative → NaN, exact).
func (a *Asm) FloatSqrt(wide bool) {
	if wide {
		a.popF64pair()
		a.emit(0xF2, 0x0F, 0x51, 0xC1)
		a.pushF64()
	} else {
		a.popF32pair()
		a.emit(0xF3, 0x0F, 0x51, 0xC1)
		a.pushF32()
	}
}

// FloatAbsNeg emits abs/neg via integer masks on the slot bits (exact,
// no FPU flags involved; -0.0 handled bit-correctly).
func (a *Asm) FloatAbsNeg(wide, isNeg bool) {
	a.popReg(RRAX)
	if wide {
		if isNeg {
			a.movAbs64(RR8, f64SignBit)
			a.emit(0x4C, 0x09, 0xC0) // xor rax, r8
		} else {
			a.movAbs64(RR8, f64AbsMask)
			a.emit(0x4C, 0x21, 0xC0) // and rax, r8
		}
	} else {
		if isNeg {
			a.emit(0x35, 0x00, 0x00, 0x00, 0x80) // xor eax, 0x80000000
		} else {
			a.emit(0x25, 0xFF, 0xFF, 0xFF, 0x7F) // and eax, 0x7FFFFFFF
		}
	}
	a.pushReg(RRAX)
}

// FloatRound emits ceil/floor/trunc/nearest via roundss/roundsd
// (SSE4.1; mode = 1/2/3/0). Exact per IEEE, including -0.0.
func (a *Asm) FloatRound(wide bool, mode byte) {
	if wide {
		a.popReg(RRAX)
		a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC0)       // movq xmm0, rax
		a.emit(0x66, 0x0F, 0x3A, 0x0B, 0xC0, mode) // roundsd (3A! 3B is packed)
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0)       // movq rax, xmm0
		a.pushReg(RRAX)
	} else {
		a.popReg(RRAX)
		a.emit(0x66, 0x0F, 0x6E, 0xC0)             // movd xmm0, eax
		a.emit(0x66, 0x0F, 0x3A, 0x0A, 0xC0, mode) // roundss
		a.emit(0x66, 0x0F, 0x7E, 0xC0)             // movd eax, xmm0
		a.pushReg(RRAX)
	}
}

// FloatConvertInt emits int→float (all four width/sign combos). The
// slot may hold a wider bit pattern than the source type (transports
// preserve bits; real Go modules launder f64 bits through i32 slots),
// so i32 sources are narrowed FIRST: signed via movsxd, unsigned via
// mov eax,eax (zero-extend). u32 slots are then exact; converting the
// raw full slot would misread f64-bit patterns as huge i64 values.
func (a *Asm) FloatConvertInt(wide, srcWide, srcUnsigned bool) {
	a.popReg(RRAX)
	if !srcWide && !srcUnsigned {
		a.emit(0x48, 0x63, 0xC0) // movsxd rax, eax (sign-extend i32)
	}
	if !srcWide && srcUnsigned {
		a.emit(0x89, 0xC0) // mov eax, eax (zero-extend u32)
	}
	if !srcWide || !srcUnsigned {
		// i32/i64/u32: value now correctly sign/zero-extended in rax.
		if wide {
			a.emit(0xF2, 0x48, 0x0F, 0x2A, 0xC0) // cvtsi2sd xmm0, rax
			a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0)
		} else {
			a.emit(0xF3, 0x48, 0x0F, 0x2A, 0xC0) // cvtsi2ss xmm0, rax
			a.emit(0x66, 0x0F, 0x7E, 0xC0)
		}
		a.pushReg(RRAX)
		return
	}
	// u64: branch on the top bit.
	small := a.NewLabel()
	done := a.NewLabel()
	a.emit(0x48, 0x85, 0xC0) // test rax, rax
	a.Jcc(CC_GES, small)     // jns: top bit clear → direct convert
	// big path (x ≥ 2^63): x87 fild reads the bits as i64 (exact —
	// f80's 64-bit mantissa holds every i64); the +2^64 bias is added
	// in x87 too (still exact: results fit 64 bits), so the single
	// fstp is the ONLY rounding — correctly-rounded output.
	a.emit(0x48, 0x83, 0xEC, 0x10) // sub rsp, 16 (scratch)
	a.emit(0x48, 0x89, 0x04, 0x24) // mov [rsp], rax
	a.emit(0xDF, 0x2C, 0x24)       // fild qword [rsp]
	a.movAbs64(RR8, 0x43F0000000000000)
	a.emit(0x4C, 0x89, 0x44, 0x24, 0x08) // mov [rsp+8], r8
	a.emit(0xDD, 0x44, 0x24, 0x08)       // fld qword [rsp+8]
	a.emit(0xDE, 0xC1)                   // faddp st1, st0 (DE C1! C9=fmulp)
	if wide {
		a.emit(0xDD, 0x5C, 0x24, 0x08)       // fstp qword [rsp+8]
		a.emit(0x48, 0x8B, 0x44, 0x24, 0x08) // mov rax, [rsp+8]
	} else {
		a.emit(0xD9, 0x5C, 0x24, 0x08) // fstp dword [rsp+8]
		a.emit(0x8B, 0x44, 0x24, 0x08) // mov eax, [rsp+8]
	}
	a.emit(0x48, 0x83, 0xC4, 0x10) // add rsp, 16
	a.pushReg(RRAX)
	a.Jmp(done)
	a.Bind(small)
	if wide {
		a.emit(0xF2, 0x48, 0x0F, 0x2A, 0xC0) // cvtsi2sd xmm0, rax
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0) // movq rax, xmm0
	} else {
		a.emit(0xF3, 0x48, 0x0F, 0x2A, 0xC0) // cvtsi2ss xmm0, rax
		a.emit(0x66, 0x0F, 0x7E, 0xC0)       // movd eax, xmm0
	}
	a.pushReg(RRAX)
	a.Bind(done)
}

// floatBound loads an f32/f64 boundary constant into xmm1.
func (a *Asm) floatBound(wide bool, bits uint64) {
	if wide {
		a.movAbs64(RRAX, bits)
		a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC8) // movq xmm1, rax (rm=000!)
	} else {
		a.emit(0xB8) // mov eax, imm32
		a.emit32(uint32(bits))
		a.emit(0x66, 0x0F, 0x6E, 0xC8) // movd xmm1, eax
	}
}

// FloatTrunc emits a trapping float→int conversion (0xA8-0xAF).
// toWide selects i64 destination; toUnsigned selects unsigned range;
// fromWide selects f64 source. NaN and out-of-range trap EXACTLY:
// bounds are checked BEFORE cvtt (whose saturation value would
// otherwise alias a valid INT_MIN).
func (a *Asm) FloatTrunc(toWide, toUnsigned, fromWide bool) {
	trap := a.NewLabel()
	a.popReg(RRAX)
	if fromWide {
		a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC0) // movq xmm0, rax
	} else {
		a.emit(0x66, 0x0F, 0x6E, 0xC0) // movd xmm0, eax
	}
	// NaN check: unordered-with-self.
	if fromWide {
		a.emit(0x66, 0x0F, 0x2E, 0xC0) // ucomisd xmm0, xmm0
	} else {
		a.emit(0x0F, 0x2E, 0xC0) // ucomiss xmm0, xmm0
	}
	a.Jcc(0x8A, trap) // jp: NaN traps
	var loBits, hiBits uint64
	if !fromWide {
		if !toWide && !toUnsigned {
			loBits, hiBits = 0xCF000000, 0x4F000000 // ∓2^31 as f32
		} else if !toWide {
			loBits, hiBits = 0x00000000, 0x4F800000 // 0, 2^32
		} else if !toUnsigned {
			loBits, hiBits = 0xDF000000, 0x5F000000 // ∓2^63 as f32
		} else {
			loBits, hiBits = 0x00000000, 0x5F800000 // 0, 2^64
		}
	} else {
		if !toWide && !toUnsigned {
			loBits, hiBits = 0xC1E0000000000000, 0x41E0000000000000
		} else if !toWide {
			loBits, hiBits = 0x0000000000000000, 0x41F0000000000000
		} else if !toUnsigned {
			loBits, hiBits = 0xC3E0000000000000, 0x43E0000000000000
		} else {
			loBits, hiBits = 0x0000000000000000, 0x43F0000000000000
		}
	}
	a.floatBound(fromWide, loBits)
	if fromWide {
		a.emit(0x66, 0x0F, 0x2E, 0xC1)
	} else {
		a.emit(0x0F, 0x2E, 0xC1)
	}
	a.Jcc(CC_LTU, trap) // x < lo traps (-0.0 vs +0.0: equal, no trap)
	a.floatBound(fromWide, hiBits)
	if fromWide {
		a.emit(0x66, 0x0F, 0x2E, 0xC1)
	} else {
		a.emit(0x0F, 0x2E, 0xC1)
	}
	a.Jcc(CC_GEU, trap) // x >= hi traps
	// In range: convert. 64-bit destinations covering ≥2^63 need the
	// bias split (cvtt saturates above INT64_MAX).
	needSplit := toWide && toUnsigned
	if !needSplit {
		if fromWide {
			if toWide {
				a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0) // cvttsd2si rax
			} else {
				a.emit(0xF2, 0x0F, 0x2C, 0xC0) // cvttsd2si eax
			}
		} else {
			if toWide {
				a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0) // cvttss2si rax
			} else {
				a.emit(0xF3, 0x0F, 0x2C, 0xC0) // cvttss2si eax
			}
		}
		a.pushReg(RRAX)
	} else {
		// x in [0, 2^64): if x < 2^63 convert directly, else
		// subtract 2^63, convert, re-add (exact: all values integral
		// after truncation... careful: convert first, the cvtt
		// truncates; bias arithmetic on the truncated integer).
		// Form: t = trunc(x) via: if x>=2^63: t = trunc(x-2^63)+2^63.
		ge := a.NewLabel()
		done := a.NewLabel()
		a.floatBound(fromWide, two63bits(fromWide))
		if fromWide {
			a.emit(0x66, 0x0F, 0x2E, 0xC1)
		} else {
			a.emit(0x0F, 0x2E, 0xC1)
		}
		a.Jcc(CC_GEU, ge)
		if fromWide {
			a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0)
		} else {
			a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0)
		}
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(ge)
		// xmm0 -= 2^63 (xmm1 still holds the bound): subss/sd.
		if fromWide {
			a.emit(0xF2, 0x0F, 0x5C, 0xC1) // subsd xmm0, xmm1
			a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0)
		} else {
			a.emit(0xF3, 0x0F, 0x5C, 0xC1) // subss xmm0, xmm1
			a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0)
		}
		a.movAbs64(RR8, 0x8000000000000000)
		a.emit(0x4C, 0x01, 0xC0) // add rax, r8
		a.pushReg(RRAX)
		a.Bind(done)
	}
	done2 := a.NewLabel()
	a.Jmp(done2)
	a.Bind(trap)
	a.Trap(TrapConv)
	a.Bind(done2)
}

func two63bits(wide bool) uint64 {
	if wide {
		return 0x43E0000000000000
	}
	return 0x5F000000
}

// FloatSat emits a saturating float→int conversion (FC sub 0-7).
// No traps: NaN → 0, below-range → MIN (or 0 unsigned), above-range →
// MAX. Conversion reuses the truncating convert tails (values are
// pre-clamped into range; u64 still needs the bias split).
func (a *Asm) FloatSat(toWide, toUnsigned, fromWide bool) {
	pushZero := a.NewLabel()
	pushLo := a.NewLabel()
	pushHi := a.NewLabel()
	convert := a.NewLabel()
	done := a.NewLabel()
	a.popReg(RRAX)
	if fromWide {
		a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC0) // movq xmm0, rax
		a.emit(0x66, 0x0F, 0x2E, 0xC0)       // ucomisd self (NaN?)
	} else {
		a.emit(0x66, 0x0F, 0x6E, 0xC0) // movd xmm0, eax
		a.emit(0x0F, 0x2E, 0xC0)       // ucomiss self
	}
	a.Jcc(0x8A, pushZero) // jp: NaN → 0
	var loBits, hiBits uint64
	var loVal, hiVal int64
	if !fromWide {
		if !toWide && !toUnsigned {
			loBits, hiBits, loVal, hiVal = 0xCF000000, 0x4F000000, -2147483648, 2147483647
		} else if !toWide {
			loBits, hiBits, loVal, hiVal = 0x00000000, 0x4F800000, 0, 4294967295
		} else if !toUnsigned {
			loBits, hiBits, loVal, hiVal = 0xDF000000, 0x5F000000, -9223372036854775808, 9223372036854775807
		} else {
			loBits, hiBits, loVal, hiVal = 0x00000000, 0x5F800000, 0, -1 // -1 = all-ones u64
		}
	} else {
		if !toWide && !toUnsigned {
			loBits, hiBits, loVal, hiVal = 0xC1E0000000000000, 0x41E0000000000000, -2147483648, 2147483647
		} else if !toWide {
			loBits, hiBits, loVal, hiVal = 0x0000000000000000, 0x41F0000000000000, 0, 4294967295
		} else if !toUnsigned {
			loBits, hiBits, loVal, hiVal = 0xC3E0000000000000, 0x43E0000000000000, -9223372036854775808, 9223372036854775807
		} else {
			loBits, hiBits, loVal, hiVal = 0x0000000000000000, 0x43F0000000000000, 0, -1
		}
	}
	a.floatBound(fromWide, loBits)
	if fromWide {
		a.emit(0x66, 0x0F, 0x2E, 0xC1)
	} else {
		a.emit(0x0F, 0x2E, 0xC1)
	}
	a.Jcc(CC_LTU, pushLo)
	a.floatBound(fromWide, hiBits)
	if fromWide {
		a.emit(0x66, 0x0F, 0x2E, 0xC1)
	} else {
		a.emit(0x0F, 0x2E, 0xC1)
	}
	a.Jcc(CC_GEU, pushHi)
	a.Jmp(convert)
	a.Bind(pushZero)
	if toWide {
		a.Const64(0)
	} else {
		a.Const32(0)
	}
	a.Jmp(done)
	a.Bind(pushLo)
	if toWide {
		a.Const64(loVal)
	} else {
		a.Const32(int32(loVal))
	}
	a.Jmp(done)
	a.Bind(pushHi)
	if toWide {
		a.Const64(hiVal)
	} else {
		a.Const32(int32(hiVal))
	}
	a.Jmp(done)
	a.Bind(convert)
	// In range: convert (u64 upper half still needs the bias split).
	if toWide && toUnsigned {
		ge := a.NewLabel()
		a.floatBound(fromWide, two63bits(fromWide))
		if fromWide {
			a.emit(0x66, 0x0F, 0x2E, 0xC1)
		} else {
			a.emit(0x0F, 0x2E, 0xC1)
		}
		a.Jcc(CC_GEU, ge)
		if fromWide {
			a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0)
		} else {
			a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0)
		}
		a.pushReg(RRAX)
		a.Jmp(done)
		a.Bind(ge)
		if fromWide {
			a.emit(0xF2, 0x0F, 0x5C, 0xC1)
			a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0)
		} else {
			a.emit(0xF3, 0x0F, 0x5C, 0xC1)
			a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0)
		}
		a.movAbs64(RR8, 0x8000000000000000)
		a.emit(0x4C, 0x01, 0xC0) // add rax, r8
		a.pushReg(RRAX)
	} else {
		if fromWide {
			if toWide {
				a.emit(0xF2, 0x48, 0x0F, 0x2C, 0xC0)
			} else {
				a.emit(0xF2, 0x0F, 0x2C, 0xC0)
			}
		} else {
			if toWide {
				a.emit(0xF3, 0x48, 0x0F, 0x2C, 0xC0)
			} else {
				a.emit(0xF3, 0x0F, 0x2C, 0xC0)
			}
		}
		a.pushReg(RRAX)
	}
	a.Bind(done)
}

// CopySign implements f32/f64.copysign via integer masks on the slot
// bits (exact: sign from b, magnitude from a, NaN payloads preserved).
func (a *Asm) CopySign(wide bool) {
	a.popReg(RRCX) // b
	a.popReg(RRAX) // a
	if wide {
		a.movAbs64(RR8, f64AbsMask)
		a.emit(0x4C, 0x21, 0xC0) // and rax, r8
		a.movAbs64(RR8, f64SignBit)
		a.movAbs64(RR8, f64SignBit)
		a.emit(0x49, 0x21, 0xC8) // and r8, rcx (src ecx: R=0!)
		a.emit(0x4C, 0x09, 0xC0) // or rax, r8
	} else {
		a.emit(0x25, 0xFF, 0xFF, 0xFF, 0x7F)       // and eax, 0x7FFFFFFF
		a.emit(0x81, 0xE1, 0x00, 0x00, 0x00, 0x80) // and ecx, 0x80000000
		a.emit(0x09, 0xC8)                         // or eax, ecx
	}
	a.pushReg(RRAX)
}
