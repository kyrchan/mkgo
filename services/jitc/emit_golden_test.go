package jitc

import (
	"bytes"
	"testing"
)

// emit_golden_test.go — exact-byte assertions for every emitter helper.
// Hand-encoded machine code fails SILENTLY (wrong REX bit still runs),
// so each template is pinned here. No build tag: pure byte assertions
// run on any platform. If you touch emit.go/emit64.go, this test tells
// you which encoding moved.

func asmBytes(f func(a *Asm)) []byte {
	a := &Asm{}
	f(a)
	if len(a.fix) != 0 {
		panic("golden helper left unbound labels")
	}
	return a.Code
}

func expectBytes(t *testing.T, name string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s:\n got %x\nwant %x", name, got, want)
	}
}

func TestGoldenStackFrame(t *testing.T) {
	expectBytes(t, "push rax", asmBytes(func(a *Asm) { a.pushReg(RRAX) }), []byte{0x50})
	expectBytes(t, "push r11", asmBytes(func(a *Asm) { a.pushReg(RR11) }), []byte{0x41, 0x53})
	expectBytes(t, "pop r8", asmBytes(func(a *Asm) { a.popReg(RR8) }), []byte{0x41, 0x58})
	expectBytes(t, "addRsp 0", asmBytes(func(a *Asm) { a.addRsp(0) }), []byte{})
	expectBytes(t, "addRsp 8", asmBytes(func(a *Asm) { a.addRsp(8) }), []byte{0x48, 0x83, 0xC4, 0x08})
	expectBytes(t, "addRsp 1000", asmBytes(func(a *Asm) { a.addRsp(1000) }),
		[]byte{0x48, 0x81, 0xC4, 0xE8, 0x03, 0x00, 0x00})
	expectBytes(t, "prologue 0", asmBytes(func(a *Asm) { a.Prologue(0) }),
		[]byte{0x55, 0x48, 0x89, 0xE5})
	expectBytes(t, "prologue 1", asmBytes(func(a *Asm) { a.Prologue(1) }),
		[]byte{0x55, 0x48, 0x89, 0xE5, 0x48, 0x81, 0xEC, 0x08, 0x00, 0x00, 0x00})
	expectBytes(t, "copyparams 1xi32", asmBytes(func(a *Asm) { a.CopyParams([]byte{0x7F}) }),
		[]byte{0x41, 0x8B, 0x82, 0x00, 0x00, 0x00, 0x00,
			0x48, 0x89, 0x85, 0xF8, 0xFF, 0xFF, 0xFF})
	expectBytes(t, "copyparams 1xi64", asmBytes(func(a *Asm) { a.CopyParams([]byte{0x7E}) }),
		[]byte{0x49, 0x8B, 0x82, 0x00, 0x00, 0x00, 0x00,
			0x48, 0x89, 0x85, 0xF8, 0xFF, 0xFF, 0xFF})
	expectBytes(t, "epilogue", asmBytes(func(a *Asm) { a.Epilogue() }),
		[]byte{0x48, 0x89, 0xEC, 0x5D, 0xC3})
	expectBytes(t, "trap", asmBytes(func(a *Asm) { a.Trap(2) }),
		[]byte{0xC7, 0x02, 0x02, 0x00, 0x00, 0x00, 0x48, 0x89, 0xEC, 0x5D, 0x31, 0xC0, 0xC3})
	expectBytes(t, "result1", asmBytes(func(a *Asm) { a.Result1() }), []byte{0x58})
}

func TestGoldenJumps(t *testing.T) {
	// jmp at 1, target bound at 7: rel = 7-(1+1+4) = 1.
	got := asmBytes(func(a *Asm) {
		a.emit1(0x90)
		l := a.NewLabel()
		a.Jmp(l)
		a.emit1(0x90)
		if err := a.Bind(l); err != nil {
			panic(err)
		}
	})
	expectBytes(t, "jmp rel", got, []byte{0x90, 0xE9, 0x01, 0x00, 0x00, 0x00, 0x90})
	// jcc uses the 0F 8x form (jumps, NOT setcc).
	got = asmBytes(func(a *Asm) {
		l := a.NewLabel()
		a.Jcc(CC_LTS, l)
		if err := a.Bind(l); err != nil {
			panic(err)
		}
	})
	expectBytes(t, "jcc lts", got, []byte{0x0F, 0x8C, 0x00, 0x00, 0x00, 0x00})
	// backward jump to an already-bound label patches immediately.
	got = asmBytes(func(a *Asm) {
		l := a.NewLabel()
		a.emit1(0x90)
		if err := a.Bind(l); err != nil {
			panic(err)
		}
		a.emit1(0x90)
		a.emit1(0x90)
		a.Jmp(l) // at 3, rel32 at 4..7, target 1: rel = 1-8 = -7
	})
	expectBytes(t, "backward jmp", got,
		[]byte{0x90, 0x90, 0x90, 0xE9, 0xF9, 0xFF, 0xFF, 0xFF})
	// call placeholder records its site for the linker.
	a := &Asm{}
	a.CallFunc(7)
	expectBytes(t, "call placeholder", a.Code, []byte{0xE8, 0x00, 0x00, 0x00, 0x00})
	if len(a.Calls) != 1 || a.Calls[0].Pos != 1 || a.Calls[0].Target != 7 {
		t.Errorf("bad callsite: %+v", a.Calls)
	}
}

func TestGoldenALU32(t *testing.T) {
	expectBytes(t, "add32", asmBytes(func(a *Asm) { a.Add32() }), []byte{0x59, 0x58, 0x01, 0xC8, 0x50})
	expectBytes(t, "sub32", asmBytes(func(a *Asm) { a.Sub32() }), []byte{0x59, 0x58, 0x29, 0xC8, 0x50})
	expectBytes(t, "mul32", asmBytes(func(a *Asm) { a.Mul32() }), []byte{0x59, 0x58, 0x0F, 0xAF, 0xC1, 0x50})
	expectBytes(t, "and32", asmBytes(func(a *Asm) { a.And32() }), []byte{0x59, 0x58, 0x21, 0xC8, 0x50})
	expectBytes(t, "or32", asmBytes(func(a *Asm) { a.Or32() }), []byte{0x59, 0x58, 0x09, 0xC8, 0x50})
	expectBytes(t, "xor32", asmBytes(func(a *Asm) { a.Xor32() }), []byte{0x59, 0x58, 0x31, 0xC8, 0x50})
	expectBytes(t, "shl32", asmBytes(func(a *Asm) { a.Shift32(0xE0) }), []byte{0x59, 0x58, 0xD3, 0xE0, 0x50})
	expectBytes(t, "eqz32", asmBytes(func(a *Asm) { a.Eqz32() }),
		[]byte{0x58, 0x85, 0xC0, 0x0F, 0x94, 0xC0, 0x0F, 0xB6, 0xC0, 0x50})
	// THE critical assertion: comparisons use SETcc (0F 9x), never the
	// 0F 8x jump form. Every CC_* code must map to cc+0x10.
	for cc, setcc := range map[byte]byte{
		CC_EQ: 0x94, CC_NE: 0x95, CC_LTU: 0x92, CC_GEU: 0x93,
		CC_LEU: 0x96, CC_GTU: 0x97, CC_LTS: 0x9C, CC_GES: 0x9D,
		CC_LES: 0x9E, CC_GTS: 0x9F,
	} {
		got32 := asmBytes(func(a *Asm) { a.Cmp32(cc) })
		want := []byte{0x59, 0x58, 0x39, 0xC8, 0x0F, setcc, 0xC0, 0x0F, 0xB6, 0xC0, 0x50}
		expectBytes(t, "cmp32", got32, want)
		got64 := asmBytes(func(a *Asm) { a.Cmp64(cc) })
		want64 := []byte{0x59, 0x58, 0x48, 0x39, 0xC8, 0x0F, setcc, 0xC0, 0x0F, 0xB6, 0xC0, 0x50}
		expectBytes(t, "cmp64", got64, want64)
	}
}

func TestGoldenALU64(t *testing.T) {
	expectBytes(t, "add64", asmBytes(func(a *Asm) { a.Add64() }), []byte{0x59, 0x58, 0x48, 0x01, 0xC8, 0x50})
	expectBytes(t, "sub64", asmBytes(func(a *Asm) { a.Sub64() }), []byte{0x59, 0x58, 0x48, 0x29, 0xC8, 0x50})
	expectBytes(t, "shr64", asmBytes(func(a *Asm) { a.Shift64(0xE8) }), []byte{0x59, 0x58, 0x48, 0xD3, 0xE8, 0x50})
	expectBytes(t, "eqz64", asmBytes(func(a *Asm) { a.Eqz64() }),
		[]byte{0x58, 0x48, 0x85, 0xC0, 0x0F, 0x94, 0xC0, 0x0F, 0xB6, 0xC0, 0x50})
	expectBytes(t, "const32 -1", asmBytes(func(a *Asm) { a.Const32(-1) }),
		[]byte{0xB8, 0xFF, 0xFF, 0xFF, 0xFF, 0x50})
	expectBytes(t, "const64 -1", asmBytes(func(a *Asm) { a.Const64(-1) }),
		[]byte{0x48, 0xC7, 0xC0, 0xFF, 0xFF, 0xFF, 0xFF, 0x50})
	expectBytes(t, "const64 2^40", asmBytes(func(a *Asm) { a.Const64(1 << 40) }),
		[]byte{0x48, 0xB8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x50})
	// div paths must preserve the RDX trap pin and trap div-by-zero.
	got := asmBytes(func(a *Asm) { a.DivRem32(true, false) })
	if !bytes.Contains(got, []byte{0xC7, 0x02, 0x03, 0x00, 0x00, 0x00}) {
		t.Errorf("div32 missing divzero trap: %x", got)
	}
	// Popcnt32 exact sequence (pins the E0-vs-E4 modrm class: rm=100
	// in direct mode is ESP, and `and esp, imm` kills the stack).
	expectBytes(t, "popcnt32", asmBytes(func(a *Asm) { a.Popcnt32() }),
		[]byte{
			0x58, 0x89, 0xC1, 0xD1, 0xE9,
			0x81, 0xE1, 0x55, 0x55, 0x55, 0x55,
			0x29, 0xC8, 0x89, 0xC1, 0xC1, 0xE9, 0x02,
			0x81, 0xE1, 0x33, 0x33, 0x33, 0x33,
			0x81, 0xE0, 0x33, 0x33, 0x33, 0x33,
			0x01, 0xC8, 0x89, 0xC1, 0xC1, 0xE9, 0x04,
			0x01, 0xC8,
			0x81, 0xE0, 0x0F, 0x0F, 0x0F, 0x0F,
			0x69, 0xC0, 0x01, 0x01, 0x01, 0x01,
			0xC1, 0xE8, 0x18, 0x50,
		})
}

func TestGoldenMemory(t *testing.T) {
	// memEA(0,4): pop; r8d=eax; r9d=0; r8+=r9; cmp rsi,4; ... (57 bytes)
	got := asmBytes(func(a *Asm) { a.memEA(0, 4) })
	if len(got) != 57 {
		t.Errorf("memEA len %d, want 57: %x", len(got), got)
	}
	expectBytes(t, "memEA head", got[:17],
		[]byte{0x58, 0x41, 0x89, 0xC0, 0x41, 0xB9, 0x00, 0x00, 0x00, 0x00,
			0x4D, 0x01, 0xC8, 0x48, 0x83, 0xFE, 0x04})
	// every bounds jump targets the single trap block: find C7 02 02
	// (trap store) and assert both rel32s land on it.
	trapPos := bytes.Index(got, []byte{0xC7, 0x02, 0x02, 0x00, 0x00, 0x00})
	if trapPos < 0 {
		t.Fatalf("memEA has no trap block: %x", got)
	}
	for _, jpos := range []int{17, 33} { // the jb and ja sites (0F 82 / 0F 87)
		rel := int32(uint32(got[jpos+2]) | uint32(got[jpos+3])<<8 |
			uint32(got[jpos+4])<<16 | uint32(got[jpos+5])<<24)
		if int(jpos+6+int(rel)) != trapPos {
			t.Errorf("memEA jump at %d lands %d, want trap at %d", jpos, int(jpos+6+int(rel)), trapPos)
		}
	}
	// loads/stores end with the right SIB access + push.
	got = asmBytes(func(a *Asm) { a.memLoad(0, 4, false, false) })
	if !bytes.HasSuffix(got, []byte{0x42, 0x8B, 0x04, 0x07, 0x50}) {
		t.Errorf("i32.load tail: %x", got)
	}
	got = asmBytes(func(a *Asm) { a.memLoad(0, 1, true, true) })
	if !bytes.HasSuffix(got, []byte{0x4A, 0x0F, 0xBE, 0x04, 0x07, 0x50}) {
		t.Errorf("i64.load8_s tail: %x", got)
	}
	got = asmBytes(func(a *Asm) { a.memStore(0, 4) })
	if !bytes.Contains(got, []byte{0x4C, 0x89, 0xD8}) { // mov rax, r11
		t.Errorf("memStore missing value restore: %x", got)
	}
	if !bytes.HasSuffix(got, []byte{0x42, 0x89, 0x04, 0x07}) {
		t.Errorf("i32.store tail: %x", got)
	}
	// globals/locals/select/misc exact bytes.
	expectBytes(t, "global.get 3", asmBytes(func(a *Asm) { a.GlobalGet(3, true) }),
		[]byte{0x48, 0x8B, 0x83, 0x18, 0x00, 0x00, 0x00, 0x50})
	expectBytes(t, "global.set 3", asmBytes(func(a *Asm) { a.GlobalSet(3, true) }),
		[]byte{0x58, 0x48, 0x89, 0x83, 0x18, 0x00, 0x00, 0x00})
	expectBytes(t, "local.get 2", asmBytes(func(a *Asm) { a.LocalGet(2) }),
		[]byte{0x48, 0x8B, 0x85, 0xE8, 0xFF, 0xFF, 0xFF, 0x50})
	expectBytes(t, "select", asmBytes(func(a *Asm) { a.Select() }),
		[]byte{0x41, 0x58, 0x41, 0x59, 0x58, 0x4D, 0x85, 0xC0, 0x49, 0x0F, 0x44, 0xC1, 0x50})
	expectBytes(t, "memory.size", asmBytes(func(a *Asm) { a.MemorySize() }),
		[]byte{0x48, 0x89, 0xF0, 0x48, 0xC1, 0xE8, 0x10, 0x50})
	// bulk ops carry the OOB trap and the string instruction.
	got = asmBytes(func(a *Asm) { a.MemoryCopy() })
	if !bytes.Contains(got, []byte{0xF3, 0xA4}) || !bytes.Contains(got, []byte{0xC7, 0x02, 0x02}) {
		t.Errorf("memcpy missing rep movsb/trap: %x", got)
	}
	got = asmBytes(func(a *Asm) { a.MemoryFill() })
	if !bytes.Contains(got, []byte{0xF3, 0xAA}) || !bytes.Contains(got, []byte{0xC7, 0x02, 0x02}) {
		t.Errorf("memfill missing rep stosb/trap: %x", got)
	}
}

func TestGoldenImportFdWrite(t *testing.T) {
	// Pinned spills use callee-saved R12-R15 (WB form: dest-extended
	// only — 0x4D would misread sources as r8-r11). Correct in BOTH
	// ABIs: callees preserve R12-R14 everywhere (Go holds its
	// goroutine pointer in R14!); the host trampoline additionally
	// saves them around the call. XMM spills would be WRONG (all XMM
	// are caller-saved; the C handler clobbers them). Gas-verified.
	got := asmBytes(func(a *Asm) { a.ImportFdWrite(0, 0) })
	wantHead := []byte{
		0x41, 0x5B, // pop r11
		0x41, 0x59, // pop r9
		0x41, 0x58, // pop r8
		0x41, 0x5A, // pop r10
	}
	if len(got) < len(wantHead) || !bytes.Equal(got[:len(wantHead)], wantHead) {
		t.Fatalf("import spill/pops: %x", got)
	}
	// C-arg setup, static alignment, call placeholder, and trap check.
	for _, seq := range [][]byte{
		{0x48, 0x83, 0xEC, 0x08},             // one-slot alignment pad
		{0x41, 0x53},                         // push r11 (7th arg)
		{0x4C, 0x89, 0xD1},                   // mov rcx, r10 (fd)
		{0xE8},                               // call placeholder
		{0x48, 0x83, 0xC4, 0x10},             // drop nw + pad
		{0x48, 0x83, 0x3A, 0x00},             // cmp qword [rdx], 0
		{0xC7, 0x02, 0x02, 0x00, 0x00, 0x00}, // TrapOOB body
	} {
		if !bytes.Contains(got, seq) {
			t.Errorf("import sequence missing %x in %x", seq, got)
		}
	}
	// Exactly one call placeholder recorded as an import site... note:
	// ImportFdWrite emits the bare E8; the CALLER (compile.go) records
	// the ImportCallSite. Here assert the single E8 exists.
	n := 0
	for i := 0; i < len(got); i++ {
		if got[i] == 0xE8 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want exactly 1 call placeholder, got %d", n)
	}
}

func TestGoldenSafepoint(t *testing.T) {
	// E8 + zero rel32, position recorded for the linker.
	got := asmBytes(func(a *Asm) { a.Safepoint() })
	expectBytes(t, "safepoint", got, []byte{0xE8, 0, 0, 0, 0})
	a := &Asm{}
	pos := a.Safepoint()
	if pos != 1 || len(a.Polls) != 1 || a.Polls[0] != 1 {
		t.Fatalf("poll record %+v, want [1]", a.Polls)
	}
}

func TestSafepointSites(t *testing.T) {
	// () { loop { br 0 } }: entry poll + loop-top poll, ascending.
	wasm := module(
		typesSec(ftype(nil, nil)),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, 0x03, 0x40, 0x0C, 0x00, 0x0B, 0x0B)),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CompileOne(m, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Polls) != 2 {
		t.Fatalf("polls %+v, want 2 (entry + loop top)", a.Polls)
	}
	for _, p := range a.Polls {
		if p < 0 || p+4 >= len(a.Code) || a.Code[p-1] != 0xE8 {
			t.Fatalf("poll at %d not a call site in %d bytes", p, len(a.Code))
		}
	}
	if a.Polls[1] <= a.Polls[0] {
		t.Fatalf("polls not ascending: %+v", a.Polls)
	}
	// Loop-free function: entry poll only.
	wasm2 := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, 0x41, 0x07, 0x0B)),
	)
	m2, err := Decode(wasm2)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := CompileOne(m2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a2.Polls) != 1 {
		t.Fatalf("polls %+v, want 1 (entry only)", a2.Polls)
	}
}

func TestGoldenImportSchedYield(t *testing.T) {
	// Zero-arg sequence, result pushed: E8 placeholder only (pad
	// depends on site depth; nloc=0 depth=0 -> pad=(0+0+1)&1=1).
	got := asmBytes(func(a *Asm) { a.ImportSchedYield(0, 0, true) })
	expectBytes(t, "sched_yield pad+call+unpad+push", got, []byte{
		0x48, 0x83, 0xEC, 0x08, // sub rsp, 8
		0xE8, 0, 0, 0, 0, // call placeholder
		0x48, 0x83, 0xC4, 0x08, // add rsp, 8
		0x50, // push rax
	})
	// No-pad variant (nloc=1 depth=0 -> (1+0+1)&1=0), void shape.
	got2 := asmBytes(func(a *Asm) { a.ImportSchedYield(1, 0, false) })
	expectBytes(t, "sched_yield nopad void", got2, []byte{0xE8, 0, 0, 0, 0})
}

func TestGoldenImportCall3(t *testing.T) {
	// Pops land in the SysV arg regs (R9=a2, R8=a1, R10=a0), a0
	// forwarded via RCX; pinned RDI/RSI/RDX untouched. Same static
	// pad + trap tail as fd_write, minus the 7th stack argument.
	got := asmBytes(func(a *Asm) { a.ImportCall3(0, 0) })
	wantHead := []byte{
		0x41, 0x59, // pop r9 (len)
		0x41, 0x58, // pop r8 (ptr)
		0x41, 0x5A, // pop r10 (h)
	}
	if len(got) < len(wantHead) || !bytes.Equal(got[:len(wantHead)], wantHead) {
		t.Fatalf("port pops: %x", got)
	}
	for _, seq := range [][]byte{
		{0x48, 0x83, 0xEC, 0x08}, // one-slot alignment pad
		{0x4C, 0x89, 0xD1},       // mov rcx, r10 (h)
		{0xE8},                   // call placeholder
		{0x48, 0x83, 0xC4, 0x08}, // drop pad
		{0x48, 0x83, 0x3A, 0x00}, // cmp qword [rdx], 0
		{0x50},                   // push rax (status)
	} {
		if !bytes.Contains(got, seq) {
			t.Errorf("port sequence missing %x in %x", seq, got)
		}
	}
}

func TestGoldenImportCall2(t *testing.T) {
	// Two-arg shape: pops R8/R10 only, same pad/call/trap tail.
	got := asmBytes(func(a *Asm) { a.ImportCall2(0, 0) })
	wantHead := []byte{
		0x41, 0x58, // pop r8 (a1)
		0x41, 0x5A, // pop r10 (a0)
	}
	if len(got) < len(wantHead) || !bytes.Equal(got[:len(wantHead)], wantHead) {
		t.Fatalf("port-create pops: %x", got)
	}
	for _, seq := range [][]byte{
		{0x4C, 0x89, 0xD1},       // mov rcx, r10 (a0)
		{0xE8},                   // call placeholder
		{0x48, 0x83, 0x3A, 0x00}, // cmp qword [rdx], 0
		{0x50},                   // push rax (handle)
	} {
		if !bytes.Contains(got, seq) {
			t.Errorf("port-create sequence missing %x in %x", seq, got)
		}
	}
}
