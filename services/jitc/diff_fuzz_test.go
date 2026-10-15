//go:build linux && amd64

package jitc

import (
	"encoding/binary"
	"testing"
)

// diff_fuzz_test.go — differential fuzzing (practice #4): random
// bit-patterns through div/rem (trap corners), f64.min (NaN/zero
// corners) and i32.trunc_f64_s (range corners), comparing native vs
// interp on every input. Soak target: 30 s per QA sweep.

var fuzzDiffImg *Image
var fuzzDiffMod *Module

func fuzzDiffSetup() (*Module, *Image) {
	if fuzzDiffMod != nil {
		return fuzzDiffMod, fuzzDiffImg
	}
	// divs(a,b) = a/b_s(i64); remu; minf; trunc.
	divb := []byte{0x20, 0x00, 0x20, 0x01, 0x7F, 0x0B}
	remb := []byte{0x20, 0x00, 0x20, 0x01, 0x82, 0x0B}
	minb := []byte{0x20, 0x00, 0x20, 0x01, 0xA4, 0x0B}
	trb := []byte{0x20, 0x00, 0xAA, 0x0B}
	raw := module(
		typesSec(
			ftype([]byte{0x7E, 0x7E}, []byte{0x7E}),
			ftype([]byte{0x7C, 0x7C}, []byte{0x7C}),
			ftype([]byte{0x7C}, []byte{0x7F}),
		),
		funcsSec(0, 0, 1, 2),
		exportSec("divs", 0, 0),
		codeSec(
			body(nil, divb...),
			body(nil, remb...),
			body(nil, minb...),
			body(nil, trb...),
		),
	)
	// NOTE: single export name slot is reused; call by index instead.
	m, err := Decode(raw)
	if err != nil {
		panic(err)
	}
	img, err := CompileModule(m)
	if err != nil {
		panic(err)
	}
	fuzzDiffMod, fuzzDiffImg = m, img
	return m, img
}

func FuzzDiffNativeVsInterp(f *testing.F) {
	m, img := fuzzDiffSetup()
	nimport := m.ImportFuncCount()
	seeds := [][2]uint64{
		{0, 0}, {1, 0}, {7, 3}, {0x8000000000000000, 0xFFFFFFFFFFFFFFFF},
		{0x7FF8000000000000, 0x3FF0000000000000}, // NaN, 1.0
		{0x8000000000000000, 0x0000000000000000}, // -0.0, +0.0
		{0x7FF0000000000000, 0xFFF0000000000000}, // infs
		{0x0000000000000001, 0x0000000000000000}, // subnormal
		{0x43EFFFFFFFFFFFFF, 0x0000000000000000},
		{0xFFFFFFFFFFFFFFFF, 0x0000000000000001},
	}
	for _, s := range seeds {
		var b [16]byte
		binary.LittleEndian.PutUint64(b[0:8], s[0])
		binary.LittleEndian.PutUint64(b[8:16], s[1])
		f.Add(b[:])
	}
	r, err := NewRunner(m, img, 1)
	if err != nil {
		f.Fatal(err)
	}
	defer r.Close()
	ip, err := NewInterp(m, 1)
	if err != nil {
		f.Fatal(err)
	}
	// (i64,i64)->i64 funcs are 0,1; (f64,f64)->f64 is 2; (f64)->i32 is 3.
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) < 16 {
			return
		}
		a := binary.LittleEndian.Uint64(b[0:8])
		bb := binary.LittleEndian.Uint64(b[8:16])
		// div_s / rem_u on (a, bb).
		for _, fi := range []uint32{uint32(nimport), uint32(nimport + 1)} {
			nres, err := r.CallFunc(m, fi, a, bb)
			if err != nil {
				t.Fatalf("native call: %v", err)
			}
			ires, itrap, err := ip.execFunc(fi, []uint64{a, bb})
			if err != nil {
				t.Fatalf("interp static: %v", err)
			}
			if nres.Trap != itrap {
				t.Fatalf("func %d args %x: trap %d vs %d", fi, []uint64{a, bb}, nres.Trap, itrap)
			}
			if itrap == 0 && (len(ires) != 1 || nres.Value != ires[0]) {
				t.Fatalf("func %d args %x: %x vs %x", fi, []uint64{a, bb}, nres.Value, ires[0])
			}
		}
		// f64.min on raw bits (NaN-aware).
		nres, _ := r.CallFunc(m, uint32(nimport+2), a, bb)
		ires, itrap, _ := ip.execFunc(uint32(nimport+2), []uint64{a, bb})
		if nres.Trap != itrap {
			t.Fatalf("min trap %d vs %d", nres.Trap, itrap)
		}
		if itrap == 0 && !nanAwareEqual(VTf64, nres.Value, ires[0]) {
			t.Fatalf("min(%x) = %x vs %x", []uint64{a, bb}, nres.Value, ires[0])
		}
		// trunc on a.
		nres, _ = r.CallFunc(m, uint32(nimport+3), a)
		ires, itrap, _ = ip.execFunc(uint32(nimport+3), []uint64{a})
		if nres.Trap != itrap {
			t.Fatalf("trunc trap %d vs %d", nres.Trap, itrap)
		}
		if itrap == 0 && uint32(nres.Value) != uint32(ires[0]) {
			t.Fatalf("trunc(%x) = %x vs %x", a, nres.Value, ires[0])
		}
	})
}
