package jitc

import (
	"errors"
	"fmt"
)

// compile.go — Stage 2a compiler: Module IR → per-function machine code
// with cross-function linking.
//
// Soundness contract: the decoder is structural, NOT validating, so the
// compiler simulates the operand-stack depth and block nesting as it
// emits. Any depth underflow, depth mismatch at a join point, bad local
// / global / function index, or float-typed operation fails compilation
// with an error. Miscompilation is impossible by construction: where the
// compiler cannot prove the stack shape (block params), it errors
// instead of guessing (see brDrop).

var (
	ErrCompile  = errors.New("jitc: compile error")
	ErrNoMemory = errors.New("jitc: memory op but no memory section")
	ErrFloat    = errors.New("jitc: float op needs Stage 2b outlining")
	ErrImport   = errors.New("jitc: unsupported import call (only fd_write)")
)

// Image is a linked module: one executable region layout with every
// function's base offset and all cross-function calls patched.
// Polls holds region-absolute offsets of safe-point rel32 words
// (patched by the host harness to a NOP stub, by the kernel at bind
// to the preemption trampoline).
type Image struct {
	// Funcs maps module-wide function index (imports first) to its
	// offset in Region. Imported functions have no entry.
	Funcs  map[uint32]int
	Region []byte // concatenated function blobs, call-patched
	Polls  []int  // region-absolute safe-point rel32 offsets
}

type blockFrame struct {
	entryDepth int
	arity      int // 0 or 1 results (multi-value is MVP-illegal anyway)
	brk        *Label
	loopTop    *Label
	isLoop     bool
	isIf       bool
	inElse     bool
}

type funcCtx struct {
	a         *Asm
	mod       *Module
	nparams   int
	nresults  int
	nlocals   int
	depth     int
	dead      bool // unreachable code: skip emission until else/end
	endedDead bool // function terminator reached while dead: a trap/
	// return/br already diverts control, so no fallthrough depth
	// check applies (valid modules may end dead, e.g. `unreachable`).
	blocks []blockFrame
	fidx   uint32 // module-wide index of the function under compile
	// irelocs collects import call sites (nil = reject imports, the
	// legacy pure-only contract used by CompileModule/CompileOne).
	irelocs *[]ImportReloc
}

// CompileModule compiles every defined function and links cross-calls.
// Functions using float ops, import calls, indirect calls or memories
// beyond one section fail individually (the error names the function);
// the caller decides whether that is fatal (tests) or fallback-to-
// interp (production cache policy).
func CompileModule(m *Module) (*Image, error) {
	nimport := m.ImportFuncCount()
	bufs := make([]*Asm, len(m.Code))
	for i := range m.Code {
		a := &Asm{}
		if err := compileFunc(m, a, uint32(nimport+i), i, nil); err != nil {
			return nil, fmt.Errorf("%w: func %d: %v", ErrCompile, nimport+i, err)
		}
		// No unbound intra-function labels may remain.
		if len(a.fix) != 0 {
			return nil, fmt.Errorf("%w: func %d: %d unbound labels",
				ErrCompile, nimport+i, len(a.fix))
		}
		bufs[i] = a
	}
	// Layout: function i at sequential offsets; patch CallSites.
	img := &Image{Funcs: map[uint32]int{}}
	off := 0
	for i, b := range bufs {
		img.Funcs[uint32(nimport+i)] = off
		for _, p := range b.Polls {
			img.Polls = append(img.Polls, off+p)
		}
		off += len(b.Code)
	}
	img.Region = make([]byte, 0, off)
	for _, b := range bufs {
		img.Region = append(img.Region, b.Code...)
	}
	off = 0
	for i, b := range bufs {
		base := img.Funcs[uint32(nimport+i)]
		for _, cs := range b.Calls {
			tgt, ok := img.Funcs[cs.Target]
			if !ok {
				return nil, fmt.Errorf("%w: call to import %d",
					ErrImport, cs.Target)
			}
			rel := int32(tgt - (base + cs.Pos + 4))
			p := base + cs.Pos
			img.Region[p] = byte(rel)
			img.Region[p+1] = byte(rel >> 8)
			img.Region[p+2] = byte(rel >> 16)
			img.Region[p+3] = byte(rel >> 24)
		}
		off = base + len(b.Code)
		_ = off
	}
	return img, nil
}

// CompileOne compiles a single defined function (module-wide index,
// imports first) without linking. Cross-function calls record CallSite
// entries for the linker; leaf functions link trivially. Used by the
// coverage analyzer and differential tests.
func CompileOne(m *Module, fidx uint32) (*Asm, error) {
	nimport := m.ImportFuncCount()
	bi := int(fidx) - nimport
	if bi < 0 || bi >= len(m.Code) {
		return nil, fmt.Errorf("%w: func %d not defined", ErrCompile, fidx)
	}
	a := &Asm{}
	if err := compileFunc(m, a, fidx, bi, nil); err != nil {
		return nil, fmt.Errorf("%w: func %d: %v", ErrCompile, fidx, err)
	}
	if len(a.fix) != 0 {
		return nil, fmt.Errorf("%w: func %d: %d unbound labels",
			ErrCompile, fidx, len(a.fix))
	}
	return a, nil
}

// funcTypeOf resolves the module-wide function index to its type.
// Imported-function indices resolve through the import entries.
func funcTypeOf(m *Module, idx uint32) (FuncType, error) {
	nf := 0
	for _, im := range m.Imports {
		if im.Kind != 0 {
			continue
		}
		if uint32(nf) == idx {
			if uint64(im.Type) >= uint64(len(m.Types)) {
				return FuncType{}, fmt.Errorf("bad import type %d", im.Type)
			}
			return m.Types[im.Type], nil
		}
		nf++
	}
	bi := int(idx) - nf
	if bi < 0 || bi >= len(m.Funcs) {
		return FuncType{}, fmt.Errorf("func index %d out of range", idx)
	}
	ti := m.Funcs[bi]
	if uint64(ti) >= uint64(len(m.Types)) {
		return FuncType{}, fmt.Errorf("bad type index %d", ti)
	}
	return m.Types[ti], nil
}

func compileFunc(m *Module, a *Asm, funcidx uint32, bodyidx int, irelocs *[]ImportReloc) error {
	ft, err := funcTypeOf(m, funcidx)
	if err != nil {
		return err
	}
	if len(ft.Results) > 1 {
		return fmt.Errorf("multi-value results unsupported")
	}
	fb := m.Code[bodyidx]
	nparams := len(ft.Params)
	nresults := len(ft.Results)
	nloc := nparams + len(fb.Locals)
	c := &funcCtx{a: a, mod: m, nparams: nparams, nresults: nresults,
		nlocals: nloc, fidx: funcidx, irelocs: irelocs}
	a.Prologue(nloc)
	if nparams > 0 {
		a.CopyParams(ft.Params)
	}
	// Zero non-parameter locals AFTER CopyParams: the caller's args bounce
	// area (R10 + offsets) may overlap a non-parameter slot, and zeroing
	// before CopyParams reads the args would clobber the parameter value.
	a.ZeroNonParamLocals(nloc, nparams)
	// Function-entry safe-point: observes pending preemption before the
	// first op (only pinned regs + slots live — see Safepoint contract).
	a.Safepoint()
	for _, o := range fb.Ops {
		if err := c.emit(o); err != nil {
			return fmt.Errorf("op 0x%02x: %v", o.Code, err)
		}
	}
	if len(c.blocks) != 0 {
		return fmt.Errorf("body ends inside %d blocks", len(c.blocks))
	}
	// Function fallthrough: exactly nresults values must remain.
	// (Skipped when the terminator was reached while dead: control
	// diverts through a trap/return and never falls through.)
	if !c.endedDead && c.depth != nresults {
		return fmt.Errorf("exit depth %d, want %d", c.depth, nresults)
	}
	if c.endedDead {
		// Still need a well-formed exit for the linker layout: the
		// diverting op already returned, so just close the frame in
		// case execution ever falls here (it cannot, but the bytes
		// must decode linearly).
		a.Epilogue()
		return nil
	}
	if nresults == 1 {
		a.Result1()
	}
	a.Epilogue()
	return nil
}

func (c *funcCtx) push(n int) { c.depth += n }

func (c *funcCtx) pop(n int) error {
	if c.depth < n {
		return fmt.Errorf("stack underflow (depth %d, pop %d)", c.depth, n)
	}
	c.depth -= n
	return nil
}

// need validates depth without mutating it (for helpers that manage
// the machine stack themselves).
func (c *funcCtx) need(n int) error {
	if c.depth < n {
		return fmt.Errorf("stack underflow (depth %d, need %d)", c.depth, n)
	}
	return nil
}

// brDrop computes the bytes to discard for a branch to the block stack
// entry `to` (0 = innermost). Loops re-enter at entry depth; other
// targets land after the block with `arity` results kept. A negative
// drop means the block carried parameters the decoder did not record —
// erroring here is the soundness backstop (never miscompile).
func (c *funcCtx) brDrop(to int) (int, *Label, error) {
	if to < 0 || to >= len(c.blocks) {
		return 0, nil, fmt.Errorf("bad label %d", to)
	}
	b := c.blocks[len(c.blocks)-1-to]
	var want int
	var lbl *Label
	if b.isLoop {
		want, lbl = b.entryDepth, b.loopTop
	} else {
		want, lbl = b.entryDepth+b.arity, b.brk
	}
	drop := c.depth - want
	if drop < 0 {
		return 0, nil, fmt.Errorf("params-block unsupported in v1")
	}
	return drop * 8, lbl, nil
}

func (c *funcCtx) emitBranch(to int) error {
	nbytes, lbl, err := c.brDrop(to)
	if err != nil {
		return err
	}
	c.a.addRsp(nbytes)
	c.a.Jmp(lbl)
	c.dead = true
	return nil
}

func (c *funcCtx) emit(o Op) error {
	a := c.a
	if c.dead {
		// Unreachable code: only block-structure ops matter, to find
		// the resumption point (else/end at our level). Depth is
		// frozen while dead, so pushed frames record the true entry
		// depth and resumption restores a known-good value.
		switch o.Code {
		case 0x00: // unreachable
			// NOT skipped: a br/br_table can target a block's
			// break label, which sits immediately before a
			// trailing unreachable. Control landing here MUST
			// trap (matching the interpreter's live trace); emitting
			// nothing would let native fall through and return
			// cleanly — a miscompilation (see func 78/859/...).
			a.Trap(TrapUnreachable)
		case 0x02, 0x03: // block, loop
			c.blocks = append(c.blocks, blockFrame{entryDepth: c.depth})
		case 0x04: // if
			c.blocks = append(c.blocks, blockFrame{entryDepth: c.depth, isIf: true})
		case 0x05: // else
			if len(c.blocks) == 0 {
				return fmt.Errorf("else outside block")
			}
			b := &c.blocks[len(c.blocks)-1]
			if !b.isIf || b.inElse {
				return fmt.Errorf("else outside if")
			}
			b.inElse = true
			c.dead = false
			c.depth = b.entryDepth
		case 0x0B: // end
			if len(c.blocks) == 0 {
				// Function terminator reached while dead: control
				// never falls through (a trap/return/br precedes),
				// so accept it and skip the depth check below.
				c.endedDead = true
				return nil
			}
			b := c.blocks[len(c.blocks)-1]
			c.blocks = c.blocks[:len(c.blocks)-1]
			// Bind break targets even for dead blocks: jumps emitted
			// while live (before the block went dead) still reference
			// them, and no code was emitted since, so the current
			// position IS the correct join point.
			if b.brk != nil {
				if err := a.Bind(b.brk); err != nil {
					return err
				}
			}
			if b.isIf && !b.inElse && b.loopTop != nil {
				if err := a.Bind(b.loopTop); err != nil {
					return err
				}
			}
			c.dead = false
			c.depth = b.entryDepth + b.arity
		}
		return nil
	}
	switch o.Code {
	case 0x00: // unreachable
		a.Trap(TrapUnreachable)
		c.dead = true
	case 0x01: // nop
	case 0x02, 0x03: // block, loop
		ar, err := blockArity(o.Block)
		if err != nil {
			return err
		}
		brk := a.NewLabel()
		f := blockFrame{entryDepth: c.depth, arity: ar, brk: brk,
			isLoop: o.Code == 0x03}
		if f.isLoop {
			f.loopTop = a.NewLabel()
			if err := a.Bind(f.loopTop); err != nil {
				return err
			}
			// Loop-top safe-point: every back-edge passes here, so a
			// compute loop without calls or imports still preempts
			// within one iteration (covers node-style spins).
			a.Safepoint()
		}
		c.blocks = append(c.blocks, f)
	case 0x04: // if: pop cond, optional else/end join
		ar, err := blockArity(o.Block)
		if err != nil {
			return err
		}
		if err := c.pop(1); err != nil {
			return err
		}
		// pop cond into rax, test FULL rax, jz else-branch.
		// Full-width is exact for i64/f64 conds and equivalent for
		// i32 (slots are zero-extended at every boundary, so low32
		// == full for i32). Testing eax would misread nonzero i64
		// values with zero low32 (e.g. 1<<32).
		a.popReg(RRAX)
		els := a.NewLabel()
		end := a.NewLabel()
		a.emit(0x48, 0x85, 0xC0) // test rax, rax
		a.Jcc(CC_EQ, els)
		c.blocks = append(c.blocks, blockFrame{entryDepth: c.depth,
			arity: ar, brk: end, isIf: true})
		// stash the else label on the side channel: reuse loopTop
		c.blocks[len(c.blocks)-1].loopTop = els
	case 0x05: // else
		if len(c.blocks) == 0 {
			return fmt.Errorf("else outside block")
		}
		b := &c.blocks[len(c.blocks)-1]
		if !b.isIf || b.inElse {
			return fmt.Errorf("else outside if")
		}
		if c.depth != b.entryDepth+b.arity {
			return fmt.Errorf("then-arm depth %d, want %d",
				c.depth, b.entryDepth+b.arity)
		}
		// then-arm falls through to end; else-arm starts fresh
		a.Jmp(b.brk)
		if err := a.Bind(b.loopTop); err != nil {
			return err
		}
		b.inElse = true
		c.depth = b.entryDepth
	case 0x0B: // end
		if len(c.blocks) == 0 {
			// function terminator: validated by the caller
			return nil
		}
		b := c.blocks[len(c.blocks)-1]
		c.blocks = c.blocks[:len(c.blocks)-1]
		if b.isIf && !b.inElse {
			// no else arm: bind the else label here
			if err := a.Bind(b.loopTop); err != nil {
				return err
			}
		}
		if c.depth != b.entryDepth+b.arity && !b.isLoop {
			return fmt.Errorf("block-end depth %d, want %d",
				c.depth, b.entryDepth+b.arity)
		}
		if b.isLoop {
			// falling off a loop end: stack must be entry+arity;
			// loops re-enter at entryDepth, so a results-loop with
			// values produced inside still joins correctly here.
			if c.depth != b.entryDepth+b.arity {
				return fmt.Errorf("loop-end depth %d, want %d",
					c.depth, b.entryDepth+b.arity)
			}
		}
		if err := a.Bind(b.brk); err != nil {
			return err
		}
		c.depth = b.entryDepth + b.arity
	case 0x0C: // br
		return c.emitBranch(int(o.Label))
	case 0x0D: // br_if: pop cond; branch or fall through
		if err := c.pop(1); err != nil {
			return err
		}
		a.popReg(RRAX)
		a.emit(0x48, 0x85, 0xC0) // test rax, rax (full width; see if)
		// cond == 0 falls through (depth already popped); cond != 0
		// takes the branch. The cond value sits in EAX, not on the
		// stack, so the taken drop computed on the popped depth is
		// exact and the fallthrough keeps it. Both paths live.
		nbytes, lbl, err := c.brDrop(int(o.Label))
		if err != nil {
			return err
		}
		fall := a.NewLabel()
		a.Jcc(CC_EQ, fall)
		a.addRsp(nbytes)
		a.Jmp(lbl)
		if err := a.Bind(fall); err != nil {
			return err
		}
	case 0x0E: // br_table: pop scrutinee, cascade compares
		if err := c.pop(1); err != nil {
			return err
		}
		a.popReg(RRAX)
		for i, t := range o.Table[:len(o.Table)-1] {
			nxt := a.NewLabel()
			// cmp eax, i; jne next; <branch to t>; next:
			if i < 128 {
				a.emit(0x83, 0xF8, byte(i)) // cmp eax, i
			} else {
				a.emit(0x3D) // cmp eax, imm32
				a.emit32(uint32(i))
			}
			a.Jcc(CC_NE, nxt)
			nbytes, lbl, err := c.brDrop(int(t))
			if err != nil {
				return err
			}
			a.addRsp(nbytes)
			a.Jmp(lbl)
			if err := a.Bind(nxt); err != nil {
				return err
			}
		}
		// default = last entry
		nbytes, lbl, err := c.brDrop(int(o.Table[len(o.Table)-1]))
		if err != nil {
			return err
		}
		a.addRsp(nbytes)
		a.Jmp(lbl)
		c.dead = true
	case 0x0F: // return: top nresults slots ARE the results
		// (valid code guarantees it); block temporaries below them
		// die with the frame reset in Epilogue. Depth model freezes
		// here — no fallthrough exists, so no resume accounting.
		if c.depth < c.nresults {
			return fmt.Errorf("return underflow")
		}
		if c.nresults == 1 {
			a.emit(0x58) // pop rax (result)
		}
		a.Epilogue()
		c.dead = true
	case 0x10: // call
		return c.emitCall(o.Func)
	case 0x11: // call_indirect: Stage 3 (needs tables)
		return fmt.Errorf("%w: call_indirect", ErrCompile)
	case 0x1A: // drop
		if err := c.pop(1); err != nil {
			return err
		}
		a.addRsp(8)
	case 0x1B: // select
		if err := c.pop(3); err != nil {
			return err
		}
		a.Select()
		c.push(1)
	case 0x20: // local.get
		slot, err := c.localSlot(o.Local)
		if err != nil {
			return err
		}
		a.LocalGet(slot)
		c.push(1)
	case 0x21: // local.set
		slot, err := c.localSlot(o.Local)
		if err != nil {
			return err
		}
		if err := c.pop(1); err != nil {
			return err
		}
		a.LocalSet(slot, false)
	case 0x22: // local.tee
		slot, err := c.localSlot(o.Local)
		if err != nil {
			return err
		}
		if err := c.pop(1); err != nil {
			return err
		}
		a.LocalSet(slot, true)
		c.push(1)
	case 0x23, 0x24: // global.get/set: pure slot moves, so any
		// value type rides (f32 globals move low-32 zero-extended,
		// exactly like i32).
		if int(o.Local) >= len(c.mod.Globals) {
			return fmt.Errorf("global %d out of range", o.Local)
		}
		g := c.mod.Globals[o.Local]
		wide := g.Type == VTi64 || g.Type == VTf64
		if o.Code == 0x23 {
			a.GlobalGet(o.Local, wide)
			c.push(1)
		} else {
			if g.Mut == 0 {
				return fmt.Errorf("set immutable global %d", o.Local)
			}
			if err := c.pop(1); err != nil {
				return err
			}
			a.GlobalSet(o.Local, wide)
		}
	case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
		0x30, 0x31, 0x32, 0x33, 0x34, 0x35: // loads (float loads
		// are bit-identical moves: f32.load = u32, f64.load = u64)
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		if err := c.pop(1); err != nil {
			return err
		}
		sz, sext, wide := loadAttrs(o.Code)
		a.memLoad(o.Mem.Offset, sz, sext, wide)
		c.push(1)
	case 0x36, 0x37, 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E: // stores
		// (f32.store/f64.store are bit-identical moves)
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		if err := c.pop(2); err != nil {
			return err
		}
		a.memStore(o.Mem.Offset, storeSize(o.Code))
	case 0x3F: // memory.size
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		a.MemorySize()
		c.push(1)
	case 0x40: // memory.grow (fails gracefully → -1)
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		if err := c.pop(1); err != nil {
			return err
		}
		a.MemoryGrow()
		c.push(1)
	case 0x41: // i32.const
		a.Const32(o.I32)
		c.push(1)
	case 0x42: // i64.const
		a.Const64(o.I64)
		c.push(1)
	case 0x43: // f32.const: exact bit pattern
		a.Const32(int32(o.F32))
		c.push(1)
	case 0x44: // f64.const: exact bit pattern (Const64 preserves bits)
		a.Const64(int64(o.F64))
		c.push(1)
	default:
		if o.Code >= 0x45 && o.Code <= 0x5A {
			return c.emitIntRel(o.Code)
		}
		if o.Code >= 0x5B && o.Code <= 0x66 {
			return c.emitFloatRel(o.Code)
		}
		if (o.Code >= 0x67 && o.Code <= 0x8A) || o.Code == 0xA7 {
			return c.emitIntALU(o.Code)
		}
		if o.Code >= 0x8B && o.Code <= 0xA6 {
			return c.emitFloatALU(o.Code)
		}
		if o.Code >= 0xA8 && o.Code <= 0xBD {
			return c.emitNumeric(o.Code)
		}
		if o.Code >= 0xC0 && o.Code <= 0xC4 {
			return c.emitExtend(o.Code)
		}
		if o.Code == 0xFC {
			return c.emitFC(o)
		}
		return fmt.Errorf("unknown op 0x%02x", o.Code)
	}
	return nil
}

func blockArity(b BlockType) (int, error) {
	if b.Empty {
		return 0, nil
	}
	// A value-typed block produces exactly one result (MVP has no
	// multi-value). The type itself is irrelevant to the emitter:
	// i32/i64 ride 8-byte slots identically.
	return 1, nil
}

func (c *funcCtx) localSlot(idx uint32) (int, error) {
	if int(idx) >= c.nlocals {
		return 0, fmt.Errorf("local %d out of range (have %d)", idx, c.nlocals)
	}
	return int(idx), nil
}

func loadAttrs(op byte) (size uint32, sext, wide bool) {
	switch op {
	case 0x28, 0x29, 0x2A:
		return 4, false, op == 0x29
	case 0x2B:
		return 8, false, true
	case 0x2C:
		return 1, true, false
	case 0x2D:
		return 1, false, false
	case 0x2E:
		return 2, true, false
	case 0x2F:
		return 2, false, false
	case 0x30:
		return 1, true, true
	case 0x31:
		return 1, false, true
	case 0x32:
		return 2, true, true
	case 0x33:
		return 2, false, true
	case 0x34:
		return 4, true, true
	case 0x35:
		return 4, false, true
	}
	return 4, false, false
}

func storeSize(op byte) uint32 {
	switch op {
	case 0x37, 0x39:
		return 8
	case 0x3A, 0x3C:
		return 1
	case 0x3B, 0x3D:
		return 2
	case 0x36, 0x38, 0x3E:
		return 4
	}
	return 4
}

// emitIntRel compiles i32/i64 comparisons. Depth: pop 2, push 1.
func (c *funcCtx) emitIntRel(op byte) error {
	a := c.a
	if op == 0x45 || op == 0x50 {
		if err := c.need(1); err != nil {
			return err
		}
		if op == 0x45 {
			a.Eqz32()
		} else {
			a.Eqz64()
		}
		return nil // machine pop+push nets zero; depth unchanged
	}
	wide := op >= 0x50
	var cc byte
	switch op {
	case 0x46, 0x51:
		cc = CC_EQ
	case 0x47, 0x52:
		cc = CC_NE
	case 0x48, 0x53:
		cc = CC_LTS
	case 0x49, 0x54:
		cc = CC_LTU
	case 0x4A, 0x55:
		cc = CC_GTS
	case 0x4B, 0x56:
		cc = CC_GTU
	case 0x4C, 0x57:
		cc = CC_LES
	case 0x4D, 0x58:
		cc = CC_LEU
	case 0x4E, 0x59:
		cc = CC_GES
	case 0x4F, 0x5A:
		cc = CC_GEU
	default:
		return fmt.Errorf("bad relop 0x%02x", op)
	}
	// Cmp helpers emit their own machine pops/pushes; account depth
	// arithmetically (pop 2, push 1).
	if err := c.need(2); err != nil {
		return err
	}
	if wide {
		a.Cmp64(cc)
	} else {
		a.Cmp32(cc)
	}
	c.depth--
	return nil
}

// emitIntALU compiles integer unary/binary ops.
func (c *funcCtx) emitIntALU(op byte) error {
	a := c.a
	is64 := op >= 0x79 && op <= 0x8A
	switch op {
	case 0x67, 0x79:
		if err := c.pop(1); err != nil {
			return err
		}
		if is64 {
			a.Clz64()
		} else {
			a.Clz32()
		}
		c.push(1)
	case 0x68, 0x7A:
		if err := c.pop(1); err != nil {
			return err
		}
		if is64 {
			a.Ctz64()
		} else {
			a.Ctz32()
		}
		c.push(1)
	case 0x69, 0x7B:
		if err := c.pop(1); err != nil {
			return err
		}
		if is64 {
			a.Popcnt64()
		} else {
			a.Popcnt32()
		}
		c.push(1)
	case 0x6A, 0x7C:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Add64()
		} else {
			a.Add32()
		}
		c.push(1)
	case 0x6B, 0x7D:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Sub64()
		} else {
			a.Sub32()
		}
		c.push(1)
	case 0x6C, 0x7E:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Mul64()
		} else {
			a.Mul32()
		}
		c.push(1)
	case 0x6D, 0x7F: // div_s
		if err := c.need(2); err != nil {
			return err
		}
		if is64 {
			a.DivRem64(true, false)
		} else {
			a.DivRem32(true, false)
		}
		c.depth--
	case 0x6E, 0x80: // div_u
		if err := c.need(2); err != nil {
			return err
		}
		if is64 {
			a.DivRem64(false, false)
		} else {
			a.DivRem32(false, false)
		}
		c.depth--
	case 0x6F, 0x81: // rem_s
		if err := c.need(2); err != nil {
			return err
		}
		if is64 {
			a.DivRem64(true, true)
		} else {
			a.DivRem32(true, true)
		}
		c.depth--
	case 0x70, 0x82: // rem_u
		if err := c.need(2); err != nil {
			return err
		}
		if is64 {
			a.DivRem64(false, true)
		} else {
			a.DivRem32(false, true)
		}
		c.depth--
	case 0x71, 0x83:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.And64()
		} else {
			a.And32()
		}
		c.push(1)
	case 0x72, 0x84:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Or64()
		} else {
			a.Or32()
		}
		c.push(1)
	case 0x73, 0x85:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Xor64()
		} else {
			a.Xor32()
		}
		c.push(1)
	case 0x74, 0x86:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Shift64(0xE0)
		} else {
			a.Shift32(0xE0)
		}
		c.push(1)
	case 0x75, 0x87:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Shift64(0xF8)
		} else {
			a.Shift32(0xF8)
		}
		c.push(1)
	case 0x76, 0x88:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Shift64(0xE8)
		} else {
			a.Shift32(0xE8)
		}
		c.push(1)
	case 0x77, 0x89:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Shift64(0xC0)
		} else {
			a.Shift32(0xC0)
		}
		c.push(1)
	case 0x78, 0x8A:
		if err := c.pop(2); err != nil {
			return err
		}
		if is64 {
			a.Shift64(0xC8)
		} else {
			a.Shift32(0xC8)
		}
		c.push(1)
	case 0xA7: // i32.wrap_i64
		if err := c.pop(1); err != nil {
			return err
		}
		a.popReg(RRAX)
		a.emit(0x89, 0xC0) // mov eax, eax (truncate; zeroes upper)
		a.pushReg(RRAX)
		c.push(1)
	default:
		return fmt.Errorf("bad ALU op 0x%02x", op)
	}
	return nil
}

// emitExtend compiles i32/i64 sign-extension ops.
func (c *funcCtx) emitExtend(op byte) error {
	a := c.a
	if err := c.pop(1); err != nil {
		return err
	}
	a.popReg(RRAX)
	switch op {
	case 0xC0: // i32.extend8_s
		a.emit(0x0F, 0xBE, 0xC0) // movsx eax, al
	case 0xC1: // i32.extend16_s
		a.emit(0x0F, 0xBF, 0xC0) // movsx eax, ax
	case 0xC2: // i64.extend8_s
		a.emit(0x48, 0x0F, 0xBE, 0xC0) // movsx rax, al
	case 0xC3: // i64.extend16_s
		a.emit(0x48, 0x0F, 0xBF, 0xC0)
	case 0xC4: // i64.extend32_s
		a.emit(0x48, 0x63, 0xC0) // movsxd rax, eax
	}
	a.pushReg(RRAX)
	c.push(1)
	return nil
}

// emitFloatRel compiles f32/f64 comparisons with exact NaN semantics.
func (c *funcCtx) emitFloatRel(op byte) error {
	a := c.a
	if err := c.need(2); err != nil {
		return err
	}
	wide := op >= 0x61
	base := op
	if wide {
		base -= 0x06 // 0x61..0x66 → 0x5B..0x60 shape
	}
	switch base {
	case 0x5B: // eq: ZF & !PF
		a.FloatCmp(wide, false, 0x94, true, false)
	case 0x5C: // ne: !ZF | PF
		a.FloatCmp(wide, false, 0x95, true, true)
	case 0x5D: // lt
		a.FloatCmp(wide, false, 0x92, true, false)
	case 0x5E: // gt = swapped lt
		a.FloatCmp(wide, true, 0x92, true, false)
	case 0x5F: // le
		a.FloatCmp(wide, false, 0x96, true, false)
	case 0x60: // ge = swapped le
		a.FloatCmp(wide, true, 0x96, true, false)
	default:
		return fmt.Errorf("bad float relop 0x%02x", op)
	}
	c.depth--
	return nil
}

// emitFloatALU compiles f32/f64 unary/binary ops.
func (c *funcCtx) emitFloatALU(op byte) error {
	a := c.a
	wide := op >= 0x99
	base := op
	if wide {
		base -= 0x0E // 0x99..0xA6 → 0x8B..0x98 shape
	}
	switch base {
	case 0x8B: // abs
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatAbsNeg(wide, false)
	case 0x8C: // neg
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatAbsNeg(wide, true)
	case 0x8D: // ceil
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatRound(wide, 2)
	case 0x8E: // floor
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatRound(wide, 1)
	case 0x8F: // trunc
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatRound(wide, 3)
	case 0x90: // nearest
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatRound(wide, 0)
	case 0x91: // sqrt
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatSqrt(wide)
	case 0x92, 0x93, 0x94, 0x95, 0x96, 0x97: // add..max
		if err := c.need(2); err != nil {
			return err
		}
		subop := map[byte]byte{
			0x92: 0x58, 0x93: 0x5C, 0x94: 0x59,
			0x95: 0x5E, 0x96: 0x5D, 0x97: 0x5F,
		}[base]
		a.FloatArith(wide, subop)
		c.depth--
	case 0x98: // copysign
		if err := c.need(2); err != nil {
			return err
		}
		a.CopySign(wide)
		c.depth--
	default:
		return fmt.Errorf("bad float ALU op 0x%02x", op)
	}
	return nil
}

// emitNumeric compiles conversions, demote/promote and reinterpret.
func (c *funcCtx) emitNumeric(op byte) error {
	a := c.a
	switch op {
	case 0xA8:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(false, false, false) // i32.trunc_f32_s
	case 0xA9:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(false, true, false)
	case 0xAA:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(false, false, true)
	case 0xAB:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(false, true, true)
	case 0xAC:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(true, false, false)
	case 0xAD:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(true, true, false)
	case 0xAE:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(true, false, true)
	case 0xAF:
		if err := c.need(1); err != nil {
			return err
		}
		a.FloatTrunc(true, true, true)
	case 0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7:
		if err := c.need(1); err != nil {
			return err
		}
		// B0..B3 = f32.convert_{i32,i32u,i64,i64u}; B4..B7 = f64.*.
		i := op - 0xB0
		a.FloatConvertInt(i >= 4, i%4 >= 2, i%2 == 1)
	case 0xB8: // f32.demote_f64
		if err := c.need(1); err != nil {
			return err
		}
		a.popReg(RRAX)
		a.emit(0x66, 0x48, 0x0F, 0x6E, 0xC0) // movq xmm0, rax
		a.emit(0xF2, 0x0F, 0x5A, 0xC0)       // cvtsd2ss xmm0, xmm0
		a.emit(0x66, 0x0F, 0x7E, 0xC0)       // movd eax, xmm0
		a.pushReg(RRAX)
	case 0xB9: // f64.promote_f32
		if err := c.need(1); err != nil {
			return err
		}
		a.popReg(RRAX)
		a.emit(0x66, 0x0F, 0x6E, 0xC0) // movd xmm0, eax
		a.emit(0xF3, 0x0F, 0x5A, 0xC0) // cvtss2sd xmm0, xmm0
		a.emit(0x66, 0x48, 0x0F, 0x7E, 0xC0)
		a.pushReg(RRAX)
	case 0xBA, 0xBB, 0xBC, 0xBD: // reinterpret: bit-identity move
	default:
		return fmt.Errorf("bad numeric op 0x%02x", op)
	}
	return nil
}

// emitFC compiles the v1 0xFC subset (memory.copy/fill, saturating
// truncations 0-7).
func (c *funcCtx) emitFC(o Op) error {
	switch o.Sub {
	case 0, 1, 2, 3, 4, 5, 6, 7: // trunc_sat (no traps; net-zero depth)
		if err := c.need(1); err != nil {
			return err
		}
		i := o.Sub
		// sub = toWide*4 + fromWide*2 + unsigned
		c.a.FloatSat(i >= 4, i%2 == 1, i&2 != 0)
	case 10: // memory.copy
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		if err := c.pop(3); err != nil {
			return err
		}
		c.a.MemoryCopy()
	case 11: // memory.fill
		if len(c.mod.Mems) == 0 {
			return ErrNoMemory
		}
		if err := c.pop(3); err != nil {
			return err
		}
		c.a.MemoryFill()
	default:
		return fmt.Errorf("%w: 0xfc sub 0x%x", ErrCompile, o.Sub)
	}
	return nil
}

// emitCall compiles a direct call: pop args, set R10 to the arg vector,
// CALL (linker-patched), push results.
func (c *funcCtx) emitCall(target uint32) error {
	ct, err := funcTypeOf(c.mod, target)
	if err != nil {
		return err
	}
	// Imported callee: resolved by KIND (producer owns import names;
	// the kernel trusts only the kind byte). Supported kinds emit
	// their per-site sequence with an import reloc the kernel patches
	// at bind; anything else fails closed here (compile error) so the
	// module falls back to INTERP.
	nf := 0
	for _, im := range c.mod.Imports {
		if im.Kind != 0 {
			continue
		}
		if uint32(nf) == target {
			if c.irelocs == nil {
				return fmt.Errorf("%w: import %s.%s (pure-only context)",
					ErrImport, im.Module, im.Name)
			}
			kind, ok := importKind(im.Module, im.Name)
			if !ok {
				return fmt.Errorf("%w: import %s.%s (no kind)",
					ErrImport, im.Module, im.Name)
			}
			if uint64(im.Type) >= uint64(len(c.mod.Types)) {
				return fmt.Errorf("jitc: import type %d out of range", im.Type)
			}
			ft := c.mod.Types[im.Type]
			switch kind {
			case ImportKindFdWrite:
				return c.emitImportFdWrite(im, ft, target)
			case ImportKindSchedYield:
				return c.emitImportSchedYield(im, ft, target)
			case ImportKindPortSend, ImportKindPortRecv,
				ImportKindBlkRead, ImportKindBlkWrite:
				return c.emitImportCall3(im, ft, target)
			case ImportKindPortCreate, ImportKindPortBind:
				return c.emitImportCall2(im, ft, target)
			default:
				return fmt.Errorf("%w: import %s.%s (kind %d unimplemented)",
					ErrImport, im.Module, im.Name, kind)
			}
		}
		nf++
	}
	if len(ct.Results) > 1 {
		return fmt.Errorf("multi-value callee unsupported")
	}
	if err := c.pop(len(ct.Params)); err != nil {
		return err
	}
	// R10 = rsp (arg vector: arg i at [R10+8*(nargs-1-i)]).
	c.a.emit(0x4C, 0x8D, 0x14, 0x24) // lea r10, [rsp]
	c.a.CallFunc(target)
	// The callee consumed args via the R10 pointer but the arg slots
	// still occupy the machine stack: drop them now (R10 is dead from
	// here; results arrive in RAX, not on the stack).
	c.a.addRsp(8 * len(ct.Params))
	// The callee's i32/f32 result may carry upper dirt (transports
	// preserve bits); re-clean here so every i32 slot stays
	// zero-extended. The type is known at this call site.
	if len(ct.Results) == 1 && (ct.Results[0] == VTi32 || ct.Results[0] == VTf32) {
		c.a.emit(0x89, 0xC0) // mov eax, eax (zero-extend)
	}
	for range ct.Results {
		c.a.pushReg(RRAX) // callees with >1 result already rejected
	}
	c.push(len(ct.Results))
	return nil
}

// emitImportFdWrite compiles a call to wasi fd_write(i32,i32,i32,i32)
// ->(i32): pops 4 args, emits the ImportFdWrite sequence with an
// import reloc, pushes the errno result.
func (c *funcCtx) emitImportFdWrite(im Import, ft FuncType, target uint32) error {
	if len(ft.Params) != 4 || len(ft.Results) != 1 {
		return fmt.Errorf("%w: fd_write bad arity", ErrImport)
	}
	for _, p := range ft.Params {
		if p != VTi32 {
			return fmt.Errorf("%w: fd_write bad param", ErrImport)
		}
	}
	if ft.Results[0] != VTi32 {
		return fmt.Errorf("%w: fd_write bad result", ErrImport)
	}
	if err := c.pop(4); err != nil {
		return err
	}
	relPos := c.a.ImportFdWrite(c.nlocals, c.depth)
	*c.irelocs = append(*c.irelocs, ImportReloc{
		CodeOff: uint32(relPos), Import: target, Func: c.fidx,
	})
	c.push(1)
	return nil
}

// emitImportCall3 compiles 3-arg (i32,i32,i32)->i32 imports sharing
// one marshalling shape: kern_port_send/recv and kern_blk_read/write.
// Pops 3 args, emits the shared sequence with an import reloc, pushes
// the status result. Per-kind semantics (ports vs block window, caps,
// sector math) live in the kernel handler, not here.
func (c *funcCtx) emitImportCall3(im Import, ft FuncType, target uint32) error {
	if len(ft.Params) != 3 || len(ft.Results) != 1 {
		return fmt.Errorf("%w: 3-arg call bad arity", ErrImport)
	}
	for _, p := range ft.Params {
		if p != VTi32 {
			return fmt.Errorf("%w: 3-arg call bad param", ErrImport)
		}
	}
	if ft.Results[0] != VTi32 {
		return fmt.Errorf("%w: 3-arg call bad result", ErrImport)
	}
	if err := c.pop(3); err != nil {
		return err
	}
	relPos := c.a.ImportCall3(c.nlocals, c.depth)
	*c.irelocs = append(*c.irelocs, ImportReloc{
		CodeOff: uint32(relPos), Import: target, Func: c.fidx,
	})
	c.push(1)
	return nil
}

// emitImportCall2 compiles 2-arg (i32,i32)->i32 imports sharing one
// marshalling shape: kern_port_create/bind. Pops 2 args, emits the
// shared sequence with an import reloc, pushes the handle result.
func (c *funcCtx) emitImportCall2(im Import, ft FuncType, target uint32) error {
	if len(ft.Params) != 2 || len(ft.Results) != 1 {
		return fmt.Errorf("%w: 2-arg call bad arity", ErrImport)
	}
	for _, p := range ft.Params {
		if p != VTi32 {
			return fmt.Errorf("%w: 2-arg call bad param", ErrImport)
		}
	}
	if ft.Results[0] != VTi32 {
		return fmt.Errorf("%w: 2-arg call bad result", ErrImport)
	}
	if err := c.pop(2); err != nil {
		return err
	}
	relPos := c.a.ImportCall2(c.nlocals, c.depth)
	*c.irelocs = append(*c.irelocs, ImportReloc{
		CodeOff: uint32(relPos), Import: target, Func: c.fidx,
	})
	c.push(1)
	return nil
}

// emitImportSchedYield compiles a call to wasi sched_yield, accepting
// both declared shapes (i() from the frozen profile, v() legacy per
// F58): pops nothing, emits the zero-arg sequence with an import
// reloc, pushes the errno result only for the i() shape.
func (c *funcCtx) emitImportSchedYield(im Import, ft FuncType, target uint32) error {
	if len(ft.Params) != 0 || len(ft.Results) > 1 {
		return fmt.Errorf("%w: sched_yield bad arity", ErrImport)
	}
	if len(ft.Results) == 1 && ft.Results[0] != VTi32 {
		return fmt.Errorf("%w: sched_yield bad result", ErrImport)
	}
	relPos := c.a.ImportSchedYield(c.nlocals, c.depth, len(ft.Results) == 1)
	*c.irelocs = append(*c.irelocs, ImportReloc{
		CodeOff: uint32(relPos), Import: target, Func: c.fidx,
	})
	if len(ft.Results) == 1 {
		c.push(1)
	}
	return nil
}
