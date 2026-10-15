package jitc

import (
	"errors"
	"fmt"
)

var (
	ErrBadMagic   = errors.New("jitc: bad magic")
	ErrBadVersion = errors.New("jitc: unsupported version")
	ErrBadSection = errors.New("jitc: bad section")
	ErrBodySize   = errors.New("jitc: function body overruns code section")
)

// Value types (MVP).
const (
	VTi32 = 0x7F
	VTi64 = 0x7E
	VTf32 = 0x7D
	VTf64 = 0x7C
)

// Section IDs (MVP).
const (
	SecCustom = 0
	SecType   = 1
	SecImport = 2
	SecFunc   = 3
	SecTable  = 4
	SecMem    = 5
	SecGlobal = 6
	SecExport = 7
	SecStart  = 8
	SecElem   = 9
	SecCode   = 10
	SecData   = 11
)

// FuncType is a decoded type-section entry.
type FuncType struct {
	Params  []byte
	Results []byte
}

// Import is a decoded import-section entry.
type Import struct {
	Module string
	Name   string
	Kind   byte
	Type   uint32 // func: type index; memory/table/global: descriptor skipped
}

// Export is a decoded export-section entry.
type Export struct {
	Name  string
	Kind  byte
	Index uint32
}

// Memory is a decoded memory-section entry.
type Memory struct {
	Min uint32
	Max uint32
	Has bool // Has==false when Max absent
}

// ElemSeg is a decoded active element segment (function indices for a
// table at a constant offset; non-const offsets are recorded with
// Const==false for the executor to reject).
type ElemSeg struct {
	Table uint32
	Const bool
	Off   int32
	Funcs []uint32
}

// DataSegRaw is a decoded active data segment (initial memory bytes).
// Non-const offsets record Const==false (cache builds reject them;
// the interpreter evaluates only const forms).
type DataSegRaw struct {
	Mem   uint32
	Const bool
	Off   int32
	Bytes []byte
}

// Global is a decoded global-section entry. Init holds a constant
// initializer value; Const==false means the initializer references
// another global (Stage 2 rejects executing such modules).
type Global struct {
	Type  byte
	Mut   byte
	Const bool
	Init  int64
}

// CustomSec records a custom section (kept, not interpreted, except
// that HasABIVer reports the kernel's abi_ver marker).
type CustomSec struct {
	Name string
	Data []byte
}

// Module is a decoded wasm module. Unknown/unsupported sections beyond
// the MVP set cause an error only if they are structurally malformed;
// unrecognised *content* inside recognised sections is rejected so the
// emitter stage never sees constructs it cannot compile.
type Module struct {
	Types   []FuncType
	Imports []Import
	Funcs   []uint32 // type index per defined function
	Mems    []Memory
	Globals []Global
	NTables []uint32 // minimum size per table (MVP: at most 1)
	Elems   []ElemSeg
	Data    []DataSegRaw
	Exports []Export
	Start   uint32
	HasSt   bool
	Code    []FuncBody
	Custom  []CustomSec
}

// HasABIVer returns the abi_ver custom-section marker byte and true if
// the module carries the kernel's `abi_ver` custom section.
func (m *Module) HasABIVer() (byte, bool) {
	for _, c := range m.Custom {
		if c.Name == "abi_ver" && len(c.Data) >= 1 {
			return c.Data[0], true
		}
	}
	return 0, false
}

// ImportFuncCount returns the number of imported (not defined) functions.
func (m *Module) ImportFuncCount() int {
	n := 0
	for _, im := range m.Imports {
		if im.Kind == 0 {
			n++
		}
	}
	return n
}

// Decode parses a complete wasm module. It never panics.
func Decode(b []byte) (m *Module, err error) {
	defer func() {
		if r := recover(); r != nil {
			m = nil
			err = fmt.Errorf("jitc: internal decoder panic: %v", r)
		}
	}()
	c := &cursor{b: b}
	magic, err := c.bytes(4)
	if err != nil {
		return nil, err
	}
	if magic[0] != 0x00 || magic[1] != 0x61 || magic[2] != 0x73 || magic[3] != 0x6D {
		return nil, ErrBadMagic
	}
	ver, err := c.bytes(4)
	if err != nil {
		return nil, err
	}
	if ver[0] != 0x01 || ver[1] != 0x00 || ver[2] != 0x00 || ver[3] != 0x00 {
		return nil, ErrBadVersion
	}
	m = &Module{}
	for c.rest() > 0 {
		id, err := c.u8()
		if err != nil {
			return nil, err
		}
		sz, err := c.u32leb()
		if err != nil {
			return nil, err
		}
		payload, err := c.bytes(int(sz))
		if err != nil {
			return nil, err
		}
		if err := m.decodeSection(id, payload, c.off-int(sz)); err != nil {
			return nil, fmt.Errorf("jitc: section %d: %w", id, err)
		}
	}
	if len(m.Funcs) != len(m.Code) && len(m.Code) != 0 {
		return nil, fmt.Errorf("jitc: func/code count mismatch %d/%d",
			len(m.Funcs), len(m.Code))
	}
	return m, nil
}

func validValType(v byte) bool {
	return v == VTi32 || v == VTi64 || v == VTf32 || v == VTf64
}

func (m *Module) decodeSection(id byte, p []byte, base int) error {
	c := &cursor{b: p}
	switch id {
	case SecCustom:
		name, err := c.name()
		if err != nil {
			return err
		}
		m.Custom = append(m.Custom, CustomSec{Name: name, Data: p[c.off:]})
		return nil
	case SecType:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			tag, err := c.u8()
			if err != nil {
				return err
			}
			if tag != 0x60 {
				return fmt.Errorf("jitc: bad functype tag 0x%x", tag)
			}
			ft := FuncType{}
			np, err := c.vecLen()
			if err != nil {
				return err
			}
			for j := uint32(0); j < np; j++ {
				v, err := c.u8()
				if err != nil {
					return err
				}
				if !validValType(v) {
					return fmt.Errorf("jitc: bad param type 0x%x", v)
				}
				ft.Params = append(ft.Params, v)
			}
			nr, err := c.vecLen()
			if err != nil {
				return err
			}
			for j := uint32(0); j < nr; j++ {
				v, err := c.u8()
				if err != nil {
					return err
				}
				if !validValType(v) {
					return fmt.Errorf("jitc: bad result type 0x%x", v)
				}
				ft.Results = append(ft.Results, v)
			}
			m.Types = append(m.Types, ft)
		}
		return nil
	case SecImport:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			mod, err := c.name()
			if err != nil {
				return err
			}
			nm, err := c.name()
			if err != nil {
				return err
			}
			kind, err := c.u8()
			if err != nil {
				return err
			}
			im := Import{Module: mod, Name: nm, Kind: kind}
			switch kind {
			case 0: // func: type index
				t, err := c.u32leb()
				if err != nil {
					return err
				}
				im.Type = t
			case 1: // table: reftype + limits
				if _, err := c.u8(); err != nil {
					return err
				}
				if err := skipLimits(c); err != nil {
					return err
				}
			case 2: // memory: limits
				if err := skipLimits(c); err != nil {
					return err
				}
			case 3: // global: valtype + mutability
				if _, err := c.u8(); err != nil {
					return err
				}
				if _, err := c.u8(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("jitc: bad import kind %d", kind)
			}
			m.Imports = append(m.Imports, im)
		}
		return nil
	case SecFunc:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			t, err := c.u32leb()
			if err != nil {
				return err
			}
			if uint64(t) >= uint64(len(m.Types)) {
				return fmt.Errorf("jitc: type index %d out of range", t)
			}
			m.Funcs = append(m.Funcs, t)
		}
		return nil
	case SecTable:
		// MVP baseline: tables exist only for call_indirect; record
		// minimum sizes so the interpreter can size them.
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			if _, err := c.u8(); err != nil {
				return err
			}
			flags, err := c.u32leb()
			if err != nil {
				return err
			}
			if flags > 1 {
				return fmt.Errorf("jitc: bad table limits flags %d", flags)
			}
			mn, err := c.u32leb()
			if err != nil {
				return err
			}
			if flags == 1 {
				if _, err := c.u32leb(); err != nil {
					return err
				}
			}
			m.NTables = append(m.NTables, mn)
		}
		return nil
	case SecMem:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			flags, err := c.u32leb()
			if err != nil {
				return err
			}
			if flags > 1 {
				return fmt.Errorf("jitc: bad memory flags %d", flags)
			}
			mn, err := c.u32leb()
			if err != nil {
				return err
			}
			mem := Memory{Min: mn}
			if flags == 1 {
				mx, err := c.u32leb()
				if err != nil {
					return err
				}
				mem.Max, mem.Has = mx, true
			}
			m.Mems = append(m.Mems, mem)
		}
		return nil
	case SecGlobal:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			vt, err := c.u8() // valtype
			if err != nil {
				return err
			}
			if !validValType(vt) {
				return fmt.Errorf("jitc: bad global type 0x%x", vt)
			}
			mut, err := c.u8() // mutability
			if err != nil {
				return err
			}
			if mut > 1 {
				return fmt.Errorf("jitc: bad global mutability %d", mut)
			}
			g := Global{Type: vt, Mut: mut}
			// init expr: const op + operand + end (global.get refs
			// recorded as non-const for the executor to reject).
			op, err := c.u8()
			if err != nil {
				return err
			}
			switch op {
			case 0x41: // i32.const
				v, err := c.s32leb()
				if err != nil {
					return err
				}
				g.Const, g.Init = true, int64(v)
			case 0x42: // i64.const
				v, err := c.s64leb()
				if err != nil {
					return err
				}
				g.Const, g.Init = true, v
			case 0x43: // f32.const
				raw, err := c.bytes(4)
				if err != nil {
					return err
				}
				g.Const = true
				g.Init = int64(uint32(raw[0]) | uint32(raw[1])<<8 |
					uint32(raw[2])<<16 | uint32(raw[3])<<24)
			case 0x44: // f64.const
				raw, err := c.bytes(8)
				if err != nil {
					return err
				}
				var v int64
				for i := 0; i < 8; i++ {
					v |= int64(raw[i]) << (8 * i)
				}
				g.Const, g.Init = true, v
			case 0x23: // global.get: reference, resolved at exec
				if _, err := c.u32leb(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("jitc: bad global init op 0x%x", op)
			}
			if end, err := c.u8(); err != nil || end != 0x0B {
				if err != nil {
					return err
				}
				return fmt.Errorf("jitc: bad global init end 0x%x", end)
			}
			m.Globals = append(m.Globals, g)
		}
		return nil
	case SecExport:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			nm, err := c.name()
			if err != nil {
				return err
			}
			kind, err := c.u8()
			if err != nil {
				return err
			}
			idx, err := c.u32leb()
			if err != nil {
				return err
			}
			if kind > 3 {
				return fmt.Errorf("jitc: bad export kind %d", kind)
			}
			m.Exports = append(m.Exports, Export{Name: nm, Kind: kind, Index: idx})
		}
		return nil
	case SecStart:
		s, err := c.u32leb()
		if err != nil {
			return err
		}
		m.Start, m.HasSt = s, true
		return nil
	case SecElem:
		// Active segments: table index, offset expr, func index
		// vector. Offsets must be i32.const for the interpreter;
		// anything else is recorded non-const (executor rejects).
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			tb, err := c.u32leb() // tableidx
			if err != nil {
				return err
			}
			seg := ElemSeg{Table: tb, Const: true}
			op, err := c.u8() // offset expr: single const + end
			if err != nil {
				return err
			}
			switch op {
			case 0x41: // i32.const
				v, err := c.s32leb()
				if err != nil {
					return err
				}
				seg.Off = v
			case 0x23: // global.get: non-const for our purposes
				if _, err := c.u32leb(); err != nil {
					return err
				}
				seg.Const = false
			default:
				return fmt.Errorf("jitc: bad elem offset op 0x%x", op)
			}
			if end, err := c.u8(); err != nil || end != 0x0B {
				if err != nil {
					return err
				}
				return fmt.Errorf("jitc: bad elem offset end 0x%x", end)
			}
			nf, err := c.vecLen()
			if err != nil {
				return err
			}
			for j := uint32(0); j < nf; j++ {
				f, err := c.u32leb()
				if err != nil {
					return err
				}
				seg.Funcs = append(seg.Funcs, f)
			}
			m.Elems = append(m.Elems, seg)
		}
		return nil
	case SecCode:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			offBefore := c.off
			sz, err := c.u32leb()
			if err != nil {
				return err
			}
			body, err := c.bytes(int(sz))
			if err != nil {
				return ErrBodySize
			}
			fb, err := decodeFuncBody(body)
			if err != nil {
				return fmt.Errorf("jitc: func %d: %w", len(m.Code), err)
			}
			fb.WasmOff = uint32(base + offBefore)
			m.Code = append(m.Code, fb)
		}
		return nil
	case SecData:
		n, err := c.vecLen()
		if err != nil {
			return err
		}
		for i := uint32(0); i < n; i++ {
			mem, err := c.u32leb() // memidx (must be 0, single memory)
			if err != nil {
				return err
			}
			seg := DataSegRaw{Mem: mem, Const: true}
			op, err := c.u8() // offset expr: single const + end
			if err != nil {
				return err
			}
			switch op {
			case 0x41: // i32.const
				v, err := c.s32leb()
				if err != nil {
					return err
				}
				seg.Off = v
			case 0x23: // global.get: recorded non-const
				if _, err := c.u32leb(); err != nil {
					return err
				}
				seg.Const = false
			default:
				return fmt.Errorf("jitc: bad data offset op 0x%x", op)
			}
			if end, err := c.u8(); err != nil || end != 0x0B {
				if err != nil {
					return err
				}
				return fmt.Errorf("jitc: bad data offset end 0x%x", end)
			}
			sz, err := c.u32leb()
			if err != nil {
				return err
			}
			bytes, err := c.bytes(int(sz))
			if err != nil {
				return err
			}
			cp := append([]byte(nil), bytes...)
			seg.Bytes = cp
			m.Data = append(m.Data, seg)
		}
		return nil
	default:
		return ErrBadSection
	}
}

func skipLimits(c *cursor) error {
	flags, err := c.u32leb()
	if err != nil {
		return err
	}
	if flags > 1 {
		return fmt.Errorf("jitc: bad limits flags %d", flags)
	}
	if _, err := c.u32leb(); err != nil {
		return err
	}
	if flags == 1 {
		if _, err := c.u32leb(); err != nil {
			return err
		}
	}
	return nil
}

// skipInitExpr skips a constant init expression (op...0x0B).
func skipInitExpr(c *cursor) error {
	for {
		op, err := c.u8()
		if err != nil {
			return err
		}
		switch op {
		case 0x0B: // end
			return nil
		case 0x41: // i32.const
			if _, err := c.s32leb(); err != nil {
				return err
			}
		case 0x42: // i64.const
			if _, err := c.s64leb(); err != nil {
				return err
			}
		case 0x43: // f32.const
			if _, err := c.bytes(4); err != nil {
				return err
			}
		case 0x44: // f64.const
			if _, err := c.bytes(8); err != nil {
				return err
			}
		case 0x23: // global.get
			if _, err := c.u32leb(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("jitc: bad init op 0x%x", op)
		}
	}
}
