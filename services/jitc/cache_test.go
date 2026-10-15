package jitc

import (
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

// cache_test.go — container serializer tests (no build tag: pure bytes,
// runs anywhere; execution of the cache contents is covered by the
// linux/amd64 exec tests).

func TestBuildCacheHeader(t *testing.T) {
	// () -> i32 { i32.const 7; i32.const 6; i32.add }.
	wasm := module(
		typesSec(ftype(nil, []byte{0x7F})),
		funcsSec(0),
		exportSec("main", 0, 0),
		codeSec(body(nil, 0x41, 0x07, 0x41, 0x06, 0x6A, 0x0B)),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	out, err := BuildCache(wasm, 2, m, ir)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(out[0:]) != CacheMagic {
		t.Fatalf("magic %x", binary.LittleEndian.Uint32(out[0:]))
	}
	if binary.LittleEndian.Uint16(out[4:]) != CacheVersion {
		t.Fatalf("version %d", binary.LittleEndian.Uint16(out[4:]))
	}
	if binary.LittleEndian.Uint16(out[6:]) != 2 {
		t.Fatalf("abiver %d", binary.LittleEndian.Uint16(out[6:]))
	}
	nfuncs := binary.LittleEndian.Uint32(out[8:])
	codelen := binary.LittleEndian.Uint32(out[12:])
	nrelocs := binary.LittleEndian.Uint32(out[48:])
	if nfuncs != 1 || nrelocs != 0 {
		t.Fatalf("nfuncs=%d nrelocs=%d", nfuncs, nrelocs)
	}
	sum := sha256.Sum256(wasm)
	for i := 0; i < 32; i++ {
		if out[16+i] != sum[i] {
			t.Fatalf("hash byte %d mismatch", i)
		}
	}
	// Layout: v1 frame plus the fixed v2 metadata counts plus the v3
	// poll section plus the v4 wasm-offset table. This module is one
	// loop-free function: exactly one safe-point (function entry).
	v2Base := 52 + 4*nfuncs + codelen + 8*nrelocs
	extra := uint32(4 + 4 + 4 + 4 + 4) // mem_pages, nimports, nglobals, ndata, nirelocs
	// v3 polls + v4 wasm offsets (one defined function).
	v4extra := uint32(4 + 4*uint32(len(ir.PollRelocs)) + 4*1)
	if uint32(len(out)) != v2Base+extra+v4extra {
		t.Fatalf("len %d vs framed %d", len(out), v2Base+extra+v4extra)
	}
	if len(ir.PollRelocs) != 1 {
		t.Fatalf("polls %d, want 1 (entry only)", len(ir.PollRelocs))
	}
	if binary.LittleEndian.Uint32(out[v2Base:]) != 0 {
		t.Fatalf("mem_pages %d, want 0", binary.LittleEndian.Uint32(out[v2Base:]))
	}
	if binary.LittleEndian.Uint32(out[v2Base+4:]) != 0 {
		t.Fatalf("nimports %d, want 0", binary.LittleEndian.Uint32(out[v2Base+4:]))
	}
	if binary.LittleEndian.Uint32(out[v2Base+8:]) != 0 {
		t.Fatalf("nglobals %d, want 0", binary.LittleEndian.Uint32(out[v2Base+8:]))
	}
	if binary.LittleEndian.Uint32(out[v2Base+12:]) != 0 {
		t.Fatalf("ndata %d, want 0", binary.LittleEndian.Uint32(out[v2Base+12:]))
	}
	if binary.LittleEndian.Uint32(out[v2Base+16:]) != 0 {
		t.Fatalf("nirelocs %d, want 0", binary.LittleEndian.Uint32(out[v2Base+16:]))
	}
	// v3 tail: npolls then absolute code offsets, each inside the code.
	npollsOff := v2Base + 20
	if binary.LittleEndian.Uint32(out[npollsOff:]) != uint32(len(ir.PollRelocs)) {
		t.Fatalf("npolls %d, want %d",
			binary.LittleEndian.Uint32(out[npollsOff:]), len(ir.PollRelocs))
	}
	for i, p := range ir.PollRelocs {
		got := binary.LittleEndian.Uint32(out[npollsOff+4+4*uint32(i):])
		if got != p {
			t.Fatalf("poll %d off %d vs %d", i, got, p)
		}
		if p+4 > codelen || out[56+p-1] != 0xE8 {
			t.Fatalf("poll %d off %d not a call site", i, p)
		}
	}
	// v4 tail: one wasm body offset, inside the module, ascending
	// (trivially, single function).
	woffBase := npollsOff + 4 + 4*uint32(len(ir.PollRelocs))
	wo := binary.LittleEndian.Uint32(out[woffBase:])
	if wo == 0 || wo >= uint32(len(wasm)) {
		t.Fatalf("wasm_off %d outside module len %d", wo, len(wasm))
	}
	if m.Code[0].WasmOff != wo {
		t.Fatalf("wasm_off %d vs decoded %d", wo, m.Code[0].WasmOff)
	}
	if binary.LittleEndian.Uint32(out[52:]) != 0 {
		t.Fatalf("func0 off %d, want 0", binary.LittleEndian.Uint32(out[52:]))
	}
	if codelen != uint32(len(ir.Image.Region)) {
		t.Fatalf("codelen %d vs region %d", codelen, len(ir.Image.Region))
	}
	// Code bytes round-trip verbatim.
	for i, b := range ir.Image.Region {
		if out[56+i] != b {
			t.Fatalf("code byte %d differs", i)
		}
	}
	// Wrong abi_ver refused.
	if _, err := BuildCache(wasm, 1, m, ir); err == nil {
		t.Fatalf("abi_ver 1 accepted")
	}
}

func TestImportKindTable(t *testing.T) {
	// Append-only registry: known names resolve, everything else fails
	// closed (producer side of the kernel-trusts-kind-byte contract).
	for _, tc := range []struct {
		mod, name string
		kind      uint8
	}{
		{"wasi_snapshot_preview1", "fd_write", ImportKindFdWrite},
		{"wasi_snapshot_preview1", "sched_yield", ImportKindSchedYield},
		{"kernel", "kern_port_create", ImportKindPortCreate},
		{"kernel", "kern_port_bind", ImportKindPortBind},
		{"kernel", "kern_port_send", ImportKindPortSend},
		{"kernel", "kern_port_recv", ImportKindPortRecv},
		{"kernel", "kern_blk_read", ImportKindBlkRead},
		{"kernel", "kern_blk_write", ImportKindBlkWrite},
	} {
		k, ok := importKind(tc.mod, tc.name)
		if !ok || k != tc.kind {
			t.Fatalf("importKind(%s.%s) = %d,%v, want %d,true",
				tc.mod, tc.name, k, ok, tc.kind)
		}
	}
	for _, tc := range [][2]string{
		{"wasi_snapshot_preview1", "nope"},
		{"kernel", "kern_input_recv"}, // Tier-1, not yet implemented
		{"kernel", "kern_pci_map_bar"},
		{"env", "memory"},
	} {
		if k, ok := importKind(tc[0], tc[1]); ok {
			t.Fatalf("importKind(%s.%s) = %d,true, want false",
				tc[0], tc[1], k)
		}
	}
}

func importSec(mod, name string, kind byte, typeidx uint32) []byte {
	p := uleb(1)
	p = append(p, uleb(uint32(len(mod)))...)
	p = append(p, mod...)
	p = append(p, uleb(uint32(len(name)))...)
	p = append(p, name...)
	p = append(p, kind)
	p = append(p, uleb(typeidx)...)
	return sec(2, p)
}

func TestSchedYieldReloc(t *testing.T) {
	// import wasi.sched_yield: ()->(i32); main() { call 0; drop }.
	// i() shape: errno pushed, dropped.
	wasm := module(
		typesSec(ftype(nil, []byte{0x7F}), ftype(nil, nil)),
		importSec("wasi_snapshot_preview1", "sched_yield", 0, 0),
		funcsSec(1),
		exportSec("main", 0, 1),
		codeSec(body(nil, 0x10, 0x00, 0x1A, 0x0B)),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	// Pure context still rejects (no trampoline without relocs).
	if _, err := CompileModule(m); err == nil {
		t.Fatalf("pure CompileModule accepted an import call")
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.ImportRelocs) != 1 {
		t.Fatalf("irelocs %+v, want exactly 1", ir.ImportRelocs)
	}
	if ir.ImportRelocs[0].Import != 0 {
		t.Fatalf("ireloc import %d, want 0", ir.ImportRelocs[0].Import)
	}
	if len(ir.Imports) != 1 || ir.Imports[0].Kind != ImportKindSchedYield {
		t.Fatalf("import meta %+v, want kind %d", ir.Imports, ImportKindSchedYield)
	}
	at := ir.ImportRelocs[0].CodeOff
	if at < 1 || at+4 > uint32(len(ir.Image.Region)) ||
		ir.Image.Region[at-1] != 0xE8 {
		t.Fatalf("ireloc at %d not a call site", at)
	}
}

func TestPortCallReloc(t *testing.T) {
	// import kernel.kern_port_send: (i32,i32,i32)->(i32);
	// main(h,ptr,len) { call 0; drop }.
	wasm := module(
		typesSec(ftype([]byte{0x7F, 0x7F, 0x7F}, []byte{0x7F}),
			ftype([]byte{0x7F, 0x7F, 0x7F}, nil)),
		importSec("kernel", "kern_port_send", 0, 0),
		funcsSec(1),
		exportSec("main", 0, 1),
		codeSec(body(nil, 0x20, 0x00, 0x20, 0x01, 0x20, 0x02,
			0x10, 0x00, 0x1A, 0x0B)),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileModule(m); err == nil {
		t.Fatalf("pure CompileModule accepted an import call")
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.ImportRelocs) != 1 {
		t.Fatalf("irelocs %+v, want exactly 1", ir.ImportRelocs)
	}
	if ir.ImportRelocs[0].Import != 0 {
		t.Fatalf("ireloc import %d, want 0", ir.ImportRelocs[0].Import)
	}
	if len(ir.Imports) != 1 || ir.Imports[0].Kind != ImportKindPortSend {
		t.Fatalf("import meta %+v, want kind %d", ir.Imports, ImportKindPortSend)
	}
	if ir.Imports[0].NArgs != 3 || ir.Imports[0].NResults != 1 {
		t.Fatalf("import meta arity %+v", ir.Imports[0])
	}
}

func TestPortCreateReloc(t *testing.T) {
	// import kernel.kern_port_create: (i32,i32)->(i32);
	// main(ptr,len) { call 0; drop }.
	wasm := module(
		typesSec(ftype([]byte{0x7F, 0x7F}, []byte{0x7F}),
			ftype([]byte{0x7F, 0x7F}, nil)),
		importSec("kernel", "kern_port_create", 0, 0),
		funcsSec(1),
		exportSec("main", 0, 1),
		codeSec(body(nil, 0x20, 0x00, 0x20, 0x01,
			0x10, 0x00, 0x1A, 0x0B)),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileModule(m); err == nil {
		t.Fatalf("pure CompileModule accepted an import call")
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.ImportRelocs) != 1 {
		t.Fatalf("irelocs %+v, want exactly 1", ir.ImportRelocs)
	}
	if len(ir.Imports) != 1 || ir.Imports[0].Kind != ImportKindPortCreate {
		t.Fatalf("import meta %+v, want kind %d", ir.Imports, ImportKindPortCreate)
	}
}

func TestBuildCacheRelocs(t *testing.T) {
	// sq + cube (one cross-function call → one reloc).
	t0 := ftype([]byte{0x7F}, []byte{0x7F})
	sq := body(nil, 0x20, 0x00, 0x20, 0x00, 0x6C, 0x0B)
	cube := body(nil, 0x20, 0x00, 0x10, 0x00, 0x20, 0x00, 0x6C, 0x0B)
	wasm := module(
		typesSec(t0),
		funcsSec(0, 0),
		exportSec("cube", 0, 1),
		codeSec(sq, cube),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Relocs) != 1 {
		t.Fatalf("relocs %d, want 1", len(ir.Relocs))
	}
	if ir.Relocs[0].Target != 0 || ir.Relocs[0].Func != 1 {
		t.Fatalf("reloc %+v", ir.Relocs[0])
	}
	out, err := BuildCache(wasm, 2, m, ir)
	if err != nil {
		t.Fatal(err)
	}
	nrelocs := binary.LittleEndian.Uint32(out[48:])
	if nrelocs != 1 {
		t.Fatalf("nrelocs %d", nrelocs)
	}
	// Reloc entry echoes absolute code offset + target.
	base := 52 + 8 + uint32(len(ir.Image.Region))
	at := binary.LittleEndian.Uint32(out[base:])
	tgt := binary.LittleEndian.Uint32(out[base+4:])
	if at != ir.Relocs[0].CodeOff || tgt != 0 {
		t.Fatalf("reloc entry %d->%d vs %+v", at, tgt, ir.Relocs[0])
	}
	// Patched value in the region equals target-(off+4).
	got := int32(binary.LittleEndian.Uint32(ir.Image.Region[at:]))
	if got != 0-int32(at)-4 {
		t.Fatalf("patched rel32 %d, want %d", got, 0-int32(at)-4)
	}
}

func TestCompileModuleParity(t *testing.T) { // WithRelocs must produce byte-identical regions to CompileModule.
	wasm := module(
		typesSec(ftype([]byte{0x7F}, []byte{0x7F})),
		funcsSec(0, 0),
		exportSec("cube", 0, 1),
		codeSec(
			body(nil, 0x20, 0x00, 0x20, 0x00, 0x6C, 0x0B),
			body(nil, 0x20, 0x00, 0x10, 0x00, 0x20, 0x00, 0x6C, 0x0B),
		),
	)
	m, err := Decode(wasm)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CompileModule(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Region) != len(b.Image.Region) {
		t.Fatalf("region len %d vs %d", len(a.Region), len(b.Image.Region))
	}
	for i := range a.Region {
		if a.Region[i] != b.Image.Region[i] {
			t.Fatalf("region byte %d differs", i)
		}
	}
}

func TestFibModuleDecodes(t *testing.T) {
	// The smoke-gate artifact must decode (1 import + main + fib),
	// declare main ()->(i32), and carry compilable pure functions plus
	// the fd_write import path.
	raw := FibModule()
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode(FibModule): %v", err)
	}
	if v, ok := m.HasABIVer(); !ok || v != 2 {
		t.Fatalf("abi_ver = %d, %v; engine_init would refuse it", v, ok)
	}
	if len(m.Code) != 2 || len(m.Funcs) != 2 || m.ImportFuncCount() != 1 {
		t.Fatalf("shape: %d funcs %d imports", len(m.Code), m.ImportFuncCount())
	}
	if len(m.Mems) != 1 || m.Mems[0].Min != 1 {
		t.Fatalf("memory: %+v", m.Mems)
	}
	if len(m.Data) != 2 {
		t.Fatalf("data segs: %d", len(m.Data))
	}
	ft, err := funcTypeOf(m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.Params) != 0 || len(ft.Results) != 1 || ft.Results[0] != VTi32 {
		t.Fatalf("main type %+v", ft)
	}
	if _, err := CompileOne(m, 2); err != nil {
		t.Fatalf("CompileOne(2): %v", err)
	}
	if _, err := CompileOne(m, 1); err == nil {
		t.Fatalf("CompileOne(1) accepted an unbound fd_write import")
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Relocs) != 4 {
		t.Fatalf("relocs %d, want 4", len(ir.Relocs))
	}
	if len(ir.ImportRelocs) != 2 {
		t.Fatalf("import relocs %d, want 2 (fd_write x2)", len(ir.ImportRelocs))
	}
	if len(ir.Imports) != 1 || ir.Imports[0].Kind != ImportKindFdWrite {
		t.Fatalf("imports %+v", ir.Imports)
	}
	if ir.MemPages != 1 || len(ir.Data) != 2 {
		t.Fatalf("mem %d data %d", ir.MemPages, len(ir.Data))
	}
	if _, err := BuildCache(raw, 2, m, ir); err != nil {
		t.Fatalf("BuildCache: %v", err)
	}
}
