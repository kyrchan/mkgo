//go:build linux && amd64

package jitc

import (
	"errors"
	"math"
	"os"
	"testing"
)

// nanAwareEqual compares result slots given their value types: NaN ==
// NaN for float results (payloads are nondeterministic per spec);
// everything else compares exactly (including ±0 distinction).
func nanAwareEqual(t byte, a, b uint64) bool {
	switch t {
	case VTi32:
		return uint32(a) == uint32(b)
	case VTi64:
		return a == b
	case VTf32:
		fa, fb := math.Float32frombits(uint32(a)), math.Float32frombits(uint32(b))
		if math.IsNaN(float64(fa)) && math.IsNaN(float64(fb)) {
			return true
		}
		return fa == fb && math.Signbit(float64(fa)) == math.Signbit(float64(fb))
	case VTf64:
		fa, fb := math.Float64frombits(a), math.Float64frombits(b)
		if math.IsNaN(fa) && math.IsNaN(fb) {
			return true
		}
		return fa == fb && math.Signbit(fa) == math.Signbit(fb)
	}
	return a == b
}

// diffExport runs export `name` with args on BOTH engines and asserts
// agreement. Static errors on either side SKIP (invalid-for-v1 module,
// not a divergence).
func diffExport(t *testing.T, m *Module, img *Image, name string, args ...uint64) {
	t.Helper()
	// Resolve expected result type.
	var rty byte
	var nargs int
	for _, e := range m.Exports {
		if e.Name == name && e.Kind == 0 {
			ft, err := funcTypeOf(m, e.Index)
			if err != nil {
				t.Fatalf("diff %s: %v", name, err)
			}
			nargs = len(ft.Params)
			if len(ft.Results) > 0 {
				rty = ft.Results[0]
			}
		}
	}
	_ = nargs
	// Native side.
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatalf("diff %s: runner: %v", name, err)
	}
	defer r.Close()
	nres, err := r.Call(m, name, args...)
	if err != nil {
		t.Fatalf("diff %s: native call: %v", name, err)
	}
	// Interp side (fresh state per run).
	ip, err := NewInterp(m, 1)
	if err != nil {
		t.Skipf("diff %s: interp setup: %v", name, err)
	}
	ires, itrap, err := ip.ExecExport(name, append([]uint64(nil), args...))
	if err != nil {
		t.Skipf("diff %s: interp static: %v", name, err)
	}
	if nres.Trap != itrap {
		t.Errorf("diff %s args %x: native trap %d vs interp trap %d",
			name, args, nres.Trap, itrap)
		return
	}
	if nres.Trap != 0 {
		return
	}
	if len(ires) != 1 {
		t.Errorf("diff %s: interp results %d, want 1", name, len(ires))
		return
	}
	if !nanAwareEqual(rty, nres.Value, ires[0]) {
		t.Errorf("diff %s args %x: native %x vs interp %x (type 0x%x)",
			name, args, nres.Value, ires[0], rty)
	}
}

func TestDiffHandbuilt(t *testing.T) {
	// fib + sum + br_table + div + globals + float min/max/convert,
	// each through both engines over several inputs.
	fibBody := []byte{
		0x20, 0x00, 0x41, 0x02, 0x48, 0x04, 0x7F, 0x20, 0x00, 0x05,
		0x20, 0x00, 0x41, 0x01, 0x6B, 0x10, 0x00,
		0x20, 0x00, 0x41, 0x02, 0x6B, 0x10, 0x00, 0x6A, 0x0B, 0x0B,
	}
	raw := module(
		typesSec(ftype([]byte{0x7F}, []byte{0x7F})),
		funcsSec(0),
		exportSec("fib", 0, 0),
		codeSec(body(nil, fibBody...)),
	)
	m, img := compile(t, raw)
	for _, n := range []uint64{0, 1, 2, 5, 10, 15} {
		diffExport(t, m, img, "fib", n)
	}
	// float min over a nasty corpus (zeros, NaNs, infs, subnormals).
	minops := func(a, b []byte) []byte {
		ops := append([]byte{0x44}, a...)
		ops = append(ops, 0x44)
		ops = append(ops, b...)
		return append(ops, 0xA4, 0x0B) // f64.min
	}
	nan := f64b(math.NaN())
	negZero := f64b(math.Copysign(0, -1))
	for _, tc := range [][2][]byte{
		{nan, f64b(1.0)}, {f64b(1.0), nan},
		{negZero, f64b(0)}, {f64b(0), negZero},
		{f64b(math.Inf(1)), f64b(2.0)}, {f64b(2.0), f64b(math.Inf(-1))},
		{f64b(5e-324), f64b(0)}, // subnormal
		{f64b(3.5), f64b(-1.25)},
	} {
		raw := module(
			typesSec(ftype(nil, []byte{0x7C})),
			funcsSec(0),
			exportSec("main", 0, 0),
			codeSec(body(nil, minops(tc[0], tc[1])...)),
		)
		m, img := compile(t, raw)
		diffExport(t, m, img, "main")
	}
	// conversions over bit-pattern corpus (incl. all-NaN payloads).
	convops := func(bits []byte, op byte) []byte {
		ops := append([]byte{0x44}, bits...)
		return append(ops, op, 0x0B)
	}
	for _, tc := range [][]byte{
		f64b(0), f64b(-0.0), f64b(1.5), f64b(-1.5),
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xF0, 0x7F}, // NaN (ickers)
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xEF, 0x7F}, // max f64
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00}, // 2^-1022-ish
	} {
		raw := module(
			typesSec(ftype(nil, []byte{0x7F})),
			funcsSec(0),
			exportSec("main", 0, 0),
			codeSec(body(nil, convops(tc, 0xAA)...)), // i32.trunc_f64_s
		)
		m, img := compile(t, raw)
		diffExport(t, m, img, "main")
	}
}

// hasCalls reports whether a function body calls anything (direct,
// indirect) or touches imports-state (globals of imported... no —
// globals are fine). Purity for differential = no calls at all.
func bodyHasCalls(fb FuncBody) bool {
	for _, o := range fb.Ops {
		if o.Code == 0x10 || o.Code == 0x11 {
			return true
		}
	}
	return false
}

// TestDiffRealCoverage compiles every pure leaf function in the shipped
// modules natively AND interprets it, over several input vectors, and
// requires agreement. It also reports the compilable fraction (the
// coverage metric driving Stage 3 priorities).
// bodyHasLoop reports whether the function body contains a branch whose
// target is at or before the branch itself, i.e. a cycle. Such a
// function may not terminate for caller-invented arguments.
func bodyHasLoop(fb FuncBody) bool {
	for pc, o := range fb.Ops {
		switch o.Code {
		case 0x0c, 0x0d: // br, br_if
			if int(o.Label) <= pc {
				return true
			}
		case 0x0e: // br_table
			for _, target := range o.Table {
				if int(target) <= pc {
					return true
				}
			}
		}
	}
	return false
}

func TestDiffRealCoverage(t *testing.T) {
	mods := []string{"../fs/fs.wasm.raw", "../shell/shell.wasm.raw",
		"../login/login.wasm.raw", "../console/console.wasm.raw",
		"../net/net.wasm.raw"}
	vecs := [][]uint64{
		{},
		{0}, {1}, {2}, {0xFFFFFFFF}, {0x80000000},
		{0xFFFFFFFFFFFFFFFF}, {0x8000000000000000},
		{0x7FC0000000000000}, {0xFFF8000000000000}, // NaNs
		{0x8000000000000000}, // -0.0
		{42, 7}, {7, 0}, {0, 0}, {1, 1},
	}
	// Backstop for the oracle. Looping functions are excluded below, but
	// the budget keeps any future caller from wedging the package.
	const maxSteps = 1 << 22
	total, compiled, skipped, checked := 0, 0, 0, 0
	budgeted, loopSkipped := 0, 0
	for _, path := range mods {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Logf("skip %s: %v", path, err)
			continue
		}
		m, err := Decode(raw)
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		nimport := m.ImportFuncCount()
		npages := 1
		if len(m.Mems) > 0 {
			if m.Mems[0].Min > 256 || m.Mems[0].Min < 1 {
				continue // too big/small to host-test faithfully
			}
			npages = int(m.Mems[0].Min)
		}
		for bi, fb := range m.Code {
			total++
			fidx := uint32(nimport + bi)
			if bodyHasCalls(fb) {
				skipped++
				continue
			}
			ft, err := funcTypeOf(m, fidx)
			if err != nil || len(ft.Params) > 2 || len(ft.Results) > 1 {
				skipped++
				continue
			}
			a, err := CompileOne(m, fidx)
			if err != nil {
				skipped++
				continue
			}
			if len(a.Calls) != 0 {
				skipped++
				continue
			}
			compiled++
			var rty byte
			if len(ft.Results) == 1 {
				rty = ft.Results[0]
			}
			if bodyHasLoop(fb) {
				// A loop can fail to terminate for arguments a sweep
				// invents (a scheduler search, a spin until a flag is
				// set), and the native side is real machine code on the
				// host -- there is no way to bound that safely from a
				// test. A watchdog does not work either: the call cannot
				// be cancelled, so every timeout leaks a goroutine that
				// keeps spinning and allocating (observed: 3100 live
				// goroutines and the process thrashing). So exclude
				// looping functions from the DIFFERENTIAL claim, which is
				// about straight-line leaves; the count is reported so
				// the reduced scope is visible.
				loopSkipped++
				continue
			}
			for _, args := range vecs {
				if len(args) != len(ft.Params) {
					continue
				}
				use := append([]uint64(nil), args...)
				img := &Image{Funcs: map[uint32]int{fidx: 0}, Region: a.Code, Polls: a.Polls}
				r, err := NewRunner(m, img, npages)
				if err != nil {
					t.Fatalf("runner: %v", err)
				}
				nres, cerr := r.CallFunc(m, fidx, use...)
				r.Close()
				if cerr != nil {
					t.Fatalf("native call: %v", cerr)
				}
				ip, err := NewInterp(m, npages)
				if err != nil {
					t.Fatalf("interp setup: %v", err)
				}
				ip.MaxSteps = maxSteps
				ires, itrap, err := ip.execFunc(fidx, use)
				if errors.Is(err, ErrInterpBudget) {
					budgeted++
					continue
				}
				if err != nil {
					t.Fatalf("interp static on compilable func %d: %v", fidx, err)
				}
				checked++
				if nres.Trap != itrap {
					t.Errorf("%s func %d args %x: native trap %d vs interp trap %d",
						path, fidx, use, nres.Trap, itrap)
					continue
				}
				if nres.Trap != 0 || len(ft.Results) == 0 {
					continue
				}
				if len(ires) != 1 || !nanAwareEqual(rty, nres.Value, ires[0]) {
					t.Errorf("%s func %d args %x: native %x vs interp %x",
						path, fidx, use, nres.Value, ires[0])
				}
			}
		}
	}
	t.Logf("coverage: total=%d compiled-leaf=%d skipped=%d checked=%d "+
		"interp-budget-skips=%d loop-funcs-excluded=%d",
		total, compiled, skipped, checked, budgeted, loopSkipped)
	if compiled == 0 {
		t.Fatalf("no compilable leaf functions found — harness broken?")
	}
}
