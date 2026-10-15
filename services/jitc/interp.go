package jitc

import (
	"errors"
	"fmt"
	"math"
	"os"
)

// interp.go — tree-walking IR interpreter (Stage 2b-ii differential
// oracle). Executes the SAME decoded IR the emitter consumes, with
// textbook wasm semantics written directly against the spec (integer
// wrapping, IEEE-754 floats via Go's math package, exact trap
// conditions). Any divergence between interp and native on the same
// module+inputs is an emitter bug by construction.
//
// Contract split (mirrors the compiler):
//   - static error (Go error): invalid module constructs the compiler
//     also rejects (bad indices, immutable-global set, import calls,
//     non-const elem offsets). Differential harnesses SKIP these.
//   - runtime trap (Trap* code): OOB, div-by-zero, conversion failure,
//     unreachable, null/indirect-table mismatch. Harnesses compare
//     trap codes EXACTLY.

var ErrInterpImport = errors.New("jitc: interp hit an import call")

// ErrInterpBudget is returned when a call exceeds Interp.MaxSteps.
// Differential harnesses MUST set a budget: they execute arbitrary
// functions out of real modules, and a loop whose exit depends on data
// the harness does not supply (a scheduler search, a spin until a flag)
// never returns. Budget exhaustion is a skip, not a disagreement.
var ErrInterpBudget = errors.New("jitc: interp step budget exhausted")

// Interp holds instantiated module state for interpretation.
type Interp struct {
	mod     *Module
	mem     []byte
	globals []uint64
	tables  [][]uint32 // 0xFFFFFFFF = empty slot
	// Imports maps "module.name" to a host fake for testing import-
	// calling modules (the kernel serves these natively; interp has
	// no WASI). Absent entry = static error (ErrInterpImport).
	Imports map[string]func(args []uint64) ([]uint64, uint64, error)
	// MaxSteps bounds one execFunc call; 0 means unlimited. Harnesses
	// that feed arbitrary guest code should set it (see
	// ErrInterpBudget).
	MaxSteps uint64
}

// NewInterp instantiates memories/globals/tables. Memories start zeroed
// with Min pages; globals from const inits; tables from const-offset
// segments (non-const offsets and global.get inits are static errors).
func NewInterp(m *Module, maxPages int) (*Interp, error) {
	ip := &Interp{mod: m}
	if len(m.Mems) > 1 {
		return nil, errors.New("jitc: multi-memory unsupported")
	}
	if len(m.Mems) == 1 {
		if m.Mems[0].Min > uint32(maxPages) {
			return nil, errors.New("jitc: memory too large")
		}
		ip.mem = make([]byte, uint64(m.Mems[0].Min)*65536)
	}
	for _, g := range m.Globals {
		if !g.Const {
			return nil, errors.New("jitc: global.get init unsupported")
		}
		ip.globals = append(ip.globals, uint64(g.Init))
	}
	for _, n := range m.NTables {
		if n > 1<<20 {
			return nil, errors.New("jitc: table too large")
		}
		tab := make([]uint32, n)
		for i := range tab {
			tab[i] = 0xFFFFFFFF
		}
		ip.tables = append(ip.tables, tab)
	}
	for _, s := range m.Elems {
		if !s.Const {
			return nil, errors.New("jitc: non-const elem offset")
		}
		if int(s.Table) >= len(ip.tables) {
			return nil, fmt.Errorf("jitc: elem table %d out of range", s.Table)
		}
		tab := ip.tables[s.Table]
		for i, f := range s.Funcs {
			off := int64(s.Off) + int64(i)
			if off < 0 || off >= int64(len(tab)) {
				return nil, fmt.Errorf("jitc: elem writes out of table")
			}
			tab[off] = f
		}
	}
	return ip, nil
}

// Mem exposes linear memory for test setup/assertion.
func (ip *Interp) Mem() []byte { return ip.mem }

// ExecExport runs an exported function by name (direct execution, no
// trampoline needed).
func (ip *Interp) ExecExport(name string, args []uint64) ([]uint64, uint64, error) {
	for _, e := range ip.mod.Exports {
		if e.Name == name && e.Kind == 0 {
			return ip.execFunc(e.Index, args)
		}
	}
	return nil, 0, fmt.Errorf("jitc: export %s not found", name)
}

type iblock struct {
	isLoop     bool
	isIf       bool
	inElse     bool
	arity      int
	entryDepth int
	startPC    int
	elsePC     int
	endPC      int
}

// blockMap precomputes else/end targets for structured control.
func blockMap(ops []Op) (elsePC, endPC map[int]int, err error) {
	elsePC = map[int]int{}
	endPC = map[int]int{}
	type open struct {
		pc    int
		else_ int
	}
	var st []open
	for pc, o := range ops {
		switch o.Code {
		case 0x02, 0x03, 0x04:
			st = append(st, open{pc: pc, else_: -1})
		case 0x05:
			if len(st) == 0 {
				return nil, nil, fmt.Errorf("else outside block at %d", pc)
			}
			top := &st[len(st)-1]
			elsePC[top.pc] = pc
			top.else_ = pc
		case 0x0B:
			if len(st) == 0 {
				if pc != len(ops)-1 {
					return nil, nil, fmt.Errorf("stray end at %d", pc)
				}
				continue // function terminator
			}
			top := st[len(st)-1]
			st = st[:len(st)-1]
			endPC[top.pc] = pc
		}
	}
	if len(st) != 0 {
		return nil, nil, fmt.Errorf("unclosed blocks")
	}
	return elsePC, endPC, nil
}

func (ip *Interp) execFunc(fidx uint32, args []uint64) ([]uint64, uint64, error) {
	m := ip.mod
	nimport := m.ImportFuncCount()
	if int(fidx) < nimport {
		// Import call: resolve the k-th func import to a host fake.
		k := int(fidx)
		nf := 0
		for _, im := range m.Imports {
			if im.Kind != 0 {
				continue
			}
			if nf == k {
				fn := ip.Imports[im.Module+"."+im.Name]
				if fn == nil {
					return nil, 0, ErrInterpImport
				}
				ct, err := funcTypeOf(m, fidx)
				if err != nil {
					return nil, 0, err
				}
				if len(args) < len(ct.Params) {
					return nil, 0, fmt.Errorf("jitc: too few args")
				}
				res, trap, err := fn(args[:len(ct.Params)])
				if err != nil {
					return nil, 0, err
				}
				if trap != 0 {
					return nil, trap, nil
				}
				return append([]uint64(nil), res...), 0, nil
			}
			nf++
		}
		return nil, 0, ErrInterpImport
	}
	bi := int(fidx) - nimport
	if bi < 0 || bi >= len(m.Code) {
		return nil, 0, fmt.Errorf("jitc: func %d out of range", fidx)
	}
	ft, err := funcTypeOf(m, fidx)
	if err != nil {
		return nil, 0, err
	}
	if len(args) < len(ft.Params) {
		return nil, 0, fmt.Errorf("jitc: too few args")
	}
	fb := m.Code[bi]
	locals := make([]uint64, len(ft.Params)+len(fb.Locals))
	copy(locals, args)
	// Boundary cleaning (mirrors native CopyParams + call-site masking
	// exactly): i32/f32 params arrive zero-extended, so full-width
	// condition tests and low-32 consumers agree on both engines.
	for i, t := range ft.Params {
		if i < len(locals) && (t == VTi32 || t == VTf32) {
			locals[i] &= 0xFFFFFFFF
		}
	}
	for i, t := range fb.Locals {
		_ = t
		locals[len(ft.Params)+i] = 0
	}
	elsePC, endPC, err := blockMap(fb.Ops)
	if err != nil {
		return nil, 0, err
	}
	var stack []uint64
	var blocks []iblock
	push := func(v uint64) { stack = append(stack, v) }
	pop := func() (uint64, error) {
		if len(stack) == 0 {
			return 0, fmt.Errorf("jitc: interp stack underflow")
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, nil
	}
	popN := func(n int) ([]uint64, error) {
		if len(stack) < n {
			return nil, fmt.Errorf("jitc: interp stack underflow")
		}
		v := append([]uint64(nil), stack[len(stack)-n:]...)
		stack = stack[:len(stack)-n]
		return v, nil
	}
	depth := func() int { return len(stack) }
	pc := 0
	trace := os.Getenv("JITC_TRACE") != ""
	// Control flow mirrors real execution (NOT the compiler's linear
	// emission model): br pops exited frames and jumps pc; ends always
	// pair with the top frame because exited frames are gone and their
	// ends are skipped by the jumps. No dead flag is needed — the jump
	// itself skips fallthrough code. Loop back-edges re-enter with the
	// loop frame retained.
	steps := ip.MaxSteps
	for pc < len(fb.Ops) {
		if steps > 0 {
			if steps == 1 {
				return nil, 0, ErrInterpBudget
			}
			steps--
		}
		o := fb.Ops[pc]
		if trace {
			fmt.Fprintf(os.Stderr, "pc=%d op=%02x depth=%d blocks=%d\n",
				pc, o.Code, len(stack), len(blocks))
		}
		pc++
		switch o.Code {
		case 0x00:
			return nil, TrapUnreachable, nil
		case 0x01: // nop
		case 0x02, 0x03: // block, loop
			ar, err := blockArity(o.Block)
			if err != nil {
				return nil, 0, err
			}
			blocks = append(blocks, iblock{isLoop: o.Code == 0x03,
				arity: ar, entryDepth: depth(), startPC: pc - 1,
				endPC: endPC[pc-1]})
		case 0x04: // if
			ar, err := blockArity(o.Block)
			if err != nil {
				return nil, 0, err
			}
			c, err := pop()
			if err != nil {
				return nil, 0, err
			}
			// NOTE: raw == comparison would diverge from native on
			// dirty-upper i32 params (native tests eax). An i32
			// cond is nonzero iff its low 32 bits are; i64/f64
			// conds use full width. The type isn't tracked here,
			// so use low-32 semantics ONLY when the upper is dirt:
			// match native exactly — native always tests eax.
			// (If the cond is a genuine i64/f64, its producer left
			// full-width meaning in low32 only when... see below.)
			_ = c
			epc, hasElse := elsePC[pc-1]
			blocks = append(blocks, iblock{isIf: true, arity: ar,
				entryDepth: depth(), startPC: pc - 1,
				elsePC: epc, endPC: endPC[pc-1],
				inElse: !hasElse})
			if c == 0 {
				if hasElse {
					blocks[len(blocks)-1].inElse = true
					pc = epc + 1
				} else {
					blocks = blocks[:len(blocks)-1]
					pc = endPC[pc-1] + 1
				}
			}
		case 0x05: // else
			b := &blocks[len(blocks)-1]
			b.inElse = true
			pc = b.endPC // re-executed as end below... see note
			// NOTE: jumping to endPC lands ON the end op; the end
			// handler pops the frame and continues after it.
		case 0x0B: // end
			if len(blocks) == 0 {
				// function terminator
				if len(ft.Results) == 0 {
					return nil, 0, nil
				}
				if len(stack) != 1 {
					return nil, 0, fmt.Errorf("jitc: exit depth %d", len(stack))
				}
				return []uint64{stack[0]}, 0, nil
			}
			b := blocks[len(blocks)-1]
			blocks = blocks[:len(blocks)-1]
			_ = b
		case 0x0C, 0x0D: // br, br_if
			if o.Code == 0x0D {
				c, err := pop()
				if err != nil {
					return nil, 0, err
				}
				if c == 0 {
					continue
				}
			}
			npc, nd, _, err := ip.brTarget(&blocks, &stack, int(o.Label))
			if err != nil {
				return nil, 0, err
			}
			stack = stack[:nd]
			pc = npc
		case 0x0E: // br_table: scrutinee is i32 (low 32 bits —
			// native compares eax; upper dirt from transports is
			// invisible there, so mask here too or engines diverge
			// on dirty params)
			idx, err := pop()
			if err != nil {
				return nil, 0, err
			}
			idx &= 0xFFFFFFFF
			t := int(o.Table[len(o.Table)-1]) // default
			if idx < uint64(len(o.Table)-1) {
				t = int(o.Table[idx])
			}
			npc, nd, _, err := ip.brTarget(&blocks, &stack, t)
			if err != nil {
				return nil, 0, err
			}
			stack = stack[:nd]
			pc = npc
		case 0x0F: // return
			if len(ft.Results) == 0 {
				return nil, 0, nil
			}
			if len(stack) == 0 {
				return nil, 0, fmt.Errorf("jitc: return underflow")
			}
			return []uint64{stack[len(stack)-1]}, 0, nil
		case 0x10: // call
			ct, err := funcTypeOf(m, o.Func)
			if err != nil {
				return nil, 0, err
			}
			argv, err := popN(len(ct.Params))
			if err != nil {
				return nil, 0, err
			}
			res, trap, err := ip.execFunc(o.Func, argv)
			if err != nil {
				return nil, 0, err
			}
			if trap != 0 {
				return nil, trap, nil
			}
			stack = append(stack, cleanResults(ct, res)...)
		case 0x11: // call_indirect
			idx, err := pop()
			if err != nil {
				return nil, 0, err
			}
			if int(o.TableIdx) >= len(ip.tables) {
				return nil, 0, fmt.Errorf("jitc: table %d out of range", o.TableIdx)
			}
			tab := ip.tables[o.TableIdx]
			if idx >= uint64(len(tab)) || tab[idx] == 0xFFFFFFFF {
				return nil, TrapUnreachable, nil // null/unbound: trap
			}
			f := tab[idx]
			ct, err := funcTypeOf(m, o.Type)
			if err != nil {
				return nil, 0, err
			}
			actual, err := funcTypeOf(m, f)
			if err != nil {
				return nil, 0, err
			}
			if !sameType(ct, actual) {
				return nil, TrapUnreachable, nil // type mismatch: trap
			}
			argv, err := popN(len(ct.Params))
			if err != nil {
				return nil, 0, err
			}
			res, trap, err := ip.execFunc(f, argv)
			if err != nil {
				return nil, 0, err
			}
			if trap != 0 {
				return nil, trap, nil
			}
			stack = append(stack, cleanResults(ct, res)...)
		case 0x1A: // drop
			if _, err := pop(); err != nil {
				return nil, 0, err
			}
		case 0x1B: // select
			v, err := popN(3)
			if err != nil {
				return nil, 0, err
			}
			if v[2] != 0 {
				push(v[0])
			} else {
				push(v[1])
			}
		case 0x20, 0x22: // local.get/tee
			if int(o.Local) >= len(locals) {
				return nil, 0, fmt.Errorf("jitc: local %d OOR", o.Local)
			}
			push(locals[o.Local])
		case 0x21: // local.set
			if int(o.Local) >= len(locals) {
				return nil, 0, fmt.Errorf("jitc: local %d OOR", o.Local)
			}
			v, err := pop()
			if err != nil {
				return nil, 0, err
			}
			locals[o.Local] = v
		case 0x23: // global.get
			if int(o.Local) >= len(ip.globals) {
				return nil, 0, fmt.Errorf("jitc: global %d OOR", o.Local)
			}
			push(ip.globals[o.Local])
		case 0x24: // global.set
			if int(o.Local) >= len(ip.globals) {
				return nil, 0, fmt.Errorf("jitc: global %d OOR", o.Local)
			}
			if ip.mod.Globals[o.Local].Mut == 0 {
				return nil, 0, fmt.Errorf("jitc: set immutable global")
			}
			v, err := pop()
			if err != nil {
				return nil, 0, err
			}
			ip.globals[o.Local] = v
		case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
			0x30, 0x31, 0x32, 0x33, 0x34, 0x35: // loads
			if len(ip.mem) == 0 {
				return nil, 0, ErrNoMemory
			}
			ea, err := pop()
			if err != nil {
				return nil, 0, err
			}
			sz, sext, wide := loadAttrs(o.Code)
			a := uint64(uint32(ea)) + uint64(o.Mem.Offset)
			if a+uint64(sz) > uint64(len(ip.mem)) {
				return nil, TrapOOB, nil
			}
			var v uint64
			for i := uint32(0); i < sz; i++ {
				v |= uint64(ip.mem[a+uint64(i)]) << (8 * i)
			}
			if sext {
				if sz == 1 && v&0x80 != 0 {
					v |= 0xFFFFFFFFFFFFFF00
				}
				if sz == 2 && v&0x8000 != 0 {
					v |= 0xFFFFFFFFFFFF0000
				}
				if sz == 4 && wide && v&0x80000000 != 0 {
					v |= 0xFFFFFFFF00000000
				}
			} else if !wide {
				v &= 0xFFFFFFFF
			}
			push(v)
		case 0x36, 0x37, 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E: // stores
			if len(ip.mem) == 0 {
				return nil, 0, ErrNoMemory
			}
			v, err := popN(2)
			if err != nil {
				return nil, 0, err
			}
			sz := storeSize(o.Code)
			a := uint64(uint32(v[0])) + uint64(o.Mem.Offset)
			if a+uint64(sz) > uint64(len(ip.mem)) {
				return nil, TrapOOB, nil
			}
			for i := uint32(0); i < sz; i++ {
				ip.mem[a+uint64(i)] = byte(v[1] >> (8 * i))
			}
		case 0x3F: // memory.size
			if len(ip.mem) == 0 {
				return nil, 0, ErrNoMemory
			}
			push(uint64(len(ip.mem)) >> 16)
		case 0x40: // memory.grow → -1 (matches emitter stub)
			if len(ip.mem) == 0 {
				return nil, 0, ErrNoMemory
			}
			if _, err := pop(); err != nil {
				return nil, 0, err
			}
			push(0xFFFFFFFFFFFFFFFF)
		case 0x41:
			push(uint64(uint32(o.I32)))
		case 0x42:
			push(uint64(o.I64))
		case 0x43:
			push(uint64(o.F32))
		case 0x44:
			push(o.F64)
		default:
			if o.Code >= 0x45 && o.Code <= 0x5A {
				if tr, err := ip.aluInt(&stack, o.Code); err != nil {
					return nil, 0, err
				} else if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if o.Code >= 0x5B && o.Code <= 0x66 {
				if tr, err := ip.aluFloat(&stack, o.Code); err != nil {
					return nil, 0, err
				} else if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if (o.Code >= 0x67 && o.Code <= 0x8A) || o.Code == 0xA7 {
				if tr, err := ip.aluInt(&stack, o.Code); err != nil {
					return nil, 0, err
				} else if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if o.Code >= 0x8B && o.Code <= 0xA6 {
				if tr, err := ip.aluFloat(&stack, o.Code); err != nil {
					return nil, 0, err
				} else if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if o.Code >= 0xA8 && o.Code <= 0xBD {
				tr, err := ip.aluConvert(&stack, o.Code)
				if err != nil {
					return nil, 0, err
				}
				if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if o.Code >= 0xC0 && o.Code <= 0xC4 {
				if tr, err := ip.aluInt(&stack, o.Code); err != nil {
					return nil, 0, err
				} else if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			if o.Code == 0xFC {
				tr, err := ip.aluFC(&stack, o)
				if err != nil {
					return nil, 0, err
				}
				if tr != 0 {
					return nil, tr, nil
				}
				continue
			}
			return nil, 0, fmt.Errorf("jitc: interp unhandled op 0x%02x", o.Code)
		}
	}
	return nil, 0, fmt.Errorf("jitc: fell off function end")
}

// brTarget resolves a branch to (nextPC, newDepth, reenteredLoop) and
// pops exited frames (all frames above the target, plus the target
// itself unless it is a re-entered loop). Later ends therefore always
// pair with their own frames. The caller truncates the value stack to
// newDepth, preserving exactly the arity result values on top.
func (ip *Interp) brTarget(blocks *[]iblock, stack *[]uint64, to int) (int, int, bool, error) {
	bs := *blocks
	if to < 0 || to >= len(bs) {
		return 0, 0, false, fmt.Errorf("jitc: bad label %d", to)
	}
	b := bs[len(bs)-1-to]
	if b.isLoop {
		return b.startPC + 1, b.entryDepth, true, nil
	}
	*blocks = bs[:len(bs)-1-to]
	return b.endPC + 1, b.entryDepth + b.arity, false, nil
}

// cleanResults zero-extends i32/f32 call results, mirroring the
// native call-site masking (compile.go emitCall). Keeps both engines'
// slot discipline identical so full-width tests agree.
func cleanResults(ct FuncType, res []uint64) []uint64 {
	for i, t := range ct.Results {
		if i < len(res) && (t == VTi32 || t == VTf32) {
			res[i] &= 0xFFFFFFFF
		}
	}
	return res
}

func sameType(a, b FuncType) bool {
	if len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Params {
		if a.Params[i] != b.Params[i] {
			return false
		}
	}
	for i := range a.Results {
		if a.Results[i] != b.Results[i] {
			return false
		}
	}
	return true
}

var _ = math.Pi
