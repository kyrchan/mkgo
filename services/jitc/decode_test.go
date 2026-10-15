package jitc

import (
	"errors"
	"os"
	"testing"
)

// minimalModule: () -> () func exported as "_start" with an empty body.
var minimalModule = []byte{
	0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, // magic + version
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: 1 x (() -> ())
	0x03, 0x02, 0x01, 0x00, // func: 1 x type 0
	0x07, 0x0A, 0x01, 0x06, 0x5F, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x00, // export "_start" func 0
	0x0A, 0x04, 0x01, 0x02, 0x00, 0x0B, // code: 1 x (no locals, end)
}

// arithModule: body exercises const/add/drop + a nested block.
var arithModule = []byte{
	0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
	0x03, 0x02, 0x01, 0x00,
	0x0A, 0x0E, 0x01, 0x0C,
	0x00,       // no locals
	0x41, 0x01, // i32.const 1
	0x41, 0x02, // i32.const 2
	0x6A,       // i32.add
	0x1A,       // drop
	0x02, 0x40, // block (empty)
	0x01, // nop
	0x0B, // end block
	0x0B, // end func
}

func TestDecodeMinimal(t *testing.T) {
	m, err := Decode(minimalModule)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(m.Types) != 1 || len(m.Types[0].Params) != 0 || len(m.Types[0].Results) != 0 {
		t.Fatalf("bad type section: %+v", m.Types)
	}
	if len(m.Funcs) != 1 || m.Funcs[0] != 0 {
		t.Fatalf("bad func section: %v", m.Funcs)
	}
	if len(m.Exports) != 1 || m.Exports[0].Name != "_start" || m.Exports[0].Kind != 0 {
		t.Fatalf("bad export section: %+v", m.Exports)
	}
	if len(m.Code) != 1 || len(m.Code[0].Ops) != 1 || m.Code[0].Ops[0].Code != 0x0B {
		t.Fatalf("bad code section: %+v", m.Code)
	}
}

func TestDecodeArithBlock(t *testing.T) {
	m, err := Decode(arithModule)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	ops := m.Code[0].Ops
	want := []byte{0x41, 0x41, 0x6A, 0x1A, 0x02, 0x01, 0x0B, 0x0B}
	if len(ops) != len(want) {
		t.Fatalf("got %d ops, want %d", len(ops), len(want))
	}
	for i, w := range want {
		if ops[i].Code != w {
			t.Fatalf("op %d = 0x%02x, want 0x%02x", i, ops[i].Code, w)
		}
	}
	if ops[0].I32 != 1 || ops[1].I32 != 2 {
		t.Fatalf("bad const immediates: %+v", ops[:2])
	}
	if !ops[4].Block.Empty {
		t.Fatalf("block should be empty-typed: %+v", ops[4].Block)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"bad magic", []byte{0x01, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}},
		{"bad version", []byte{0x00, 0x61, 0x73, 0x6D, 0x02, 0x00, 0x00, 0x00}},
		{"truncated header", []byte{0x00, 0x61, 0x73}},
		{"truncated section", append(append([]byte{}, minimalModule[:8]...), 0x01)},
		{"overlong leb", []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
			0x01, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}},
		{"float op decodes (outlined in Stage 2)", []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
			0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
			0x03, 0x02, 0x01, 0x00,
			0x0A, 0x05, 0x01, 0x03, 0x00, 0xA2, 0x0B}}, // f64.mul body
		{"unassigned opcode 0xFF", []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
			0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
			0x03, 0x02, 0x01, 0x00,
			0x0A, 0x05, 0x01, 0x03, 0x00, 0xFF, 0x0B}},
		{"SIMD prefix 0xFD (out of v1 scope)", []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
			0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
			0x03, 0x02, 0x01, 0x00,
			0x0A, 0x06, 0x01, 0x04, 0x00, 0xFD, 0x0C, 0x0B}},
	}
	for _, tc := range cases[:6] {
		if _, err := Decode(tc.b); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
	if _, err := Decode(cases[6].b); err != nil {
		t.Errorf("%s: expected success, got %v", cases[6].name, err)
	}
	for _, tc := range cases[7:] {
		if _, err := Decode(tc.b); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
	// overlong LEB must specifically be overflow, not silent truncation
	bad := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
	c := &cursor{b: bad[8:]}
	c.u8() // section id
	if _, err := c.u32leb(); !errors.Is(err, ErrOverflow) {
		t.Errorf("overlong LEB: got %v, want ErrOverflow", err)
	}
}

func TestLEBVectors(t *testing.T) {
	u32cases := map[uint32][]byte{
		0:          {0x00},
		1:          {0x01},
		127:        {0x7F},
		128:        {0x80, 0x01},
		624485:     {0xE5, 0x8E, 0x26},
		0xFFFFFFFF: {0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
	}
	for want, enc := range u32cases {
		c := &cursor{b: enc}
		got, err := c.u32leb()
		if err != nil || got != want {
			t.Errorf("u32leb(%x) = %d,%v; want %d", enc, got, err, want)
		}
	}
	s32cases := map[int32][]byte{
		0:           {0x00},
		-1:          {0x7F},
		1:           {0x01},
		-128:        {0x80, 0x7F},
		624485:      {0xE5, 0x8E, 0x26},
		-624485:     {0x9B, 0xF1, 0x59},
		-2147483648: {0x80, 0x80, 0x80, 0x80, 0x78},
	}
	for want, enc := range s32cases {
		c := &cursor{b: enc}
		got, err := c.s32leb()
		if err != nil || got != want {
			t.Errorf("s32leb(%x) = %d,%v; want %d", enc, got, err, want)
		}
	}
}

// TestDecodeRealModule exercises the decoder on a stock-toolchain Go
// artifact (the real fs server build). Skipped gracefully if the file
// is absent (e.g. fresh checkout before service builds).
func TestDecodeRealModule(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Skipf("no fs.wasm.raw: %v", err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode(fs.wasm.raw): %v", err)
	}
	if len(m.Code) == 0 || len(m.Funcs) == 0 {
		t.Fatalf("empty code/func sections in real module")
	}
	t.Logf("fs.wasm.raw: %d types %d imports %d funcs %d bodies %d mems %d exports",
		len(m.Types), len(m.Imports), len(m.Funcs), len(m.Code), len(m.Mems), len(m.Exports))
	if len(m.Mems) == 0 {
		t.Errorf("Go module without a memory section?")
	}
}

// TestABIVerMarker decodes the stamped artifact and checks the kernel's
// abi_ver custom section (the writer is scripts/add_abiver.py).
func TestABIVerMarker(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm")
	if err != nil {
		t.Skipf("no fs.wasm: %v", err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode(fs.wasm): %v", err)
	}
	v, ok := m.HasABIVer()
	if !ok {
		t.Fatalf("stamped fs.wasm lacks abi_ver section")
	}
	if v != 2 {
		t.Fatalf("abi_ver = %d, want 2", v)
	}
}
