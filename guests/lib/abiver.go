package kern

import "errors"

// abi_ver.go — the READER for the `abi_ver` custom section that
// scripts/add_abiver.py writes. The writer has existed since Phase 7 and
// nothing could read it back, so "module verify" (ABI §1 module-update rule)
// had no implementation to test against.
//
// Custom section layout (the writer's, unchanged):
//
//	0x00  section id 0 (custom)
//	     LEB128 body length
//	     body = LEB128 name length, name bytes, payload
//
// The payload is a single version byte. The kernel refuses to instantiate a
// module whose embedded version differs from its own, so this reader is what
// lets a guest explain *why* a module would be refused rather than only
// observing the refusal.

var (
	ErrNotWasm       = errors.New("not a wasm module")
	ErrAbiVerMissing = errors.New("no abi_ver custom section")
	ErrAbiVerTrunc   = errors.New("wasm section runs past end of module")
)

// leb128 decodes one unsigned LEB128 value, returning it and how many bytes
// it occupied. A truncated or over-long encoding is an error rather than a
// silently wrong number: this parses untrusted module bytes.
func leb128(b []byte) (uint64, int, error) {
	var v uint64
	var shift uint
	for i := 0; i < len(b); i++ {
		if i >= 10 {
			return 0, 0, ErrAbiVerTrunc // uint64 is at most 10 LEB bytes
		}
		c := b[i]
		v |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, ErrAbiVerTrunc
}

// ReadAbiVer returns the `abi_ver` version embedded in a module. ok is false
// when the module is well formed but carries no such section, which is a
// different answer from "malformed".
func ReadAbiVer(wasm []byte) (ver int, ok bool, err error) {
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		return 0, false, ErrNotWasm
	}
	for off := 8; off < len(wasm); {
		id := wasm[off]
		off++
		size, n, err := leb128(wasm[off:])
		if err != nil {
			return 0, false, err
		}
		off += n
		if uint64(len(wasm)-off) < size {
			return 0, false, ErrAbiVerTrunc
		}
		body := wasm[off : off+int(size)]
		off += int(size)
		if id != 0 {
			continue // not a custom section
		}
		nameLen, n2, err := leb128(body)
		if err != nil {
			return 0, false, err
		}
		if uint64(len(body)-n2) < nameLen {
			return 0, false, ErrAbiVerTrunc
		}
		if string(body[n2:n2+int(nameLen)]) != "abi_ver" {
			continue // some other custom section
		}
		payload := body[n2+int(nameLen):]
		if len(payload) < 1 {
			return 0, false, ErrAbiVerTrunc
		}
		return int(payload[0]), true, nil
	}
	return 0, false, nil
}

// AbiVerStamp is the version this kernel stamps into and accepts. It is a
// constant here because the guest cannot ask the kernel "what do you
// accept?" — the kernel simply refuses a mismatch. Exported so the shell can
// print both numbers in one line rather than making the user remember.
const AbiVerStamp = 2
