package jitc

import (
	"errors"
	"fmt"
)

var ErrUnsupportedOp = errors.New("jitc: unsupported opcode for v1 baseline")

// BlockType is a block/loop/if immediate: empty (0x40) or a value type.
type BlockType struct {
	Empty bool
	Value byte
}

// MemArg is a load/store immediate (align is advisory; offset applies).
type MemArg struct {
	Align  uint32
	Offset uint32
}

// Op is one decoded instruction: Code plus at most one immediate,
// stored in the matching field. Stage 2 (emitter) consumes this IR;
// anything it cannot compile is rejected HERE with ErrUnsupportedOp,
// so emitter coverage == decoder coverage by construction.
type Op struct {
	Code byte
	// immediates (only the relevant field is set):
	Block    BlockType // block/loop/if
	Label    uint32    // br/br_if
	Table    []uint32  // br_table targets (last entry = default)
	Sub      uint32    // 0xFC prefix sub-opcode
	Func     uint32    // call
	TableIdx uint32    // call_indirect table index
	Type     uint32    // call_indirect type index
	Local    uint32    // local.get/set/tee, global.get/set
	Mem      MemArg    // loads/stores
	I32      int32     // i32.const
	I64      int64     // i64.const
	F32      uint32    // f32.const bits (Stage 2 outlines float ops)
	F64      uint64    // f64.const bits (Stage 2 outlines float ops)
}

// FuncBody is a decoded code-section entry: declared locals plus the
// instruction stream including the terminating end. WasmOff is the
// absolute file offset of the body's size field — byte-identical to
// wasm3's (function->wasm - module base), which is what the kernel's
// native dispatch lookup keys on (v4 container table).
type FuncBody struct {
	Locals  []byte // expanded: one entry per local slot
	Ops     []Op
	WasmOff uint32
}

// LocalCount reports parameter + declared-local slot count base used by
// local-index validation (params are prepended by the caller).
func (f *FuncBody) LocalCount(nparams int) int { return nparams + len(f.Locals) }

func decodeBlockType(c *cursor) (BlockType, error) {
	b, err := c.u8()
	if err != nil {
		return BlockType{}, err
	}
	if b == 0x40 {
		return BlockType{Empty: true}, nil
	}
	if !validValType(b) {
		return BlockType{}, fmt.Errorf("%w: bad blocktype 0x%x", ErrUnsupportedOp, b)
	}
	return BlockType{Value: b}, nil
}

func decodeMemArg(c *cursor) (MemArg, error) {
	var m MemArg
	var err error
	if m.Align, err = c.u32leb(); err != nil {
		return m, err
	}
	if m.Offset, err = c.u32leb(); err != nil {
		return m, err
	}
	return m, nil
}

// decodeFuncBody parses one code entry. Supported: the v1 baseline
// integer/control subset below; everything else (floats, SIMD,
// call_indirect, select with typed immediates beyond MVP) is rejected
// with ErrUnsupportedOp so Stage 2 never meets a surprise.
func decodeFuncBody(body []byte) (FuncBody, error) {
	fb := FuncBody{}
	c := &cursor{b: body}
	nl, err := c.vecLen()
	if err != nil {
		return fb, err
	}
	for i := uint32(0); i < nl; i++ {
		n, err := c.u32leb()
		if err != nil {
			return fb, err
		}
		if uint64(n) > uint64(c.rest()) {
			return fb, ErrTruncated
		}
		t, err := c.u8()
		if err != nil {
			return fb, err
		}
		if !validValType(t) {
			return fb, fmt.Errorf("jitc: bad local type 0x%x", t)
		}
		for j := uint32(0); j < n; j++ {
			fb.Locals = append(fb.Locals, t)
		}
	}
	depth := 0
	for c.rest() > 0 {
		op, err := c.u8()
		if err != nil {
			return fb, err
		}
		var o Op
		o.Code = op
		switch op {
		case 0x00, 0x01, 0x05, 0x0B, 0x0F, 0x1A, 0x1B:
			// unreachable, nop, else, end, return, drop, select
		case 0x02, 0x03, 0x04: // block, loop, if
			bt, err := decodeBlockType(c)
			if err != nil {
				return fb, err
			}
			o.Block = bt
		case 0x0C, 0x0D: // br, br_if
			l, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.Label = l
		case 0x0E: // br_table: vec of labels + default
			n, err := c.vecLen()
			if err != nil {
				return fb, err
			}
			for i := uint32(0); i < n; i++ {
				l, err := c.u32leb()
				if err != nil {
					return fb, err
				}
				o.Table = append(o.Table, l)
			}
			d, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.Table = append(o.Table, d)
		case 0xC0, 0xC1, 0xC2, 0xC3, 0xC4:
			// i32/i64 sign-extension (no immediate)
		case 0xFC: // misc prefix: sub-opcode + operands
			sub, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.Sub = sub
			switch sub {
			case 0, 1, 2, 3, 4, 5, 6, 7:
				// trunc_sat family: no further immediates
			case 10: // memory.copy: two reserved bytes
				for i := 0; i < 2; i++ {
					r, err := c.u8()
					if err != nil {
						return fb, err
					}
					if r != 0x00 {
						return fb, fmt.Errorf("jitc: bad memory.copy reserved 0x%x", r)
					}
				}
			case 11: // memory.fill: one reserved byte
				r, err := c.u8()
				if err != nil {
					return fb, err
				}
				if r != 0x00 {
					return fb, fmt.Errorf("jitc: bad memory.fill reserved 0x%x", r)
				}
			default:
				return fb, fmt.Errorf("%w: 0xfc sub 0x%x", ErrUnsupportedOp, sub)
			}
		case 0x10: // call
			f, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.Func = f
		case 0x11: // call_indirect: tableidx + typeidx immediates.
			// Decoded structurally (Stage 2 emits a table-bounds/type
			// check + indirect call via a runtime helper; tables are
			// kernel-managed, resolved at cache-load time).
			ti, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			ty, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.TableIdx, o.Type = ti, ty
		case 0x20, 0x21, 0x22, 0x23, 0x24: // locals, globals
			l, err := c.u32leb()
			if err != nil {
				return fb, err
			}
			o.Local = l
		case 0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, // i32/i64/f32/f64 loads
			0x2E, 0x2F, 0x30, 0x31, 0x32, 0x33, // ext loads
			0x34, 0x35, // i64.load32_s/u
			0x36, 0x37, 0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E: // stores
			m, err := decodeMemArg(c)
			if err != nil {
				return fb, err
			}
			o.Mem = m
		case 0x3F, 0x40: // memory.size, memory.grow
			r, err := c.u8() // reserved byte, must be 0x00
			if err != nil {
				return fb, err
			}
			if r != 0x00 {
				return fb, fmt.Errorf("jitc: bad memory reserved 0x%x", r)
			}
		case 0x41: // i32.const
			v, err := c.s32leb()
			if err != nil {
				return fb, err
			}
			o.I32 = v
		case 0x42: // i64.const
			v, err := c.s64leb()
			if err != nil {
				return fb, err
			}
			o.I64 = v
		case 0x43: // f32.const: raw bits, outlined in Stage 2
			raw, err := c.bytes(4)
			if err != nil {
				return fb, err
			}
			o.F32 = uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24
		case 0x44: // f64.const: raw bits, outlined in Stage 2
			raw, err := c.bytes(8)
			if err != nil {
				return fb, err
			}
			var v uint64
			for i := 0; i < 8; i++ {
				v |= uint64(raw[i]) << (8 * i)
			}
			o.F64 = v
		case 0x45, 0x46, 0x47, 0x48, 0x49, 0x4A, // i32 eqz..ge_s
			0x4B, 0x4C, 0x4D, 0x4E, 0x4F, 0x50, // i32 ge_u + i64 eqz
			0x51, 0x52, 0x53, 0x54, 0x55, 0x56, // i64 eq..gt_u
			0x57, 0x58, 0x59, 0x5A, // i64 le_s..ge_u
			0x67, 0x68, 0x69, // i32 clz/ctz/popcnt
			0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F, // i32 add..rotr
			0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78,
			0x79, 0x7A, 0x7B, // i64 clz..popcnt
			0x7C, 0x7D, 0x7E, 0x7F, 0x80, 0x81, // i64 add..rotr
			0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8A,
			0x5B, 0x5C, 0x5D, 0x5E, 0x5F, 0x60, // f32 relop
			0x61, 0x62, 0x63, 0x64, 0x65, 0x66, // f64 relop
			0x8B, 0x8C, 0x8D, 0x8E, 0x8F, 0x90, // f32 abs..nearest
			0x91, 0x92, 0x93, 0x94, 0x95, 0x96, // f32 sqrt..min
			0x97, 0x98, // f32 max/copysign
			0x99, 0x9A, 0x9B, 0x9C, 0x9D, 0x9E, // f64 abs..nearest
			0x9F, 0xA0, 0xA1, 0xA2, 0xA3, 0xA4, // f64 sqrt..min
			0xA5, 0xA6, // f64 max/copysign
			0xA7,                   // i32.wrap_i64
			0xA8, 0xA9, 0xAA, 0xAB, // i32.trunc_f32/64_s/u
			0xAC, 0xAD, 0xAE, 0xAF, // i64.trunc_f32/64_s/u
			0xB0, 0xB1, 0xB2, 0xB3, // f32.convert_i32/64_s/u
			0xB4, 0xB5, 0xB6, 0xB7, // f64.convert_i32/64_s/u
			0xB8, 0xB9, // f32.demote_f64, f64.promote_f32
			0xBA, 0xBB, 0xBC, 0xBD: // reinterpret family
			// pure integer ALU + float/int conversions: no immediate.
			// (Stage 2 note: float ops are decoded structurally here so
			// coverage is measurable; the emitter will outline them to
			// runtime helpers rather than inline x87/SSE.)
		default:
			return fb, fmt.Errorf("%w: 0x%02x", ErrUnsupportedOp, op)
		}
		// Track block nesting so the function's terminating end is the
		// end at depth 0; block-local ends just close one level.
		switch op {
		case 0x02, 0x03, 0x04: // block, loop, if
			depth++
		case 0x0B: // end
			if depth == 0 {
				fb.Ops = append(fb.Ops, o)
				goto done
			}
			depth--
		}
		fb.Ops = append(fb.Ops, o)
	}
done:
	if len(fb.Ops) == 0 || fb.Ops[len(fb.Ops)-1].Code != 0x0B {
		return fb, fmt.Errorf("jitc: body missing terminating end")
	}
	// A well-formed body consumes its size exactly; trailing bytes mean
	// the enclosing size was wrong (Decode already sliced it, so rest>0
	// here is unreachable in practice — kept as a hard invariant).
	if c.rest() != 0 {
		return fb, fmt.Errorf("jitc: %d trailing bytes in body", c.rest())
	}
	return fb, nil
}
