package main

import "testing"

// FuzzFindAbiVer stresses the wasm section parser (findAbiVer + readLEB)
// against arbitrary byte input. It locks the two critical modulesign
// invariants that the hand-written tests pin down only for fixed seeds:
//
//  1. findAbiVer never panics on any input. Malformed wasm must be rejected
//     with ok=false, never a crash — a panic here would take down
//     "module verify" (ABI §1) and the build-time stamping path.
//  2. When found=true, the stripped output contains ZERO abi_ver sections.
//     This is the "re-stamps REPLACES, not appends" property (M3 task #3,
//     README §"Module signer"): a module re-stamped today must carry exactly
//     one abi_ver section, so the guest reader — which takes the FIRST —
//     always sees the fresh version and never a stale duplicate left by
//     scripts/add_abiver.py's blind append.
//  3. Re-stamping the stripped output and reading it back is stable:
//     exactly one section, version matches.
//
// Practice #2 (no fix without a failing test): if a random input violates
// (2), the failing corpus becomes the regression seed that must be green
// before re-stamp can ship.
func FuzzFindAbiVer(f *testing.F) {
	f.Add(minimalWasm())
	f.Add(wasmWithAbiVer(2))
	{
		dup := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
		f.Add(append(dup, customSection("abi_ver", []byte{5})...))
	}
	f.Add(append(minimalWasm(), customSection("abi_ver", []byte{42})...))
	f.Add([]byte("not wasm at all"))
	f.Add([]byte{0x00, 0x61, 0x73, 0x6d}) // magic only, no version
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, data []byte) {
		ver, stripped, ok := findAbiVer(data)
		if !ok {
			return
		}
		// (2): all abi_ver sections must be gone from the stripped output.
		if c := countAbiVerSections(t, stripped); c != 0 {
			t.Fatalf("found=true but %d abi_ver sections remain after strip", c)
		}
		// (3): re-stamp -> exactly one section, version matches, readable back.
		out := append(stripped, customSection("abi_ver", []byte{byte(ver)})...)
		if c := countAbiVerSections(t, out); c != 1 {
			t.Fatalf("re-stamp produced %d abi_ver sections, want 1", c)
		}
		rv, _, rok := findAbiVer(out)
		if !rok || rv != ver {
			t.Fatalf("re-stamp round-trip: ver=%d ok=%v, want %d/true", rv, rok, ver)
		}
	})
}

// FuzzReadLEB verifies the LEB128 decoder is total (never panics) and that
// leb/readLEB form a consistent round-trip for every decodable byte prefix.
func FuzzReadLEB(f *testing.F) {
	f.Add([]byte{0x00})             // 0
	f.Add([]byte{0x7f})             // 127
	f.Add([]byte{0x80, 0x01})       // 128
	f.Add([]byte{0xff, 0xff, 0x03}) // 16383
	f.Add([]byte(nil))

	f.Fuzz(func(t *testing.T, data []byte) {
		v, n, ok := readLEB(data)
		if !ok {
			return
		}
		if n <= 0 || n > 10 || n > len(data) {
			t.Fatalf("readLEB consumed %d bytes from %d-byte input", n, len(data))
		}
		enc := leb(v)
		dv, dn, dok := readLEB(enc)
		if !dok || dv != v || dn != len(enc) {
			t.Fatalf("LEB round-trip failed: got %d (%d bytes) want %d (%d bytes)",
				dv, dn, v, len(enc))
		}
	})
}
