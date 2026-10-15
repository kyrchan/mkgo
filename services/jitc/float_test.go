//go:build linux && amd64

package jitc

import (
	"math"
	"testing"
)

// float_test.go — host-executed tests for the Stage 2b float baseline.
// Expected values come from Go's math package (hardware float on the
// host); NaN results assert IsNaN (payloads are nondeterministic per
// the wasm spec, except our min/max which promise canonical NaN).

func f32b(f float32) []byte {
	v := math.Float32bits(f)
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func f64b(f float64) []byte {
	v := math.Float64bits(f)
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24),
		byte(v >> 32), byte(v >> 40), byte(v >> 48), byte(v >> 56)}
}

func f32mod(name string, ops ...byte) []byte {
	return module(
		typesSec(ftype(nil, []byte{0x7D})),
		funcsSec(0),
		exportSec(name, 0, 0),
		codeSec(body(nil, ops...)),
	)
}

func f64mod(name string, ops ...byte) []byte {
	return module(
		typesSec(ftype(nil, []byte{0x7C})),
		funcsSec(0),
		exportSec(name, 0, 0),
		codeSec(body(nil, ops...)),
	)
}

func runBits(t *testing.T, raw []byte, name string) (uint64, uint64) {
	t.Helper()
	m, img := compile(t, raw)
	res := run(t, m, img, 1, name)
	return res.Value, res.Trap
}

func expectF32(t *testing.T, raw []byte, want float32) {
	t.Helper()
	v, trap := runBits(t, raw, "main")
	if trap != 0 {
		t.Fatalf("unexpected trap %d", trap)
	}
	got := math.Float32frombits(uint32(v))
	if math.IsNaN(float64(want)) {
		if !math.IsNaN(float64(got)) {
			t.Fatalf("got %v (%x), want NaN", got, v)
		}
		return
	}
	if got != want || math.Signbit(float64(got)) != math.Signbit(float64(want)) {
		t.Fatalf("got %v (%x), want %v", got, v, want)
	}
}

func expectF64(t *testing.T, raw []byte, want float64) {
	t.Helper()
	v, trap := runBits(t, raw, "main")
	if trap != 0 {
		t.Fatalf("unexpected trap %d", trap)
	}
	got := math.Float64frombits(v)
	if math.IsNaN(want) {
		if !math.IsNaN(got) {
			t.Fatalf("got %v (%x), want NaN", got, v)
		}
		return
	}
	if got != want || math.Signbit(got) != math.Signbit(want) {
		t.Fatalf("got %v (%x), want %v", got, v, want)
	}
}

func TestFloatArith32(t *testing.T) {
	c := func(a, b float32, op byte, want float32) {
		ops := append([]byte{0x43}, f32b(a)...)
		ops = append(ops, 0x43)
		ops = append(ops, f32b(b)...)
		ops = append(ops, op, 0x0B)
		expectF32(t, f32mod("main", ops...), want)
	}
	c(1.5, 2.25, 0x92, 3.75)
	c(1.5, 2.25, 0x93, -0.75)
	c(1.5, 2.0, 0x94, 3.0)
	c(7.0, 2.0, 0x95, 3.5)
	c(float32(math.Inf(1)), 1.0, 0x92, float32(math.Inf(1)))
	c(float32(math.NaN()), 1.0, 0x92, float32(math.NaN()))
}

func TestFloatArith64(t *testing.T) {
	c := func(a, b float64, op byte, want float64) {
		ops := append([]byte{0x44}, f64b(a)...)
		ops = append(ops, 0x44)
		ops = append(ops, f64b(b)...)
		ops = append(ops, op, 0x0B)
		expectF64(t, f64mod("main", ops...), want)
	}
	c(1.5, 2.25, 0xA0, 3.75)
	c(1.0, 0.0, 0xA3, math.Inf(1))   // div, incl. sign
	c(-1.0, 0.0, 0xA3, math.Inf(-1)) // -1/0 = -inf
	c(0.0, 0.0, 0xA3, math.NaN())    // 0/0 = NaN (no trap!)
}

func TestFloatSqrt(t *testing.T) {
	ops := append([]byte{0x44}, f64b(2.0)...)
	ops = append(ops, 0x9F, 0x0B) // f64.sqrt (0x9F; 0xA1 is f64.sub)
	expectF64(t, f64mod("main", ops...), math.Sqrt(2.0))
	ops = append([]byte{0x43}, f32b(-1.0)...)
	ops = append(ops, 0x91, 0x0B) // f32.sqrt(-1) = NaN
	expectF32(t, f32mod("main", ops...), float32(math.NaN()))
}

func TestFloatRelopNaN(t *testing.T) {
	nan32 := float32(math.NaN())
	c32 := func(a, b float32, op byte, want uint64) {
		ops := append([]byte{0x43}, f32b(a)...)
		ops = append(ops, 0x43)
		ops = append(ops, f32b(b)...)
		raw := module(
			typesSec(ftype(nil, []byte{0x7F})),
			funcsSec(0),
			exportSec("main", 0, 0),
			codeSec(body(nil, append(ops, op, 0x0B)...)),
		)
		v, trap := runBits(t, raw, "main")
		if trap != 0 || v != want {
			t.Errorf("f32 relop %x(%v,%v) = %d trap %d, want %d", op, a, b, v, trap, want)
		}
	}
	c32(nan32, 1.0, 0x5B, 0) // eq = false
	c32(nan32, 1.0, 0x5C, 1) // ne = true
	c32(nan32, 1.0, 0x5D, 0) // lt = false
	c32(1.0, 2.0, 0x5D, 1)
	c32(2.0, 1.0, 0x5E, 1)                                  // gt
	c32(1.0, 1.0, 0x5F, 1)                                  // le
	c32(float32(math.Copysign(0, -1)), float32(0), 0x5B, 1) // -0 == +0
}

func TestFloatMinMax(t *testing.T) {
	negZero := float32(math.Copysign(0, -1))
	// min(-0, +0) = -0 (sign matters!)
	ops := append([]byte{0x43}, f32b(negZero)...)
	ops = append(ops, 0x43)
	ops = append(ops, f32b(0)...)
	ops = append(ops, 0x96, 0x0B) // f32.min
	v, trap := runBits(t, f32mod("main", ops...), "main")
	if trap != 0 || v != 0x80000000 {
		t.Errorf("min(-0,+0) = %x trap %d, want 80000000", v, trap)
	}
	// max(-0, +0) = +0
	ops = append([]byte{0x43}, f32b(negZero)...)
	ops = append(ops, 0x43)
	ops = append(ops, f32b(0)...)
	ops = append(ops, 0x97, 0x0B) // f32.max
	v, trap = runBits(t, f32mod("main", ops...), "main")
	if trap != 0 || v != 0 {
		t.Errorf("max(-0,+0) = %x trap %d, want 0", v, trap)
	}
	// min(NaN, 1) = canonical NaN
	ops = append([]byte{0x43}, f32b(float32(math.NaN()))...)
	ops = append(ops, 0x43)
	ops = append(ops, f32b(1)...)
	ops = append(ops, 0x96, 0x0B)
	v, trap = runBits(t, f32mod("main", ops...), "main")
	if trap != 0 || v != f32CanonNaN {
		t.Errorf("min(NaN,1) = %x trap %d, want %x", v, trap, uint64(f32CanonNaN))
	}
	// max(1, NaN) = canonical NaN
	ops = append([]byte{0x43}, f32b(1)...)
	ops = append(ops, 0x43)
	ops = append(ops, f32b(float32(math.NaN()))...)
	ops = append(ops, 0x97, 0x0B)
	v, trap = runBits(t, f32mod("main", ops...), "main")
	if trap != 0 || v != f32CanonNaN {
		t.Errorf("max(1,NaN) = %x trap %d, want %x", v, trap, uint64(f32CanonNaN))
	}
}

func TestFloatConvert(t *testing.T) {
	// i32.trunc_f64_s(123456.789) = 123456
	ops := append([]byte{0x44}, f64b(123456.789)...)
	ops = append(ops, 0xAA, 0x0B)
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	v, trap := runBits(t, raw, "main")
	if trap != 0 || int32(v) != 123456 {
		t.Errorf("trunc = %d trap %d, want 123456", int32(v), trap)
	}
	// NaN traps with TrapConv.
	ops = append([]byte{0x44}, f64b(math.NaN())...)
	ops = append(ops, 0xAA, 0x0B)
	raw = module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	_, trap = runBits(t, raw, "main")
	if trap != TrapConv {
		t.Errorf("NaN trunc trap = %d, want %d", trap, TrapConv)
	}
	// 1e19 overflows i32 → TrapConv.
	ops = append([]byte{0x44}, f64b(1e19)...)
	ops = append(ops, 0xAA, 0x0B)
	raw = module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	_, trap = runBits(t, raw, "main")
	if trap != TrapConv {
		t.Errorf("overflow trunc trap = %d, want %d", trap, TrapConv)
	}
	// i64.trunc_f32_u(2^40) exact.
	ops = append([]byte{0x43}, f32b(1099511627776.0)...)
	ops = append(ops, 0xAD, 0x0B) // i64.trunc_f32_u
	raw = module(
		typesSec(ftype(nil, []byte{0x7E})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	v, trap = runBits(t, raw, "main")
	if trap != 0 || v != 1099511627776 {
		t.Errorf("u64 trunc = %d trap %d", v, trap)
	}
	// u64→f64 of max u64 (x87 path): correctly rounded 2^64.
	ops = []byte{0x42}
	ops = append(ops, sleb64(-1)...) // -1 as i64 bits = all-ones u64
	ops = append(ops, 0xB7, 0x0B)    // f64.convert_i64_u
	raw = module(
		typesSec(ftype(nil, []byte{0x7C})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	m, img := compile(t, raw)
	res := run(t, m, img, 1, "main")
	if res.Trap != 0 || res.Value != 0x43F0000000000000 {
		t.Errorf("u64max→f64 = %x trap %d, want 43F0...", res.Value, res.Trap)
	}
	// i32→f64 of -5.
	ops = []byte{0x41}
	ops = append(ops, sleb(-5)...)
	ops = append(ops, 0xB4, 0x0B) // f64.convert_i32_s
	raw = module(
		typesSec(ftype(nil, []byte{0x7C})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	v, trap = runBits(t, raw, "main")
	if trap != 0 || v != math.Float64bits(-5.0) {
		t.Errorf("i32→f64 = %x trap %d", v, trap)
	}
}

func TestFloatSat(t *testing.T) {
	// i32.trunc_sat_f32_s(NaN) = 0; (1e20) = MAX; (-1e20) = MIN.
	for _, tc := range []struct {
		in   float32
		want uint32
	}{
		{float32(math.NaN()), 0},
		{1e20, 0x7FFFFFFF},
		{-1e20, 0x80000000},
		{3.75, 3},
		{-3.75, 0xFFFFFFFD}, // -3
	} {
		ops := append([]byte{0x43}, f32b(tc.in)...)
		ops = append(ops, 0xFC, 0x00, 0x0B) // sub 0
		raw := module(
			typesSec(ftype(nil, []byte{0x7F})),
			funcsSec(0),
			exportSec("main", 0, 0),
			codeSec(body(nil, ops...)),
		)
		v, trap := runBits(t, raw, "main")
		if trap != 0 || uint32(v) != tc.want {
			t.Errorf("sat(%v) = %x trap %d, want %x", tc.in, v, trap, tc.want)
		}
	}
}

func TestFloatMisc(t *testing.T) {
	// abs/neg bit-exact incl. -0.
	ops := append([]byte{0x43}, f32b(float32(math.Copysign(0, -1)))...)
	ops = append(ops, 0x8B, 0x0B) // f32.abs(-0) = +0
	expectF32(t, f32mod("main", ops...), 0)
	ops = append([]byte{0x43}, f32b(0)...)
	ops = append(ops, 0x8C, 0x0B) // f32.neg(+0) = -0
	v, _ := runBits(t, f32mod("main", ops...), "main")
	if v != 0x80000000 {
		t.Errorf("neg(+0) = %x, want 80000000", v)
	}
	// copysign(3.0, -0.0) = -3.0.
	ops = append([]byte{0x43}, f32b(3.0)...)
	ops = append(ops, 0x43)
	ops = append(ops, f32b(float32(math.Copysign(0, -1)))...)
	ops = append(ops, 0x98, 0x0B) // f32.copysign
	expectF32(t, f32mod("main", ops...), -3.0)
	// floor(2.75) = 2, ceil(-2.25) = -2, trunc(-2.75) = -2.
	ops = append([]byte{0x44}, f64b(2.75)...)
	ops = append(ops, 0x9C, 0x0B) // f64.floor
	expectF64(t, f64mod("main", ops...), 2.0)
	ops = append([]byte{0x44}, f64b(-2.25)...)
	ops = append(ops, 0x9B, 0x0B) // f64.ceil
	expectF64(t, f64mod("main", ops...), -2.0)
	// reinterpret roundtrip: i32.reinterpret_f32(1.0 bits) = 0x3F800000.
	ops = append([]byte{0x43}, f32b(1.0)...)
	ops = append(ops, 0xBA, 0x0B) // i32.reinterpret_f32
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	v, trap := runBits(t, raw, "main")
	if trap != 0 || v != 0x3F800000 {
		t.Errorf("reinterpret = %x trap %d", v, trap)
	}
}
