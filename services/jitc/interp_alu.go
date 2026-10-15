package jitc

import (
	"fmt"
	"math"
	"math/bits"
)

// interp_alu.go — interpreter execution units. Each helper pops operands
// from *stack and pushes results, returning a Trap* code for runtime
// traps (div-by-zero, conversion failure, OOB is handled by callers)
// or a Go error for static problems. Float behavior mirrors the wasm
// spec via Go's IEEE-754 math package; conversions implement the exact
// NaN/range trap conditions the emitter checks in machine code.

func ipop(s *[]uint64, n int) ([]uint64, error) {
	if len(*s) < n {
		return nil, fmt.Errorf("jitc: interp stack underflow")
	}
	v := append([]uint64(nil), (*s)[len(*s)-n:]...)
	*s = (*s)[:len(*s)-n]
	return v, nil
}

func (ip *Interp) aluInt(stack *[]uint64, op byte) (uint64, error) {
	pop1 := func() (uint64, error) {
		v, err := ipop(stack, 1)
		if err != nil {
			return 0, err
		}
		return v[0], nil
	}
	pop2 := func() (uint64, uint64, error) {
		v, err := ipop(stack, 2)
		if err != nil {
			return 0, 0, err
		}
		return v[0], v[1], nil
	}
	push := func(v uint64) { *stack = append(*stack, v) }
	m32 := func(v uint64) uint64 { return v & 0xFFFFFFFF }

	switch op {
	case 0x45:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		if uint32(a) == 0 {
			push(1)
		} else {
			push(0)
		}
	case 0x50: // i64.eqz (unary! not part of the binary relops below)
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		if a == 0 {
			push(1)
		} else {
			push(0)
		}
	case 0x46, 0x47, 0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E, 0x4F:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		x, y := uint32(a), uint32(b)
		var r bool
		switch op {
		case 0x46:
			r = x == y
		case 0x47:
			r = x != y
		case 0x48:
			r = int32(x) < int32(y)
		case 0x49:
			r = x < y
		case 0x4A:
			r = int32(x) > int32(y)
		case 0x4B:
			r = x > y
		case 0x4C:
			r = int32(x) <= int32(y)
		case 0x4D:
			r = x <= y
		case 0x4E:
			r = int32(x) >= int32(y)
		case 0x4F:
			r = x >= y
		}
		if r {
			push(1)
		} else {
			push(0)
		}
	case 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5A:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		var r bool
		switch op {
		case 0x51:
			r = a == b
		case 0x52:
			r = a != b
		case 0x53:
			r = int64(a) < int64(b)
		case 0x54:
			r = a < b
		case 0x55:
			r = int64(a) > int64(b)
		case 0x56:
			r = a > b
		case 0x57:
			r = int64(a) <= int64(b)
		case 0x58:
			r = a <= b
		case 0x59:
			r = int64(a) >= int64(b)
		case 0x5A:
			r = a >= b
		}
		if r {
			push(1)
		} else {
			push(0)
		}
	case 0x67:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.LeadingZeros32(uint32(a))))
	case 0x68:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.TrailingZeros32(uint32(a))))
	case 0x69:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.OnesCount32(uint32(a))))
	case 0x79:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.LeadingZeros64(a)))
	case 0x7A:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.TrailingZeros64(a)))
	case 0x7B:
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(bits.OnesCount64(a)))
	case 0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F, 0x70,
		0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		x, y := uint32(a), uint32(b)
		switch op {
		case 0x6A:
			push(m32(uint64(x) + uint64(y)))
		case 0x6B:
			push(m32(uint64(x) - uint64(y)))
		case 0x6C:
			push(m32(uint64(x) * uint64(y)))
		case 0x6D:
			if y == 0 {
				return TrapDivZero, nil
			}
			if x == 0x80000000 && y == 0xFFFFFFFF {
				push(0x80000000)
			} else {
				push(m32(uint64(int32(x) / int32(y))))
			}
		case 0x6E:
			if y == 0 {
				return TrapDivZero, nil
			}
			push(m32(uint64(x) / uint64(y)))
		case 0x6F:
			if y == 0 {
				return TrapDivZero, nil
			}
			if x == 0x80000000 && y == 0xFFFFFFFF {
				push(0)
			} else {
				push(m32(uint64(int32(x) % int32(y))))
			}
		case 0x70:
			if y == 0 {
				return TrapDivZero, nil
			}
			push(m32(uint64(x) % uint64(y)))
		case 0x71:
			push(m32(a & b))
		case 0x72:
			push(m32(a | b))
		case 0x73:
			push(m32(a ^ b))
		case 0x74:
			push(m32(uint64(x) << (y & 31)))
		case 0x75:
			push(m32(uint64(int32(x)) >> (y & 31)))
		case 0x76:
			push(m32(uint64(x) >> (y & 31)))
		case 0x77:
			push(m32(uint64(bits.RotateLeft32(x, int(y&31)))))
		case 0x78:
			push(m32(uint64(bits.RotateLeft32(x, int(-(y&31))&31))))
		}
	case 0x7C, 0x7D, 0x7E, 0x7F, 0x80, 0x81, 0x82,
		0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8A:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		switch op {
		case 0x7C:
			push(a + b)
		case 0x7D:
			push(a - b)
		case 0x7E:
			push(a * b)
		case 0x7F:
			if b == 0 {
				return TrapDivZero, nil
			}
			if a == 0x8000000000000000 && b == 0xFFFFFFFFFFFFFFFF {
				push(0x8000000000000000)
			} else {
				push(uint64(int64(a) / int64(b)))
			}
		case 0x80:
			if b == 0 {
				return TrapDivZero, nil
			}
			push(a / b)
		case 0x81:
			if b == 0 {
				return TrapDivZero, nil
			}
			if a == 0x8000000000000000 && b == 0xFFFFFFFFFFFFFFFF {
				push(0)
			} else {
				push(uint64(int64(a) % int64(b)))
			}
		case 0x82:
			if b == 0 {
				return TrapDivZero, nil
			}
			push(a % b)
		case 0x83:
			push(a & b)
		case 0x84:
			push(a | b)
		case 0x85:
			push(a ^ b)
		case 0x86:
			push(a << (b & 63))
		case 0x87:
			push(uint64(int64(a) >> (b & 63)))
		case 0x88:
			push(a >> (b & 63))
		case 0x89:
			push(bits.RotateLeft64(a, int(b&63)))
		case 0x8A:
			push(bits.RotateLeft64(a, int(-(b&63))&63))
		}
	case 0xA7: // i32.wrap_i64
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(m32(a))
	case 0xC0: // i32.extend8_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(m32(uint64(int32(int8(a)))))
	case 0xC1: // i32.extend16_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(m32(uint64(int32(int16(a)))))
	case 0xC2: // i64.extend8_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(int64(int8(a))))
	case 0xC3: // i64.extend16_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(int64(int16(a))))
	case 0xC4: // i64.extend32_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(int64(int32(a))))
	default:
		return 0, fmt.Errorf("jitc: interp bad int op 0x%02x", op)
	}
	return 0, nil
}

// f32 helpers with exact wasm NaN/zero semantics.
func f32min(a, b float32) float32 {
	if math.IsNaN(float64(a)) || math.IsNaN(float64(b)) {
		return float32(math.NaN())
	}
	if a == 0 && b == 0 {
		if math.Signbit(float64(a)) || math.Signbit(float64(b)) {
			return float32(math.Copysign(0, -1))
		}
		return 0
	}
	if a < b {
		return a
	}
	return b
}

func f32max(a, b float32) float32 {
	if math.IsNaN(float64(a)) || math.IsNaN(float64(b)) {
		return float32(math.NaN())
	}
	if a == 0 && b == 0 {
		if math.Signbit(float64(a)) && math.Signbit(float64(b)) {
			return float32(math.Copysign(0, -1))
		}
		return 0
	}
	if a > b {
		return a
	}
	return b
}

func (ip *Interp) aluFloat(stack *[]uint64, op byte) (uint64, error) {
	pop1 := func() (uint64, error) {
		v, err := ipop(stack, 1)
		if err != nil {
			return 0, err
		}
		return v[0], nil
	}
	pop2 := func() (uint64, uint64, error) {
		v, err := ipop(stack, 2)
		if err != nil {
			return 0, 0, err
		}
		return v[0], v[1], nil
	}
	push := func(v uint64) { *stack = append(*stack, v) }
	pushF32 := func(f float32) { push(uint64(math.Float32bits(f))) }
	pushF64 := func(f float64) { push(math.Float64bits(f)) }
	wide := op >= 0x61 && op <= 0x66 || op >= 0x99 && op <= 0xA6
	_ = wide
	switch op {
	case 0x5B, 0x5C, 0x5D, 0x5E, 0x5F, 0x60:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		x, y := math.Float32frombits(uint32(a)), math.Float32frombits(uint32(b))
		var r bool
		switch op {
		case 0x5B:
			r = x == y
		case 0x5C:
			r = x != y
		case 0x5D:
			r = x < y
		case 0x5E:
			r = x > y
		case 0x5F:
			r = x <= y
		case 0x60:
			r = x >= y
		}
		if r {
			push(1)
		} else {
			push(0)
		}
	case 0x61, 0x62, 0x63, 0x64, 0x65, 0x66:
		a, b, err := pop2()
		if err != nil {
			return 0, err
		}
		x, y := math.Float64frombits(a), math.Float64frombits(b)
		var r bool
		switch op {
		case 0x61:
			r = x == y
		case 0x62:
			r = x != y
		case 0x63:
			r = x < y
		case 0x64:
			r = x > y
		case 0x65:
			r = x <= y
		case 0x66:
			r = x >= y
		}
		if r {
			push(1)
		} else {
			push(0)
		}
	case 0x8B, 0x8C, 0x8D, 0x8E, 0x8F, 0x90, 0x91,
		0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98:
		a, err := pop1()
		if err != nil && op >= 0x92 {
			return 0, err
		}
		if op >= 0x92 {
			// binary: need two operands
			*stack = append(*stack, a)
			a2, b, err := pop2()
			if err != nil {
				return 0, err
			}
			x, y := math.Float32frombits(uint32(a2)), math.Float32frombits(uint32(b))
			switch op {
			case 0x92:
				pushF32(x + y)
			case 0x93:
				pushF32(x - y)
			case 0x94:
				pushF32(x * y)
			case 0x95:
				pushF32(x / y)
			case 0x96:
				pushF32(f32min(x, y))
			case 0x97:
				pushF32(f32max(x, y))
			case 0x98:
				pushF32(float32(math.Copysign(float64(x), float64(y))))
			}
			return 0, nil
		}
		x := math.Float32frombits(uint32(a))
		switch op {
		case 0x8B:
			pushF32(float32(math.Abs(float64(x))))
		case 0x8C:
			pushF32(-x)
		case 0x8D:
			pushF32(float32(math.Ceil(float64(x))))
		case 0x8E:
			pushF32(float32(math.Floor(float64(x))))
		case 0x8F:
			pushF32(float32(math.Trunc(float64(x))))
		case 0x90:
			pushF32(float32(math.RoundToEven(float64(x))))
		case 0x91:
			pushF32(float32(math.Sqrt(float64(x))))
		}
	case 0x99, 0x9A, 0x9B, 0x9C, 0x9D, 0x9E, 0x9F,
		0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6:
		a, err := pop1()
		if err != nil && op >= 0xA0 {
			return 0, err
		}
		if op >= 0xA0 {
			*stack = append(*stack, a)
			a2, b, err := pop2()
			if err != nil {
				return 0, err
			}
			x, y := math.Float64frombits(a2), math.Float64frombits(b)
			switch op {
			case 0xA0:
				pushF64(x + y)
			case 0xA1:
				pushF64(x - y)
			case 0xA2:
				pushF64(x * y)
			case 0xA3:
				pushF64(x / y)
			case 0xA4:
				pushF64(f64min(x, y))
			case 0xA5:
				pushF64(f64max(x, y))
			case 0xA6:
				pushF64(math.Copysign(x, y))
			}
			return 0, nil
		}
		x := math.Float64frombits(a)
		switch op {
		case 0x99:
			pushF64(math.Abs(x))
		case 0x9A:
			pushF64(-x)
		case 0x9B:
			pushF64(math.Ceil(x))
		case 0x9C:
			pushF64(math.Floor(x))
		case 0x9D:
			pushF64(math.Trunc(x))
		case 0x9E:
			pushF64(math.RoundToEven(x))
		case 0x9F:
			pushF64(math.Sqrt(x))
		}
	default:
		return 0, fmt.Errorf("jitc: interp bad float op 0x%02x", op)
	}
	return 0, nil
}

func f64min(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Min(a, b) // math.Min implements the ±0 rule exactly
}

func f64max(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Max(a, b)
}

func (ip *Interp) aluConvert(stack *[]uint64, op byte) (uint64, error) {
	pop1 := func() (uint64, error) {
		v, err := ipop(stack, 1)
		if err != nil {
			return 0, err
		}
		return v[0], nil
	}
	push := func(v uint64) { *stack = append(*stack, v) }
	switch op {
	case 0xA8: // i32.trunc_f32_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float32frombits(uint32(a))
		if math.IsNaN(float64(f)) || f < -2147483648.0 || f >= 2147483648.0 {
			return TrapConv, nil
		}
		push(uint64(uint32(int32(f))))
	case 0xA9: // i32.trunc_f32_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float32frombits(uint32(a))
		if math.IsNaN(float64(f)) || f < 0 || f >= 4294967296.0 {
			return TrapConv, nil
		}
		push(uint64(uint32(f)))
	case 0xAA: // i32.trunc_f64_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float64frombits(a)
		if math.IsNaN(f) || f < -2147483648.0 || f >= 2147483648.0 {
			return TrapConv, nil
		}
		push(uint64(uint32(int32(f))))
	case 0xAB: // i32.trunc_f64_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float64frombits(a)
		if math.IsNaN(f) || f < 0 || f >= 4294967296.0 {
			return TrapConv, nil
		}
		push(uint64(uint32(f)))
	case 0xAC: // i64.trunc_f32_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float32frombits(uint32(a))
		if math.IsNaN(float64(f)) || f < -9223372036854775808.0 || f >= 9223372036854775808.0 {
			return TrapConv, nil
		}
		push(uint64(int64(f)))
	case 0xAD: // i64.trunc_f32_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float32frombits(uint32(a))
		if math.IsNaN(float64(f)) || f < 0 || f >= 18446744073709551616.0 {
			return TrapConv, nil
		}
		push(uint64(f))
	case 0xAE: // i64.trunc_f64_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float64frombits(a)
		if math.IsNaN(f) || f < -9223372036854775808.0 || f >= 9223372036854775808.0 {
			return TrapConv, nil
		}
		push(uint64(int64(f)))
	case 0xAF: // i64.trunc_f64_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		f := math.Float64frombits(a)
		if math.IsNaN(f) || f < 0 || f >= 18446744073709551616.0 {
			return TrapConv, nil
		}
		push(uint64(f))
	case 0xB0: // f32.convert_i32_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(math.Float32bits(float32(int32(a)))))
	case 0xB1: // f32.convert_i32_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(math.Float32bits(float32(uint32(a)))))
	case 0xB2: // f32.convert_i64_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(math.Float32bits(float32(int64(a)))))
	case 0xB3: // f32.convert_i64_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(math.Float32bits(float32(a))))
	case 0xB4: // f64.convert_i32_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(math.Float64bits(float64(int32(a))))
	case 0xB5: // f64.convert_i32_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(math.Float64bits(float64(uint32(a))))
	case 0xB6: // f64.convert_i64_s
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(math.Float64bits(float64(int64(a))))
	case 0xB7: // f64.convert_i64_u
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(math.Float64bits(float64(a)))
	case 0xB8: // f32.demote_f64
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(uint64(math.Float32bits(float32(math.Float64frombits(a)))))
	case 0xB9: // f64.promote_f32
		a, err := pop1()
		if err != nil {
			return 0, err
		}
		push(math.Float64bits(float64(math.Float32frombits(uint32(a)))))
	case 0xBA, 0xBB, 0xBC, 0xBD: // reinterpret: identity on bits
	default:
		return 0, fmt.Errorf("jitc: interp bad numeric op 0x%02x", op)
	}
	return 0, nil
}

func (ip *Interp) aluFC(stack *[]uint64, o Op) (uint64, error) {
	push := func(v uint64) { *stack = append(*stack, v) }
	switch o.Sub {
	case 0, 1, 2, 3, 4, 5, 6, 7: // trunc_sat
		a, err := ipop(stack, 1)
		if err != nil {
			return 0, err
		}
		i := o.Sub
		toWide, toUnsigned, fromWide := i >= 4, i%2 == 1, i&2 != 0
		var f float64
		if fromWide {
			f = math.Float64frombits(a[0])
		} else {
			f = float64(math.Float32frombits(uint32(a[0])))
		}
		// bounds as (lo, hi, loVal, hiVal); NaN → 0
		var lo, hi float64
		var loV, hiV uint64
		if !toWide && !toUnsigned {
			lo, hi, loV, hiV = -2147483648.0, 2147483648.0, 0x80000000, 0x7FFFFFFF
		} else if !toWide {
			lo, hi, loV, hiV = 0, 4294967296.0, 0, 0xFFFFFFFF
		} else if !toUnsigned {
			lo, hi, loV, hiV = -9223372036854775808.0, 9223372036854775808.0, 0x8000000000000000, 0x7FFFFFFFFFFFFFFF
		} else {
			lo, hi, loV, hiV = 0, 18446744073709551616.0, 0, 0xFFFFFFFFFFFFFFFF
		}
		if math.IsNaN(f) {
			push(0)
		} else if f < lo {
			push(loV)
		} else if f >= hi {
			push(hiV)
		} else if toWide && toUnsigned {
			push(uint64(f))
		} else if toWide {
			push(uint64(int64(f)))
		} else if toUnsigned {
			push(uint64(uint32(f)))
		} else {
			push(uint64(uint32(int32(f))))
		}
	case 10: // memory.copy
		if len(ip.mem) == 0 {
			return 0, ErrNoMemory
		}
		v, err := ipop(stack, 3)
		if err != nil {
			return 0, err
		}
		dst, src, n := uint32(v[0]), uint32(v[1]), uint32(v[2])
		if uint64(dst)+uint64(n) > uint64(len(ip.mem)) ||
			uint64(src)+uint64(n) > uint64(len(ip.mem)) {
			return TrapOOB, nil
		}
		copy(ip.mem[dst:dst+n], ip.mem[src:src+n]) // memmove semantics
	case 11: // memory.fill
		if len(ip.mem) == 0 {
			return 0, ErrNoMemory
		}
		v, err := ipop(stack, 3)
		if err != nil {
			return 0, err
		}
		dst, val, n := uint32(v[0]), byte(v[1]), uint32(v[2])
		if uint64(dst)+uint64(n) > uint64(len(ip.mem)) {
			return TrapOOB, nil
		}
		for i := uint32(0); i < n; i++ {
			ip.mem[dst+i] = val
		}
	default:
		return 0, fmt.Errorf("jitc: interp bad FC sub 0x%x", o.Sub)
	}
	return 0, nil
}
