// services/pkg/pkg_fuzz_test.go — Phase 18 fuzzing for the wasm section
// parser and the signature-verification message builder.
//
// AGENTS.md practice #4: fuzz every wire-format parser. The "wire format" here
// is the wasm custom-section stream that carries abi_ver, module_name, and sig.
// A malformed or weaponised wasm binary must never crash or loop forever inside
// the guest's pkg.Verify path — it must return an error, deterministically.
package pkg

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// FuzzExtractCustomSection feeds arbitrary bytes at the wasm custom-section
// reader. The parser must never panic — it returns (nil, false) for any input
// that is not a well-formed wasm binary with a recognisable custom section.
func FuzzExtractCustomSection(f *testing.F) {
	// Seed with a valid wasm carrying abi_ver + name + sig sections.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		f.Fatal(err)
	}
	valid := buildWasmWithSections(f, "demo", 2)
	msg := append([]byte{2}, valid...)
	sig := ed25519.Sign(priv, msg)
	valid = appendCustomSection(valid, "sig", sig)
	f.Add(valid)
	f.Add(valid[:8])          // truncated to just the header
	f.Add([]byte("not wasm")) // wrong magic
	f.Add(valid[:12])         // header + partial first section

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		body, ok := extractCustomSection(data, "abi_ver")
		if ok && len(body) > 0 {
			// If we found a section, the abi_ver byte must be readable.
			_ = body[0]
		}
		_, _ = extractCustomSection(data, "sig")
		_, _ = extractCustomSection(data, "module_name")
		_, _ = extractCustomSection(data, "name")
		_, _, _ = findSectionSize(data, "sig")
		_, _, _ = findSectionSize(data, "abi_ver")
	})
	// Silence unused-variable warnings.
	_ = pub
	_ = hex.EncodeToString(pub)
}

// FuzzVerifyMessage exercises the single-allocation message builder with
// arbitrary wasm blobs. The result must never panic and must never return a
// nil slice for a non-empty input.
func FuzzVerifyMessage(f *testing.F) {
	valid := buildWasmWithSections(f, "demo", 2)
	abi := byte(2)
	f.Add(valid, abi)
	f.Add([]byte{}, byte(0))
	f.Add([]byte("garbage"), byte(1))

	// A validly signed module.
	pub, priv, _ := ed25519.GenerateKey(nil)
	_ = pub
	signed := appendCustomSection(valid, "sig", ed25519.Sign(priv, append([]byte{abi}, valid...)))
	f.Add(signed, abi)

	f.Fuzz(func(t *testing.T, data []byte, abiVer byte) {
		msg := verifyMessage(abiVer, data)
		// The first byte is always the abi_ver.
		if len(msg) > 0 && msg[0] != abiVer {
			t.Errorf("verifyMessage[0] = %d, want %d", msg[0], abiVer)
		}
		// The remainder must be the input with any "sig" section removed.
		stripped := stripSigSection(data)
		if len(msg)-1 != len(stripped) {
			t.Errorf("verifyMessage len = %d, want %d (1 + stripped len)", len(msg), 1+len(stripped))
		}
		if len(msg) > 1 {
			copy := msg[1:]
			for i := range stripped {
				if copy[i] != stripped[i] {
					t.Errorf("verifyMessage payload byte %d = %#x, want %#x", i, copy[i], stripped[i])
					break
				}
			}
		}
	})
}

// FuzzVerify exercises the full Verify pipeline: trusted-key loading + ed25519
// check on arbitrary inputs. A malicious wasm must be rejected, never crash.
func FuzzVerify(f *testing.F) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		f.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	valid := buildWasmWithSections(f, "demo", 2)
	msg := append([]byte{2}, valid...)
	sig := ed25519.Sign(priv, msg)
	valid = appendCustomSection(valid, "sig", sig)
	f.Add(valid)

	// Run enough to confirm the happy path.
	mgr := New(newMemFS(), "/boot/modules")
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	full := &Manager{fs: fs, modulesDir: "/boot/modules"}
	if err := full.Verify(valid); err != nil {
		f.Fatalf("valid module failed verify: %v", err)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Verify must never panic; it returns an error for any unsigned,
		// tampered, or malformed input.
		_ = mgr.Verify(data)
		_ = full.Verify(data)
	})
}

// FuzzStrippedSection exercises stripSigSection under arbitrary inputs. It
// must always return a slice whose length is <= the input and never panic.
func FuzzStrippedSection(f *testing.F) {
	valid := buildWasmWithSections(f, "demo", 2)
	msg := append([]byte{2}, valid...)
	_, priv, _ := ed25519.GenerateKey(nil)
	sig := ed25519.Sign(priv, msg)
	signed := appendCustomSection(valid, "sig", sig)
	f.Add(signed)
	f.Add(valid)
	f.Add([]byte("not wasm"))
	f.Add([]byte{0x00, 'a', 's', 'm', 1, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		stripped := stripSigSection(data)
		if len(stripped) > len(data) {
			t.Errorf("stripSigSection returned %d bytes, input was %d", len(stripped), len(data))
		}
	})
}
