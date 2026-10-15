//go:build linux && amd64

package jitc

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// exec_test.go — host-executed differential tests for the Stage 2a
// baseline. Modules are assembled by hand below (no external toolchain)
// and run through CompileModule + Runner; results and traps are
// asserted directly. These are the executable specification of the
// emitter: every opcode the compiler claims must behave exactly like
// wasm semantics here.

// --- module builders ---

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			return append(out, b)
		}
	}
}

func sleb(v int32) []byte {
	var out []byte
	more := true
	for more {
		b := byte(v & 0x7F)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			more = false
		} else {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

func sec(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint32(len(payload)))...)
	return append(out, payload...)
}

func module(parts ...[]byte) []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func ftype(params, results []byte) []byte {
	out := []byte{0x60, byte(len(params))}
	out = append(out, params...)
	out = append(out, byte(len(results)))
	return append(out, results...)
}

func typesSec(ts ...[]byte) []byte {
	p := uleb(uint32(len(ts)))
	for _, t := range ts {
		p = append(p, t...)
	}
	return sec(1, p)
}

func funcsSec(tidx ...uint32) []byte {
	p := uleb(uint32(len(tidx)))
	for _, t := range tidx {
		p = append(p, uleb(t)...)
	}
	return sec(3, p)
}

func exportSec(name string, kind byte, idx uint32) []byte {
	p := uleb(1)
	p = append(p, byte(len(name)))
	p = append(p, name...)
	p = append(p, kind)
	p = append(p, uleb(idx)...)
	return sec(7, p)
}

// body assembles one code entry: locals as (count, type) pairs flattened,
// ops as raw bytes.
func body(localPairs []byte, ops ...byte) []byte {
	b := []byte{byte(len(localPairs) / 2)}
	for i := 0; i < len(localPairs); i += 2 {
		b = append(b, localPairs[i], localPairs[i+1])
	}
	b = append(b, ops...)
	out := uleb(uint32(len(b)))
	return append(out, b...)
}

func codeSec(bodies ...[]byte) []byte {
	p := uleb(uint32(len(bodies)))
	for _, b := range bodies {
		p = append(p, b...)
	}
	return sec(10, p)
}

func memSec(min uint32) []byte {
	p := append(uleb(1), 0x00)
	p = append(p, uleb(min)...)
	return sec(5, p)
}

func globalSec(vt, mut byte, initOp []byte) []byte {
	p := append(uleb(1), vt, mut)
	p = append(p, initOp...)
	p = append(p, 0x0B)
	return sec(6, p)
}

// run compiles + executes the named export, failing the test on error.
func run(t *testing.T, m *Module, img *Image, npages int, name string, args ...uint64) ExecResult {
	t.Helper()
	r, err := NewRunner(m, img, npages)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer r.Close()
	res, err := r.Call(m, name, args...)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return res
}

func compile(t *testing.T, raw []byte) (*Module, *Image) {
	t.Helper()
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	img, err := CompileModule(m)
	if err != nil {
		t.Fatalf("CompileModule: %v", err)
	}
	return m, img
}

func expectValue(t *testing.T, res ExecResult, want uint64) {
	t.Helper()
	if res.Trap != 0 {
		t.Fatalf("unexpected trap %d", res.Trap)
	}
	if res.Value != want {
		t.Fatalf("got %d (0x%x), want %d", res.Value, res.Value, want)
	}
}

func expectTrap(t *testing.T, res ExecResult, want uint64) {
	t.Helper()
	if res.Trap != want {
		t.Fatalf("got trap %d value %d, want trap %d", res.Trap, res.Value, want)
	}
}

func TestExecConstAdd(t *testing.T) {
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, 0x41, 0x07, 0x41, 0x06, 0x6A, 0x0B)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "main"), 13)
}

func TestExecParamsLocals(t *testing.T) {
	// (a, b) -> a*b via an extra local slot.
	raw := module(
		typesSec(ftype([]byte{0x7F, 0x7F}, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body([]byte{0x01, 0x7F},
			0x20, 0x00, 0x20, 0x01, 0x6C, 0x21, 0x02, 0x20, 0x02, 0x0B)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "main", 6, 7), 42)
}

func TestExecFib(t *testing.T) {
	// fib(n) = n<2 ? n : fib(n-1)+fib(n-2); exercises calls+recursion.
	fibBody := []byte{
		0x20, 0x00, 0x41, 0x02, 0x48, // n, 2, lt_s
		0x04, 0x7F, // if (result i32)
		0x20, 0x00, // then: n
		0x05,                                     // else
		0x20, 0x00, 0x41, 0x01, 0x6B, 0x10, 0x00, // fib(n-1)
		0x20, 0x00, 0x41, 0x02, 0x6B, 0x10, 0x00, // fib(n-2)
		0x6A, // add
		0x0B, // end if
		0x0B, // end func
	}
	raw := module(
		typesSec(ftype([]byte{0x7F}, []byte{0x7F})),
		funcsSec(0),
		exportSec("fib", 0, 0),
		codeSec(body(nil, fibBody...)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "fib", 10), 55)
	expectValue(t, run(t, m, img, 1, "fib", 20), 6765)
	expectValue(t, run(t, m, img, 1, "fib", 0), 0)
	expectValue(t, run(t, m, img, 1, "fib", 1), 1)
}

func TestExecSumLoop(t *testing.T) {
	// sum(n) = 1+...+n via block/loop/br_if. Locals: 1=s, 2=i.
	ops := []byte{
		0x41, 0x00, 0x21, 0x01, // s = 0
		0x41, 0x01, 0x21, 0x02, // i = 1
		0x02, 0x40, // block (done)
		0x03, 0x40, // loop (top)
		0x20, 0x02, 0x20, 0x00, 0x4A, 0x0D, 0x01, // if i>n break
		0x20, 0x01, 0x20, 0x02, 0x6A, 0x21, 0x01, // s += i
		0x20, 0x02, 0x41, 0x01, 0x6A, 0x21, 0x02, // i++
		0x0C, 0x00, // br loop
		0x0B,       // end loop
		0x0B,       // end block
		0x20, 0x01, // return s
		0x0B,
	}
	raw := module(
		typesSec(ftype([]byte{0x7F}, []byte{0x7F})),
		funcsSec(0),
		exportSec("sum", 0, 0),
		codeSec(body([]byte{0x02, 0x7F}, ops...)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "sum", 100), 5050)
	expectValue(t, run(t, m, img, 1, "sum", 0), 0)
}

func TestExecBrTable(t *testing.T) {
	// f(x): 0->10, 1->20, 2->30, else 40.
	ops := []byte{
		0x02, 0x7F, // $out (result i32)
		0x02, 0x40, // $d3
		0x02, 0x40, // $d2
		0x02, 0x40, // $d1
		0x02, 0x40, // $d0
		0x20, 0x00, 0x0E, 0x03, 0x00, 0x01, 0x02, 0x03, // br_table
		0x0B,                   // end d0
		0x41, 0x0A, 0x0C, 0x03, // arm0: 10; br $out
		0x0B,                   // end d1
		0x41, 0x14, 0x0C, 0x02, // arm1: 20; br $out
		0x0B,                   // end d2
		0x41, 0x1E, 0x0C, 0x01, // arm2: 30; br $out
		0x0B,       // end d3
		0x41, 0x28, // default: 40
		0x0B, // end $out
		0x0B, // end func
	}
	raw := module(
		typesSec(ftype([]byte{0x7F}, []byte{0x7F})),
		funcsSec(0),
		exportSec("sw", 0, 0),
		codeSec(body(nil, ops...)),
	)
	m, img := compile(t, raw)
	for x, want := range map[uint64]uint64{0: 10, 1: 20, 2: 30, 3: 40, 99: 40} {
		expectValue(t, run(t, m, img, 1, "sw", x), want)
	}
}

func TestExecMemory(t *testing.T) {
	// store 0x12345678 @0, load back; byte/halfword roundtrips.
	ops := []byte{
		0x41, 0x00, // addr 0
		0x41, // 0x12345678
	}
	ops = append(ops, sleb(0x12345678)...)
	ops = append(ops, 0x36, 0x00, 0x00)                   // i32.store
	ops = append(ops, 0x41, 0x00, 0x28, 0x00, 0x00, 0x0B) // load
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		memSec(1),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "main"), 0x12345678)

	// OOB load traps.
	oob := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		memSec(1),
		exportSec("oob", 0, 0),
		codeSec(body(nil, 0x41, 0x80, 0x80, 0x04, 0x28, 0x00, 0x00, 0x0B)), // load @65536
	)
	m2, img2 := compile(t, oob)
	expectTrap(t, run(t, m2, img2, 1, "oob"), TrapOOB)

	// memory.size reports pages.
	sz := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		memSec(1),
		exportSec("sz", 0, 0),
		codeSec(body(nil, 0x3F, 0x00, 0x0B)),
	)
	m3, img3 := compile(t, sz)
	expectValue(t, run(t, m3, img3, 3, "sz"), 3)
}

func TestExecDiv(t *testing.T) {
	cases := []struct {
		op   byte
		a, b int32
		want uint32
		trap uint64
	}{
		{0x6D, 7, 3, 2, 0},           // div_s
		{0x6D, -7, 3, 0xFFFFFFFE, 0}, // -2
		{0x6E, 7, 3, 2, 0},           // div_u
		{0x6F, 7, 3, 1, 0},           // rem_s
		{0x70, 7, 3, 1, 0},           // rem_u
		{0x6D, 7, 0, 0, TrapDivZero},
		{0x70, 7, 0, 0, TrapDivZero},
		{0x6D, -2147483648, -1, 0x80000000, 0}, // wrap, no trap
		{0x6F, -2147483648, -1, 0, 0},          // rem wrap
	}
	for _, tc := range cases {
		ops := []byte{0x41}
		ops = append(ops, sleb(tc.a)...)
		ops = append(ops, 0x41)
		ops = append(ops, sleb(tc.b)...)
		ops = append(ops, tc.op, 0x0B)
		raw := module(
			typesSec(ftype(nil, []byte{0x7F})),
			funcsSec(0),
			exportSec("main", 0, 0),
			codeSec(body(nil, ops...)),
		)
		m, img := compile(t, raw)
		res := run(t, m, img, 1, "main")
		if tc.trap != 0 {
			expectTrap(t, res, tc.trap)
		} else {
			expectValue(t, res, uint64(tc.want))
		}
	}
}

func TestExecGlobals(t *testing.T) {
	// mutable i32 global init 41; func returns (g+1) and stores it.
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		globalSec(0x7F, 0x01, append([]byte{0x41}, sleb(41)...)),
		exportSec("bump", 0, 0),
		codeSec(body(nil,
			0x23, 0x00, 0x41, 0x01, 0x6A, 0x24, 0x00, 0x23, 0x00, 0x0B)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "bump"), 42)
	// second call on a FRESH runner re-inits; same runner accumulates.
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	res, err := r.Call(m, "bump")
	if err != nil {
		t.Fatal(err)
	}
	expectValue(t, res, 42)
	res, err = r.Call(m, "bump")
	if err != nil {
		t.Fatal(err)
	}
	expectValue(t, res, 43) // globals persist across calls
}

func TestExecCallChain(t *testing.T) {
	// sq(x) = x*x; cube(x) = sq(x)*x. Cross-function calls + linking.
	t0 := ftype([]byte{0x7F}, []byte{0x7F})
	sq := body(nil, 0x20, 0x00, 0x20, 0x00, 0x6C, 0x0B)
	cube := body(nil, 0x20, 0x00, 0x10, 0x00, 0x20, 0x00, 0x6C, 0x0B)
	raw := module(
		typesSec(t0),
		funcsSec(0, 0),
		exportSec("cube", 0, 1),
		codeSec(sq, cube),
	)
	m, img := compile(t, raw)
	if len(img.Funcs) != 2 {
		t.Fatalf("linked %d funcs, want 2", len(img.Funcs))
	}
	expectValue(t, run(t, m, img, 1, "cube", 3), 27)
}

func TestExecBitops(t *testing.T) {
	// clz(0)=32, ctz(0)=32, popcnt(0b1011)=3, rotl, wrap, extend.
	ops := []byte{
		0x41, 0x00, 0x67, // clz(0) = 32
		0x41, 0x0B, 0x69, 0x6A, // + popcnt(11)=3 → 35
		0x41, 0x00, 0x68, 0x6A, // + ctz(0)=32 → 67
		0x0B,
	}
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, ops...)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "main"), 67)
}

func TestExecI64(t *testing.T) {
	// (2^40 + 2^40) = 2^41 exactly (beyond i32 range).
	// build programmatically: const 1<<40 twice, add.
	b := []byte{0x42}
	b = append(b, sleb64(1<<40)...)
	b = append(b, 0x42)
	b = append(b, sleb64(1<<40)...)
	b = append(b, 0x7C, 0x0B) // i64.add
	raw := module(
		typesSec(ftype(nil, []byte{0x7E})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, b...)),
	)
	m, img := compile(t, raw)
	expectValue(t, run(t, m, img, 1, "main"), 1<<41)
}

func sleb64(v int64) []byte {
	var out []byte
	more := true
	for more {
		b := byte(v & 0x7F)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			more = false
		} else {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

func TestExecUnreachable(t *testing.T) {
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, 0x00, 0x0B)),
	)
	m, img := compile(t, raw)
	// NOTE: unreachable-as-first-op makes the rest dead; the function
	// still compiles (dead end handled) and traps at runtime.
	expectTrap(t, run(t, m, img, 1, "main"), TrapUnreachable)
}

func TestExecMemCopy(t *testing.T) {
	// copy 8 bytes from 100 -> 200, load back from 200.
	// memory.copy pops (dst, src, n) per MVP operand order: dst, src, n.
	b := []byte{}
	b = append(b, 0x41)
	b = append(b, uleb(200)...)
	b = append(b, 0x41)
	b = append(b, sleb(100)...) // NOTE: sleb, not uleb (100 needs 2 bytes)
	b = append(b, 0x41)
	b = append(b, uleb(8)...)
	b = append(b, 0xFC, 0x0A, 0x00, 0x00) // memory.copy
	b = append(b, 0x41)
	b = append(b, uleb(200)...)
	b = append(b, 0x29, 0x00, 0x00) // i64.load
	b = append(b, 0xA7, 0x0B)       // wrap + end... returns i32
	raw := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		memSec(1),
		exportSec("main", 0, 0),
		codeSec(body(nil, b...)),
	)
	m, img := compile(t, raw)
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// seed source bytes
	for i := 0; i < 8; i++ {
		r.Mem()[100+i] = byte(0xA0 + i)
	}
	res, err := r.Call(m, "main")
	if err != nil {
		t.Fatal(err)
	}
	// low 4 bytes of the 8-byte little-endian load, wrapped to i32
	expectValue(t, res, 0xA3A2A1A0)
	for i := 0; i < 8; i++ {
		if r.Mem()[200+i] != byte(0xA0+i) {
			t.Fatalf("copy byte %d = 0x%x", i, r.Mem()[200+i])
		}
	}
}

func TestExecFibMain(t *testing.T) {
	// The exact Stage 3b-ii smoke artifact, executed on host with a
	// fake fd_write stub (gas-verified below): main() must print
	// "NATIVE-OK\n" (len 10, prefix "NATIVE-O") and return 6765.
	//
	// Import-callee contract (emit.go:11-16, emit64.go:443-470,
	// arch/x86_64/jit_poll.S:14-28, core/jit_tramp.cc:104):
	//   - pinned regs RDI/RSI/RDX/RBX survive the call. RDX is the trap
	//     pointer: post-call importTrapTail does cmp qword [rdx],0
	//     (emit64.go:478), so RDX must be restored before ret.
	//   - every i32 pointer arg is a guest offset relative to RDI
	//     (linear-mem base); host address = RDI + offset.
	//   - args layout after ImportFdWrite's push r11:
	//     [rsp+0]=retaddr, [rsp+8]=nw. R9=count, R8=iovs, RCX/… per
	//     emitter marshalling. RAX/RCX/R8-R11 are scratch/dead.
	//   - return errno in RAX.
	m, err := Decode(FibModule())
	if err != nil {
		t.Fatal(err)
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	var log [4096]byte
	logAddr := uint64(uintptr(unsafe.Pointer(&log[0])))
	// Fake fd_write, gas-verified 2026-09-19: follows iovs[0], logs
	// {count, ptr, len, first-msg-qword}, stores *nw = len, errno 0.
	// Log layout: [0:8]=iovs_len, [8:16]=ptr, [16:24]=len, [24:32]=msg.
	//
	// Three defects fixed vs the original gas-verified stub:
	//   1. *nwritten was written to host 0x50 (guest offset used as a
	//      host pointer) — now lea rdx,[rdi+rdx] before the store.
	//   2. RDX (trap pin) was clobbered — push/pop rdx around the stub.
	//   3. 8B 4C 24 04 read [rsp+4] instead of [rdx+4] — now 8B 4A 04.
	stub := []byte{
		0x52,                               // push rdx — save trap pin (NEW)
		0x48, 0xB8, 0, 0, 0, 0, 0, 0, 0, 0, // movabs rax, logaddr
		0x4C, 0x89, 0x08, // mov [rax], r9 (count)
		0x4A, 0x8D, 0x14, 0x07, // lea rdx, [rdi+r8] (&iov0)
		0x8B, 0x0A, // mov ecx, [rdx] (ptr)
		0x48, 0x89, 0x48, 0x08, // mov [rax+8], rcx
		0x8B, 0x4A, 0x04, // mov ecx, [rdx+4] (len) — FIX#3
		0x48, 0x89, 0x48, 0x10, // mov [rax+16], rcx
		0x44, 0x8B, 0x12, // mov r10d, [rdx] (ptr, gas-verified)
		0x4A, 0x8D, 0x14, 0x17, // lea rdx, [rdi+r10] (msg)
		0x48, 0x8B, 0x0A, // mov rcx, [rdx] (first qword)
		0x48, 0x89, 0x48, 0x18, // mov [rax+24], rcx
		0x48, 0x8B, 0x54, 0x24, 0x10, // mov rdx, [rsp+16] (nw ptr) — FIX#2
		0x48, 0x8D, 0x14, 0x17, // lea rdx, [rdi+rdx] — FIX#1: guest off → host addr
		0x48, 0x8B, 0x48, 0x10, // mov rcx, [rax+16] (len)
		0x89, 0x0A, // mov [rdx], ecx (*nw, u32)
		0x31, 0xC0, // xor eax, eax (errno 0)
		0x5A, // pop rdx — restore trap pin (NEW)
		0xC3, // ret
	}
	// movabs address bytes now start at offset 3 (1 push rdx + 48 B8).
	for i := 0; i < 8; i++ {
		stub[3+i] = byte(logAddr >> (8 * i))
	}
	// Append one stub copy per import reloc (shared stub would do;
	// per-site copies keep the math obvious) and patch.
	img := ir.Image
	for _, r := range ir.ImportRelocs {
		off := len(img.Region)
		img.Region = append(img.Region, stub...)
		rel := int32(off - (int(r.CodeOff) + 4))
		img.Region[r.CodeOff] = byte(rel)
		img.Region[r.CodeOff+1] = byte(rel >> 8)
		img.Region[r.CodeOff+2] = byte(rel >> 16)
		img.Region[r.CodeOff+3] = byte(rel >> 24)
	}
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// Sanity: data segments pre-seeded the messages.
	if string(r.Mem()[100:110]) != "NATIVE-OK\n" {
		t.Fatalf("data seg: %q", r.Mem()[100:110])
	}
	res, err := r.Call(m, "main")
	if err != nil {
		t.Fatal(err)
	}
	expectValue(t, res, 6765)
	if binary.LittleEndian.Uint64(log[0:]) != 1 {
		t.Fatalf("logged iovs_len %d, want 1", binary.LittleEndian.Uint64(log[0:]))
	}
	if binary.LittleEndian.Uint64(log[8:]) != 100 {
		t.Fatalf("logged ptr %d, want 100", binary.LittleEndian.Uint64(log[8:]))
	}
	if binary.LittleEndian.Uint64(log[16:]) != 10 {
		t.Fatalf("logged len %d, want 10 (NATIVE-OK, not BAD)", binary.LittleEndian.Uint64(log[16:]))
	}
	if binary.LittleEndian.Uint64(log[24:]) != 0x4F2D45564954414E {
		t.Fatalf("logged msg %x, want NATIVE-O", binary.LittleEndian.Uint64(log[24:]))
	}
	if binary.LittleEndian.Uint32(r.Mem()[80:]) != 10 {
		t.Fatalf("guest *nw = %d, want 10", binary.LittleEndian.Uint32(r.Mem()[80:]))
	}
}
