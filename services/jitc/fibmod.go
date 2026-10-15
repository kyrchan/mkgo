package jitc

// fibmod.go — the canonical Stage 3b-i smoke-gate module set.
//
// v2 layout (NATIVE entry = func 0 by the Stage 3b-i convention):
//   func 0 "main": () -> i32 { i32.const 20; call 1; end }
//   func 1 "fib":  (i32) -> i32 (recursive)
// tools (cmd/fibcache) and the QEMU smoke gate consume THIS; exec tests
// build their own variants via the test-only builders in exec_test.go.

func ulebEnc(v uint32) []byte {
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

func fibSec(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, ulebEnc(uint32(len(payload)))...)
	return append(out, payload...)
}

func slebEnc(v int32) []byte {
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

func constI32(v int32) []byte {
	return append([]byte{0x41}, slebEnc(v)...)
}

// printSeq emits: iovs[0] = {ptr,len} at [64..71], then
// fd_write(1, 64, 1, 80) + drop. Net-zero stack.
func printSeq(ptr, length int32) []byte {
	var ops []byte
	ops = append(ops, constI32(64)...)
	ops = append(ops, constI32(ptr)...)
	ops = append(ops, 0x36, 0x02, 0x00) // i32.store align=2 off=0
	ops = append(ops, constI32(64)...)
	ops = append(ops, constI32(length)...)
	ops = append(ops, 0x36, 0x02, 0x04) // i32.store align=2 off=4
	ops = append(ops, constI32(1)...)   // fd
	ops = append(ops, constI32(64)...)  // iovs
	ops = append(ops, constI32(1)...)   // iovs_len
	ops = append(ops, constI32(80)...)  // nwritten
	ops = append(ops, 0x10, 0x00)       // call fd_write (func 0)
	ops = append(ops, 0x1A)             // drop
	return ops
}

// StampABIVer appends the kernel's `abi_ver` custom section (same
// layout as scripts/add_abiver.py: section id 0 + size + name +
// version byte) so hand-built modules pass engine_init's abi_ver gate.
func StampABIVer(wasm []byte, ver byte) []byte {
	name := []byte("abi_ver")
	body := append(ulebEnc(uint32(len(name))), name...)
	body = append(body, ver)
	out := append([]byte(nil), wasm...)
	out = append(out, 0x00)
	out = append(out, ulebEnc(uint32(len(body)))...)
	return append(out, body...)
}

func fibBodyWrap(localsAndOps []byte) []byte {
	out := ulebEnc(uint32(len(localsAndOps)))
	return append(out, localsAndOps...)
}

// FibModule returns the fib.wasm bytes (Stage 3b-ii v3):
// one fd_write import, main + fib, one memory page, two data segments
// ("NATIVE-OK\n" @100, "NATIVE-BAD\n" @120). main computes fib(20),
// prints OK iff it equals 6765, and returns the comparison. All
// imports resolve to the FD_WRITE kind (compilable by construction).
func FibModule() []byte {
	// main body (no locals):
	//   i32.const 20; call 2; i32.const 6765; i32.eq
	//   if (void) { print OK } else { print BAD } end; end
	var main []byte
	main = append(main, 0x00) // no locals
	main = append(main, constI32(20)...)
	main = append(main, 0x10, 0x02) // call fib (func 2)
	main = append(main, constI32(6765)...)
	main = append(main, 0x46)       // i32.eq
	main = append(main, 0x04, 0x40) // if void
	main = append(main, printSeq(100, 10)...)
	main = append(main, 0x05) // else
	main = append(main, printSeq(120, 11)...)
	main = append(main, 0x0B) // end if
	// leave the eq result as the return value? No — consumed by if.
	// Return fib(20) again for the exit code: recompute cheaply.
	main = append(main, constI32(20)...)
	main = append(main, 0x10, 0x02) // call fib
	main = append(main, 0x0B)       // end func
	// fib body (func 2; recursive calls target 2, not 1).
	fibBody := []byte{
		0x20, 0x00, 0x41, 0x02, 0x48,
		0x04, 0x7F, 0x20, 0x00, 0x05,
		0x20, 0x00, 0x41, 0x01, 0x6B, 0x10, 0x02,
		0x20, 0x00, 0x41, 0x02, 0x6B, 0x10, 0x02,
		0x6A, 0x0B, 0x0B,
	}
	fibBody = fibBodyWrap(append([]byte{0x00}, fibBody...))
	mainBody := fibBodyWrap(main)
	code := append(ulebEnc(2), mainBody...)
	code = append(code, fibBody...)
	out := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00}
	// type section: [()->(i32)], [(i32)->(i32)], [(i32×4)->(i32)].
	out = append(out, fibSec(1, []byte{
		0x03, 0x60, 0x00, 0x01, 0x7F,
		0x60, 0x01, 0x7F, 0x01, 0x7F,
		0x60, 0x04, 0x7F, 0x7F, 0x7F, 0x7F, 0x01, 0x7F,
	})...)
	// import section: wasi_snapshot_preview1.fd_write func type 2.
	mod := []byte("wasi_snapshot_preview1")
	nm := []byte("fd_write")
	var imp []byte
	imp = append(imp, ulebEnc(1)...)
	imp = append(imp, ulebEnc(uint32(len(mod)))...)
	imp = append(imp, mod...)
	imp = append(imp, ulebEnc(uint32(len(nm)))...)
	imp = append(imp, nm...)
	imp = append(imp, 0x00, 0x02)
	out = append(out, fibSec(2, imp)...)
	// func section: main->type 0, fib->type 1.
	out = append(out, fibSec(3, []byte{0x02, 0x00, 0x01})...)
	// memory section: min 1 page.
	out = append(out, fibSec(5, []byte{0x01, 0x00, 0x01})...)
	// export "main" func 1 and "_start" func 1 (engine_start enters
	// via _start; both names address the same function).
	out = append(out, fibSec(7, []byte{0x02, 0x04, 'm', 'a', 'i', 'n', 0x00, 0x01,
		0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x01})...)
	out = append(out, fibSec(10, code)...)
	// data section: "NATIVE-OK\n" @100, "NATIVE-BAD\n" @120.
	okMsg := []byte("NATIVE-OK\n")
	badMsg := []byte("NATIVE-BAD\n")
	var data []byte
	data = append(data, ulebEnc(2)...)
	data = append(data, 0x00) // memidx 0
	data = append(data, 0x41)
	data = append(data, slebEnc(100)...)
	data = append(data, 0x0B)
	data = append(data, ulebEnc(uint32(len(okMsg)))...)
	data = append(data, okMsg...)
	data = append(data, 0x00)
	data = append(data, 0x41)
	data = append(data, slebEnc(120)...)
	data = append(data, 0x0B)
	data = append(data, ulebEnc(uint32(len(badMsg)))...)
	data = append(data, badMsg...)
	out = append(out, fibSec(11, data)...)
	return StampABIVer(out, 2)
}
