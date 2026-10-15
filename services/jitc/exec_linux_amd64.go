//go:build linux && amd64

package jitc

import (
	"errors"
	"syscall"
	"unsafe"
)

// exec_linux_amd64.go — host-test execution harness (Stage 2a ONLY).
// Maps compiled code RWX, runs it through the assembly trampoline,
// and reports (result, trap). RWX mapping is a test-only convenience;
// the kernel loader (Stage 3) will use separate W^X windows with
// signature verification instead.

// execCode is implemented in call_amd64.s. Convention (see emit.go):
//
//	RDI=mem, RSI=memLen, RDX=trapSlot, RBX=globals, R10=args, R11=nargs
//
// returns RAX.
func execCode(code, mem unsafe.Pointer, memLen uint64, trap *uint64,
	globals, args unsafe.Pointer, nargs int) uint64

// ExecResult is one executed call's outcome.
type ExecResult struct {
	Value uint64 // RAX on return (meaningful only when Trap == 0)
	Trap  uint64 // 0 = ok, else Trap* code
}

// Runner holds one linked image mapped executable with its memory.
type Runner struct {
	code    []byte // RWX mapping (must stay alive + unmapped on Close)
	mem     []byte
	globals []uint64
	img     *Image
}

// NewRunner maps img executable with npages of zeroed linear memory.
// Globals are initialized from the module's const initializers;
// modules with global.get initializers are rejected (Stage 3 work).
func NewRunner(m *Module, img *Image, npages int) (*Runner, error) {
	if npages < 1 || npages > 256 {
		return nil, errors.New("jitc: npages out of range 1..256")
	}
	for _, g := range m.Globals {
		if !g.Const {
			return nil, errors.New("jitc: global.get init needs Stage 3")
		}
	}
	if len(m.Mems) > 1 {
		return nil, errors.New("jitc: multi-memory needs Stage 3")
	}
	size := len(img.Region)
	if size == 0 {
		return nil, errors.New("jitc: empty region")
	}
	// One spare byte minimum for the poll RET sled below.
	pages := (size + 1 + 0xFFF) &^ 0xFFF
	code, err := syscall.Mmap(-1, 0, pages,
		syscall.PROT_READ|syscall.PROT_WRITE|syscall.PROT_EXEC,
		syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, err
	}
	copy(code, img.Region)
	// Safe-point patching (host harness): poll placeholders call a
	// one-byte RET sled appended here, so polls are balanced no-ops
	// under test. The kernel patches the same sites to its preemption
	// trampoline at bind instead.
	if len(img.Polls) > 0 {
		if len(code)-size < 1 {
			syscall.Munmap(code)
			return nil, errors.New("jitc: no room for poll sled")
		}
		sled := size
		code[sled] = 0xC3 // ret
		for _, p := range img.Polls {
			if p < 0 || p+4 > size {
				syscall.Munmap(code)
				return nil, errors.New("jitc: poll site out of range")
			}
			rel := int32(sled - (p + 4))
			code[p] = byte(rel)
			code[p+1] = byte(rel >> 8)
			code[p+2] = byte(rel >> 16)
			code[p+3] = byte(rel >> 24)
		}
	}
	globals := make([]uint64, len(m.Globals))
	for i, g := range m.Globals {
		globals[i] = uint64(g.Init)
	}
	// Linear memory + initial image (mirrors the kernel bind path:
	// zeroed pages, then const-offset data segments, bounds-checked).
	// The memory is at least Min pages; an explicit larger npages (as
	// in the memory.size test) is honored.
	var mem []byte
	if len(m.Mems) > 0 {
		if m.Mems[0].Min < 1 || m.Mems[0].Min > 256 {
			syscall.Munmap(code)
			return nil, errors.New("jitc: bad memory size")
		}
		if npages < int(m.Mems[0].Min) {
			npages = int(m.Mems[0].Min)
		}
		mem = make([]byte, uint64(npages)*65536)
		for _, d := range m.Data {
			if !d.Const {
				syscall.Munmap(code)
				return nil, errors.New("jitc: non-const data offset")
			}
			if d.Off < 0 || uint64(d.Off)+uint64(len(d.Bytes)) > uint64(len(mem)) {
				syscall.Munmap(code)
				return nil, errors.New("jitc: data segment out of range")
			}
			copy(mem[d.Off:], d.Bytes)
		}
	}
	return &Runner{
		code:    code,
		mem:     mem,
		globals: globals,
		img:     img,
	}, nil
}

// Close releases the executable mapping.
func (r *Runner) Close() error { return syscall.Munmap(r.code) }

// Mem exposes linear memory for test setup/assertion.
func (r *Runner) Mem() []byte { return r.mem }

// Call runs the exported function by name with the given args.
func (r *Runner) Call(m *Module, name string, args ...uint64) (ExecResult, error) {
	var fidx uint32
	found := false
	for _, e := range m.Exports {
		if e.Name == name && e.Kind == 0 {
			fidx, found = e.Index, true
		}
	}
	if !found {
		return ExecResult{}, errors.New("jitc: export not found: " + name)
	}
	return r.CallFunc(m, fidx, args...)
}

// CallFunc runs a module-wide function index directly (used for
// non-exported functions in differential tests).
func (r *Runner) CallFunc(m *Module, fidx uint32, args ...uint64) (ExecResult, error) {
	off, ok := r.img.Funcs[fidx]
	if !ok {
		return ExecResult{}, errors.New("jitc: func has no code (import?)")
	}
	// Arity guard: CopyParams reads args[0:nparams] blindly, so a short
	// call would read out of bounds (nil R10 → SIGSEGV). Fail loudly.
	ft, err := funcTypeOf(m, fidx)
	if err != nil {
		return ExecResult{}, err
	}
	if len(args) < len(ft.Params) {
		return ExecResult{}, errors.New("jitc: too few args for call")
	}
	if len(args) > 8 {
		// Trampoline bounce area fits 8 slots (see call_amd64.s).
		return ExecResult{}, errors.New("jitc: too many args for call")
	}
	var trap uint64
	var gptr, aptr unsafe.Pointer
	if len(r.globals) > 0 {
		gptr = unsafe.Pointer(&r.globals[0])
	}
	argv := append([]uint64(nil), args...)
	if len(argv) > 0 {
		aptr = unsafe.Pointer(&argv[0])
	}
	cptr := unsafe.Pointer(&r.code[off])
	var mptr unsafe.Pointer
	if len(r.mem) > 0 {
		mptr = unsafe.Pointer(&r.mem[0])
	}
	v := execCode(cptr, mptr, uint64(len(r.mem)), &trap, gptr, aptr, len(argv))
	return ExecResult{Value: v, Trap: trap}, nil
}
