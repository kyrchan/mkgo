package main

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/ed25519"
)

func TestMismatchCreatesUnverifiableModule(t *testing.T) {
	// Build a minimal wasm module with abi_ver=2.
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	abiVer := byte(2)
	abiBody := append(leb(len("abi_ver")), []byte("abi_ver")...)
	abiBody = append(abiBody, abiVer)
	mod = append(mod, 0x00)
	mod = append(mod, leb(len(abiBody))...)
	mod = append(mod, abiBody...)

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)

	// Sign the module with the trusted key.
	signed, err := sign(mod, priv, abiVer, "testmod")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the signed module — should pass with the matching key.
	if err := verify(signed, pub); err != nil {
		t.Fatalf("verified good module: %v", err)
	}

	// Create a mismatch module (wrong abi_ver + different key).
	stripped := stripSigSection(signed)
	modified, ok := replaceABIVer(stripped, 1)
	if !ok {
		t.Fatal("no abi_ver section found in stripped module")
	}
	_, newPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mismatched, err := sign(modified, newPriv, 1, "mismatchmod")
	if err != nil {
		t.Fatal(err)
	}

	// The mismatch module should NOT verify with the original trusted key.
	if err := verify(mismatched, pub); err == nil {
		t.Fatal("mismatched module verified — expected failure")
	}

	// Verify the abi_ver was changed to 1.
	badAbiVer, found := extractABIVer(mismatched)
	if !found {
		t.Fatal("mismatched module has no abi_ver")
	}
	if badAbiVer != 1 {
		t.Fatalf("expected abi_ver=1, got %d", badAbiVer)
	}
}

func TestReplaceABIVer(t *testing.T) {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	abiVer := byte(2)
	abiBody := append(leb(len("abi_ver")), []byte("abi_ver")...)
	abiBody = append(abiBody, abiVer)
	mod = append(mod, 0x00)
	mod = append(mod, leb(len(abiBody))...)
	mod = append(mod, abiBody...)

	result, ok := replaceABIVer(mod, 3)
	if !ok {
		t.Fatal("replaceABIVer returned false")
	}
	newVer, found := extractABIVer(result)
	if !found {
		t.Fatal("no abi_ver in result")
	}
	if newVer != 3 {
		t.Fatalf("expected abi_ver=3, got %d", newVer)
	}

	// Verify only the abi_ver byte changed.
	if !bytes.Equal(mod[:len(mod)-1], result[:len(result)-1]) {
		t.Fatal("replaceABIVer changed unexpected bytes")
	}
}

// TestTamperSigFlipsOneByte verifies that tamperSig produces a module that
// fails verification (signature mismatch) while retaining the original abi_ver.
// This isolates the tampered-signature rejection path from the abi_ver-mismatch
// path that TestMismatchCreatesUnverifiableModule covers.
func TestTamperSigFlipsOneByte(t *testing.T) {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	abiVer := byte(2)
	abiBody := append(leb(len("abi_ver")), []byte("abi_ver")...)
	abiBody = append(abiBody, abiVer)
	mod = append(mod, 0x00)
	mod = append(mod, leb(len(abiBody))...)
	mod = append(mod, abiBody...)

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)

	signed, err := sign(mod, priv, abiVer, "testmod")
	if err != nil {
		t.Fatal(err)
	}
	// The correctly signed module must verify.
	if err := verify(signed, pub); err != nil {
		t.Fatalf("signed module should verify: %v", err)
	}

	// Tamper: flip one byte in the signature, keep abi_ver = 2.
	tampered, err := tamperSig(signed)
	if err != nil {
		t.Fatal(err)
	}

	// abi_ver must be unchanged (2, not 1).
	ver, found := extractABIVer(tampered)
	if !found {
		t.Fatal("tampered module lost its abi_ver section")
	}
	if ver != 2 {
		t.Fatalf("expected abi_ver=2 after tamper, got %d", ver)
	}

	// Tampered module must NOT verify — the signature is now broken.
	if err := verify(tampered, pub); err == nil {
		t.Fatal("tampered module verified — expected signature failure")
	}

	// Only one byte should differ.
	diffCount := 0
	for i := range signed {
		if i < len(tampered) && signed[i] != tampered[i] {
			diffCount++
		}
	}
	if diffCount != 1 {
		t.Fatalf("expected exactly 1 byte difference, got %d", diffCount)
	}
}

// TestTamperSigNoSigSection verifies that tamperSig returns an error when the
// input has no signature section.
func TestTamperSigNoSigSection(t *testing.T) {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	abiBody := append(leb(len("abi_ver")), []byte("abi_ver")...)
	abiBody = append(abiBody, byte(2))
	mod = append(mod, 0x00)
	mod = append(mod, leb(len(abiBody))...)
	mod = append(mod, abiBody...)

	if _, err := tamperSig(mod); err == nil {
		t.Fatal("tamperSig on unsigned module should error")
	}
}
