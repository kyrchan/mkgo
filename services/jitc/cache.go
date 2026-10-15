package jitc

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// cache.go — native-cache container serializer (Stage 3a+3b-ii).
// Produces the exact byte layout core/jit.cc verifies (see jit.h +
// abi/ABI.md v2.3):
//
//	v1: u32 magic 'JITC' | u16 version | u16 abi_ver | u32 nfuncs |
//	    u32 code_len | u8 wasm_sha256[32] | u32 nrelocs |
//	    u32 func_off[nfuncs] | u8 code[code_len] |
//	    struct { u32 code_off, u32 target_func } relocs[nrelocs]
//	v2 (appended; version ∈ {1,2}, v1 decodes as empty extras):
//	    u32 mem_pages | u32 nimports |
//	    struct { u8 nargs, u8 nresults, u8 kind, u8 rsv }[nimports] |
//	    u32 nglobals | u64[nglobals] |
//	    u32 ndata | struct { u32 dst, u32 len }[ndata] | bytes... |
//	    u32 nirelocs | struct { u32 code_off, u32 import_idx }[nirelocs]
//	v3 (appended; version ∈ {1,2,3}):
//	    u32 npolls | u32 code_off[npolls]  (safe-point call sites;
//	    kernel patches them at bind to the preemption trampoline)
//	v4 (appended; version ∈ {1,2,3,4}):
//	    u32 wasm_off[nfuncs]  (absolute file offset of each defined
//	    function's body-size field — byte-identical to wasm3's
//	    (function->wasm - module base), which is the kernel dispatch
//	    lookup key; see FuncBody.WasmOff)
//
// Layout rule (load-bearing for the kernel's position-independent
// patching): functions concatenate in module-index order from offset
// 0; rel32 at code_off patches to func_off[target]-code_off-4. The
// kernel recomputes (never trusts stored patch values).

const (
	CacheMagic   uint32 = 0x4354494A // 'JITC' LE
	CacheVersion uint16 = 4
	CacheABIVer  uint16 = 2
)

// Reloc is one linker call site: rel32 at CodeOff targets Target.
type Reloc struct {
	CodeOff uint32 // offset of the rel32 word within its function
	Target  uint32 // module-wide function index (imports first)
	Func    uint32 // containing function index (for layout)
}

// ImportReloc is one NATIVE import call site: rel32 at CodeOff calls
// the import, resolved at bind to the per-kind handler. Positions are
// absolute region offsets (like Reloc post-link).
type ImportReloc struct {
	CodeOff uint32 // absolute offset of the rel32 word in Region
	Import  uint32 // module-wide func index of the import
	Func    uint32 // containing function index
}

// Import kinds (v2+ container meta + kernel dispatch). The producer
// owns import NAMES; the kernel trusts only the kind byte, never
// strings. APPEND-ONLY: renumbering invalidates cached blobs.
// Tier-1 = server set (no hardware); Tier-2 = device-adjacent.
const (
	ImportKindFdWrite    = 0
	ImportKindSchedYield = 1
	ImportKindPortCreate = 2
	ImportKindPortBind   = 3
	ImportKindPortSend   = 4
	ImportKindPortRecv   = 5
	ImportKindBlkRead    = 6
	ImportKindBlkWrite   = 7
	// Tier-1 reserved (kinds 8..31, assign on implementation):
	//  input_recv focus_set clock_time_get random_get args_get
	//  args_sizes_get environ_get environ_sizes_get proc_exit fd_read
	//  path_open path_create_directory fd_close fd_prestat_get
	//  fd_prestat_dir_name fd_fdstat_get fd_fdstat_set_flags poll_oneoff
	// Tier-2 reserved (kinds 32..63):
	//  pci_read32 pci_write32 pci_map_bar pci_unmap_bar
	//  pci_enable_busmaster pci_bind_irq pci_flr fb_set_mode
	//  fb_set_cursor fb_present doorbell_wait vmware_backdoor
	//  uart_read uart_write
	ImportKindTier1Base = 8
	ImportKindTier2Base = 32
)

// importKind resolves (module, name) to a kind byte. Unknown names fail
// closed (compile error on the producer side, INTERP fallback).
// Resolution is wider than emission: kinds with a table entry but no
// emitter sequence still fail at emitCall (kind unimplemented) — the
// meta stays correct for forward images.
func importKind(mod, name string) (uint8, bool) {
	if mod == "wasi_snapshot_preview1" {
		switch name {
		case "fd_write":
			return ImportKindFdWrite, true
		case "sched_yield":
			return ImportKindSchedYield, true
		}
		return 0, false
	}
	if mod == "kernel" {
		switch name {
		case "kern_port_create":
			return ImportKindPortCreate, true
		case "kern_port_bind":
			return ImportKindPortBind, true
		case "kern_port_send":
			return ImportKindPortSend, true
		case "kern_port_recv":
			return ImportKindPortRecv, true
		case "kern_blk_read":
			return ImportKindBlkRead, true
		case "kern_blk_write":
			return ImportKindBlkWrite, true
		}
		return 0, false
	}
	return 0, false
}

// ImportMeta describes one func import for the container + kernel.
type ImportMeta struct {
	NArgs    uint8
	NResults uint8
	Kind     uint8
}

// ImageRelocs carries a linked image plus its call sites resolved to
// absolute region offsets. CompileModuleWithRelocs builds it.
type ImageRelocs struct {
	Image        *Image
	Relocs       []Reloc
	ImportRelocs []ImportReloc
	PollRelocs   []uint32 // region-absolute safe-point rel32 offsets
	WasmOffs     []uint32 // per-defined-function body file offsets
	Imports      []ImportMeta
	MemPages     uint32
	Globals      []uint64
	Data         []DataSeg
}

// DataSeg is one flattened data segment for the initial memory image.
type DataSeg struct {
	Dst   uint32
	Bytes []byte
}

// CompileModuleWithRelocs compiles every defined function like
// CompileModule, but retains the call sites (for the cache reloc
// table) instead of only patching them.
func CompileModuleWithRelocs(m *Module) (*ImageRelocs, error) {
	nimport := m.ImportFuncCount()
	bufs := make([]*Asm, len(m.Code))
	irelocs := make([][]ImportReloc, len(m.Code))
	for i := range m.Code {
		a := &Asm{}
		var irel []ImportReloc
		if err := compileFunc(m, a, uint32(nimport+i), i, &irel); err != nil {
			return nil, fmt.Errorf("%w: func %d: %v", ErrCompile, nimport+i, err)
		}
		if len(a.fix) != 0 {
			return nil, fmt.Errorf("%w: func %d: %d unbound labels",
				ErrCompile, nimport+i, len(a.fix))
		}
		bufs[i] = a
		irelocs[i] = irel
	}
	ir := &ImageRelocs{Image: &Image{Funcs: map[uint32]int{}}}
	off := 0
	for i, b := range bufs {
		ir.Image.Funcs[uint32(nimport+i)] = off
		for _, cs := range b.Calls {
			if int(cs.Target) < nimport || int(cs.Target)-nimport >= len(bufs) {
				return nil, fmt.Errorf("%w: call to import %d",
					ErrImport, cs.Target)
			}
			ir.Relocs = append(ir.Relocs, Reloc{
				CodeOff: uint32(off + cs.Pos),
				Target:  cs.Target,
				Func:    uint32(nimport + i),
			})
		}
		for _, r := range irelocs[i] {
			ir.ImportRelocs = append(ir.ImportRelocs, ImportReloc{
				CodeOff: uint32(off) + r.CodeOff,
				Import:  r.Import,
				Func:    uint32(nimport + i),
			})
		}
		for _, p := range bufs[i].Polls {
			ir.PollRelocs = append(ir.PollRelocs, uint32(off+p))
		}
		ir.WasmOffs = append(ir.WasmOffs, m.Code[i].WasmOff)
		off += len(b.Code)
	}
	// Patch internal calls (same math the kernel re-derives at load).
	// Import calls stay zeroed: the kernel patches them at bind.
	ir.Image.Region = make([]byte, 0, off)
	for _, b := range bufs {
		ir.Image.Region = append(ir.Image.Region, b.Code...)
	}
	for _, r := range ir.Relocs {
		tgt := ir.Image.Funcs[r.Target]
		rel := int32(tgt - (int(r.CodeOff) + 4))
		binary.LittleEndian.PutUint32(ir.Image.Region[r.CodeOff:], uint32(rel))
	}
	// Import metadata in func-import order (kernel dispatch needs it).
	for _, im := range m.Imports {
		if im.Kind != 0 {
			continue
		}
		if uint64(im.Type) >= uint64(len(m.Types)) {
			return nil, fmt.Errorf("jitc: import type %d out of range", im.Type)
		}
		ft := m.Types[im.Type]
		// Kind resolution lives here (producer owns names); the
		// kernel trusts only the kind byte, never strings.
		kind, ok := importKind(im.Module, im.Name)
		if !ok {
			return nil, fmt.Errorf("%w: import %s.%s", ErrImport, im.Module, im.Name)
		}
		ir.Imports = append(ir.Imports, ImportMeta{
			NArgs: uint8(len(ft.Params)), NResults: uint8(len(ft.Results)),
			Kind: uint8(kind),
		})
	}
	// Memory + globals + data for session instantiation.
	if len(m.Mems) > 1 {
		return nil, fmt.Errorf("%w: multi-memory", ErrCompile)
	}
	if len(m.Mems) == 1 {
		if m.Mems[0].Min > 256 {
			return nil, fmt.Errorf("%w: memory too large", ErrCompile)
		}
		ir.MemPages = m.Mems[0].Min
	}
	for _, g := range m.Globals {
		if !g.Const {
			return nil, fmt.Errorf("%w: non-const global init", ErrCompile)
		}
		ir.Globals = append(ir.Globals, uint64(g.Init))
	}
	for _, d := range m.Data {
		if !d.Const {
			return nil, fmt.Errorf("%w: non-const data offset", ErrCompile)
		}
		ir.Data = append(ir.Data, DataSeg{Dst: uint32(d.Off), Bytes: d.Bytes})
	}
	return ir, nil
}

// BuildCache serializes (module source bytes, compiled image) into a
// container the kernel accepts. abiVer stamps the guest ABI version
// (must equal the kernel's). Func offsets are implicit: functions lay
// out in index order, so offsets derive from lengths.
func BuildCache(wasmBytes []byte, abiVer uint16, m *Module, ir *ImageRelocs) ([]byte, error) {
	if abiVer != CacheABIVer {
		return nil, fmt.Errorf("jitc: abi_ver %d != %d", abiVer, CacheABIVer)
	}
	nimport := m.ImportFuncCount()
	nfuncs := len(m.Code)
	if nfuncs == 0 || nfuncs > 4096 {
		return nil, errors.New("jitc: bad func count")
	}
	if len(ir.Image.Region) == 0 || len(ir.Image.Region) > 32*1024*1024 {
		return nil, errors.New("jitc: bad code size")
	}
	// Func offsets in index order.
	offs := make([]uint32, nfuncs)
	off := uint32(0)
	// Re-derive per-function lengths by re-emitting? No — lengths come
	// from the link layout: recompute by compiling each function and
	// measuring. Cheaper: derive from Image.Funcs bases + total.
	bases := make([]int, nfuncs+1)
	for i := 0; i < nfuncs; i++ {
		b, ok := ir.Image.Funcs[uint32(nimport+i)]
		if !ok {
			return nil, fmt.Errorf("jitc: func %d missing", nimport+i)
		}
		bases[i] = b
	}
	bases[nfuncs] = len(ir.Image.Region)
	for i := 0; i < nfuncs; i++ {
		if bases[i+1] < bases[i] {
			return nil, errors.New("jitc: layout not ascending")
		}
		offs[i] = uint32(bases[i])
		_ = off
	}
	sum := sha256.Sum256(wasmBytes)
	out := make([]byte, 0, 52+4*nfuncs+len(ir.Image.Region)+8*len(ir.Relocs))
	var hdr [52]byte
	binary.LittleEndian.PutUint32(hdr[0:], CacheMagic)
	binary.LittleEndian.PutUint16(hdr[4:], CacheVersion)
	binary.LittleEndian.PutUint16(hdr[6:], abiVer)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(nfuncs))
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(ir.Image.Region)))
	copy(hdr[16:], sum[:])
	binary.LittleEndian.PutUint32(hdr[48:], uint32(len(ir.Relocs)))
	out = append(out, hdr[:]...)
	for _, o := range offs {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], o)
		out = append(out, b[:]...)
	}
	out = append(out, ir.Image.Region...)
	for _, r := range ir.Relocs {
		// Region-absolute code offset (what the kernel patches in
		// place); the Func field is producer-side bookkeeping only.
		var b [8]byte
		binary.LittleEndian.PutUint32(b[0:], r.CodeOff)
		binary.LittleEndian.PutUint32(b[4:], r.Target)
		out = append(out, b[:]...)
	}
	// ---- v2 tail (kernel requires version ∈ {1,2}; v1 files end at
	// the relocs above and decode as empty extras). All counts
	// bounds-checked by the kernel before any copy.
	var v2 [4]byte
	binary.LittleEndian.PutUint32(v2[:], ir.MemPages)
	out = append(out, v2[:]...)
	binary.LittleEndian.PutUint32(v2[:], uint32(len(ir.Imports)))
	out = append(out, v2[:]...)
	for _, im := range ir.Imports {
		out = append(out, im.NArgs, im.NResults, im.Kind, 0)
	}
	binary.LittleEndian.PutUint32(v2[:], uint32(len(ir.Globals)))
	out = append(out, v2[:]...)
	for _, g := range ir.Globals {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], g)
		out = append(out, b[:]...)
	}
	binary.LittleEndian.PutUint32(v2[:], uint32(len(ir.Data)))
	out = append(out, v2[:]...)
	for _, d := range ir.Data {
		var b [8]byte
		binary.LittleEndian.PutUint32(b[0:], d.Dst)
		binary.LittleEndian.PutUint32(b[4:], uint32(len(d.Bytes)))
		out = append(out, b[:]...)
	}
	for _, d := range ir.Data {
		out = append(out, d.Bytes...)
	}
	binary.LittleEndian.PutUint32(v2[:], uint32(len(ir.ImportRelocs)))
	out = append(out, v2[:]...)
	for _, r := range ir.ImportRelocs {
		var b [8]byte
		binary.LittleEndian.PutUint32(b[0:], r.CodeOff)
		binary.LittleEndian.PutUint32(b[4:], r.Import)
		out = append(out, b[:]...)
	}
	// ---- v3 tail: safe-point table (kernel patches at bind).
	binary.LittleEndian.PutUint32(v2[:], uint32(len(ir.PollRelocs)))
	out = append(out, v2[:]...)
	for _, p := range ir.PollRelocs {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], p)
		out = append(out, b[:]...)
	}
	// ---- v4 tail: wasm body offsets (kernel dispatch key).
	if len(ir.WasmOffs) != nfuncs {
		return nil, errors.New("jitc: wasm offset count mismatch")
	}
	for _, o := range ir.WasmOffs {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], o)
		out = append(out, b[:]...)
	}
	return out, nil
}
