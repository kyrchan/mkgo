// services/pkg/pkg_test.go -- Phase 18 package management tests.
//
// Covers the host-testable core: signature verification, install, list,
// remove. Uses an in-memory FS so tests run without a real fs.wasm.
package pkg

import (
	"crypto/ed25519"
	"encoding/hex"
	"sort"
	"strings"
	"testing"

	lib "kernel.lane/guests/lib"
)

// memFS is an in-memory FS implementation for testing.
type memFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMemFS() *memFS {
	return &memFS{
		files: map[string][]byte{},
		dirs:  map[string]bool{"/": true, "/etc": true, "/boot": true, "/boot/modules": true},
	}
}

func (m *memFS) ensureDir(p string) {
	m.dirs[p] = true
}

// ensureParent creates the parent directories of p without adding p itself.
func (m *memFS) ensureParent(p string) {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return
	}
	dir := p[:idx]
	for dir != "" && dir != "/" {
		m.dirs[dir] = true
		idx := strings.LastIndex(dir, "/")
		if idx <= 0 {
			break
		}
		dir = dir[:idx]
	}
}

func (m *memFS) List(p string) ([]lib.FileInfo, error) {
	if !m.dirs[p] {
		return nil, &fsErr{"not a directory: " + p}
	}
	var out []lib.FileInfo
	prefix := p
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	seen := map[string]bool{}
	for f := range m.files {
		if !strings.HasPrefix(f, prefix) {
			continue
		}
		rest := f[len(prefix):]
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
			if seen[rest] {
				continue
			}
			seen[rest] = true
			out = append(out, lib.FileInfo{Name: rest, Attr: lib.AttrDir, Size: 0})
		} else {
			out = append(out, lib.FileInfo{Name: rest, Attr: 0, Size: uint32(len(m.files[f]))})
		}
	}
	return out, nil
}

func (m *memFS) ReadFile(p string, off uint64, buf []byte) (int, error) {
	data, ok := m.files[p]
	if !ok {
		return 0, &fsErr{"no such file: " + p}
	}
	if off >= uint64(len(data)) {
		return 0, nil
	}
	n := copy(buf, data[off:])
	return n, nil
}

func (m *memFS) WriteFile(p string, off uint64, data []byte) (int, error) {
	m.ensureParent(p)
	cur := m.files[p]
	end := int(off) + len(data)
	if end > len(cur) {
		new := make([]byte, end)
		copy(new, cur)
		cur = new
	}
	copy(cur[off:], data)
	m.files[p] = cur
	return len(data), nil
}

func (m *memFS) Create(p string) error {
	m.ensureParent(p)
	m.files[p] = []byte{}
	return nil
}

func (m *memFS) Delete(p string) error {
	if _, ok := m.files[p]; !ok {
		return &fsErr{"no such file: " + p}
	}
	delete(m.files, p)
	return nil
}

func (m *memFS) Stat(p string) (lib.FileInfo, error) {
	if m.dirs[p] {
		return lib.FileInfo{Name: p, Attr: lib.AttrDir, Size: 0}, nil
	}
	data, ok := m.files[p]
	if !ok {
		return lib.FileInfo{}, &fsErr{"no such file: " + p}
	}
	return lib.FileInfo{Name: p, Attr: 0, Size: uint32(len(data))}, nil
}

func (m *memFS) setFile(p string, data []byte) {
	m.ensureParent(p)
	m.files[p] = data
}

type fsErr struct{ msg string }

func (e *fsErr) Error() string { return e.msg }

// --- test helpers ---

// makeSignedWasm builds a minimal wasm with abi_ver, name, and sig sections
// signed with the given private key. The wasm body is a header + one byte.
func makeSignedWasm(t *testing.T, name string, abiVer byte, priv ed25519.PrivateKey) []byte {
	t.Helper()
	wasm := buildWasmWithSections(t, name, abiVer)
	// Sign [abi_ver_byte || wasm_without_sig]
	stripped := stripSigSection(wasm)
	msg := append([]byte{abiVer}, stripped...)
	sig := ed25519.Sign(priv, msg)
	return appendCustomSection(wasm, "sig", sig)
}

// buildWasmWithSections creates a minimal wasm with abi_ver and name
// custom sections.
func buildWasmWithSections(t testing.TB, name string, abiVer byte) []byte {
	t.Helper()
	// minimal wasm: magic + version + one section (type)
	wasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	// abi_ver section
	wasm = appendCustomSection(wasm, "abi_ver", []byte{abiVer})
	// name section
	wasm = appendCustomSection(wasm, "name", []byte(name))
	return wasm
}

// appendCustomSection mirrors the host tool's writer.
func appendCustomSection(buf []byte, name string, payload []byte) []byte {
	section := append(Leb(len(name)), []byte(name)...)
	section = append(section, payload...)
	buf = append(buf, 0)
	buf = append(buf, Leb(len(section))...)
	buf = append(buf, section...)
	return buf
}

func TestPhase18VerifyValid(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)+"\n"))
	m := New(fs, "/boot/modules")
	if m.TrustedKeyCount() != 1 {
		t.Fatalf("want 1 key, got %d", m.TrustedKeyCount())
	}
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if err := m.Verify(wasm); err != nil {
		t.Errorf("verify valid: %v", err)
	}
}

// TestPhase18VerifyMismatchedABIAndKey exercises the exact -mismatch tool
// behaviour using only the exported API: stripSigSection removes the sig,
// mismatchABI increments the abi_ver payload byte, and sign re-signs with a
// fresh key. The resulting module must fail verification against the original
// trusted key, and its abi_ver must read back as 1.
func TestPhase18VerifyMismatchedABIAndKey(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")

	// Build a valid module with abi_ver=2, signed with the trusted key.
	good := makeSignedWasm(t, "hello", 2, priv)

	// Reproduce -mismatch using exported helpers only.
	mismatched := mismatchABI(good, 1)

	// abi_ver must read back as 1.
	if v := ABIVer(mismatched); v != 1 {
		t.Fatalf("ABIVer = %d, want 1", v)
	}

	// Must FAIL verification with the original trusted key.
	if err := m.Verify(mismatched); err == nil {
		t.Fatal("mismatched module verified — expected failure")
	}
}

// mismatchABI strips the sig section from a signed wasm and sets the abi_ver
// payload byte to newVer. Uses only exported pkg functions. The caller is
// responsible for re-signing if it needs a valid signature on the result.
func mismatchABI(wasm []byte, newVer byte) []byte {
	stripped := stripSigSection(wasm)
	// In the test fixture layout (name section first, abi_ver second) the
	// abi_ver payload byte is at a fixed offset (18) from the section start.
	// We locate it by scanning for the b"abi_ver" section name to stay correct
	// if the fixture layout changes.
	out := make([]byte, len(stripped))
	copy(out, stripped)
	if idx := findABIPayloadOffset(out); idx >= 0 && idx < len(out) {
		out[idx] = newVer
	}
	return out
}

// findABIPayloadOffset returns the index of the abi_ver payload byte within
// a wasm binary that carries abi_ver and name custom sections in that order.
// Returns -1 if not found.
func findABIPayloadOffset(data []byte) int {
	if len(data) < 8 || string(data[:4]) != "\x00asm" {
		return -1
	}
	i := 8
	for i < len(data) {
		if i >= len(data) {
			return -1
		}
		id := data[i]
		i++
		size, ok := readLEB128(data, &i)
		if !ok || i+size > len(data) {
			return -1
		}
		if id == 0 {
			sec := data[i : i+size]
			nameLen, j := readLEB128Full(sec)
			if j+nameLen <= len(sec) && string(sec[j:j+nameLen]) == "abi_ver" {
				return i + j + nameLen
			}
		}
		i += size
	}
	return -1
}

func TestPhase18VerifyTampered(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	// Tamper: flip a byte in the wasm body
	wasm[10] ^= 0xff
	if err := m.Verify(wasm); err == nil {
		t.Errorf("tampered wasm should fail verify")
	}
}

// TestPhase18VerifyTamperedSignature is the tampered-signature-only case:
// abi_ver=2 is correct, the signature was produced with the trusted key, but
// one byte of the signature payload is flipped. This is distinct from
// TestPhase18VerifyMismatchedABIAndKey (which also changes abi_ver) and
// isolates the pure signature-rejection path that the -tamper gate module
// exercises. Verify must fail while ABIVer still reads 2.
func TestPhase18VerifyTamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")

	wasm := makeSignedWasm(t, "hello", 2, priv)

	// Flip one byte inside the sig custom section payload.
	tampered := flipSigByte(t, wasm)

	// abi_ver must be unchanged: 2.
	if v := ABIVer(tampered); v != 2 {
		t.Fatalf("ABIVer = %d, want 2 (tamper was sig-only)", v)
	}

	// Verify must reject the tampered signature.
	if err := m.Verify(tampered); err == nil {
		t.Fatal("tampered-signature module verified — expected failure")
	}
	// Ensure only one byte changed.
	diffs := 0
	for i := range wasm {
		if i < len(tampered) && wasm[i] != tampered[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Fatalf("expected exactly 1 byte difference, got %d", diffs)
	}
}

// flipSigByte returns a copy of wasm with the first byte of the "sig" custom
// section payload toggled. It scans the section table to locate the payload,
// so it does not depend on fixture layout assumptions.
func flipSigByte(t *testing.T, wasm []byte) []byte {
	t.Helper()
	out := make([]byte, len(wasm))
	copy(out, wasm)
	off := findSectionPayloadOffset(out, "sig")
	if off < 0 || off >= len(out) {
		t.Fatalf("could not locate sig section payload in wasm")
	}
	out[off] ^= 0x01
	return out
}

// findSectionPayloadOffset returns the byte index of the payload of the
// custom section named `name` within a wasm binary, or -1 if not found.
func findSectionPayloadOffset(data []byte, name string) int {
	if len(data) < 8 || string(data[:4]) != "\x00asm" {
		return -1
	}
	i := 8
	for i < len(data) {
		id := data[i]
		i++
		size, ok := readLEB128(data, &i)
		if !ok || i+size > len(data) {
			return -1
		}
		body := data[i : i+size]
		if id == 0 {
			nameLen, j := readLEB128Full(body)
			if j > 0 && j+nameLen <= len(body) && string(body[j:j+nameLen]) == name {
				return i + j + nameLen
			}
		}
		i += size
	}
	return -1
}

func TestPhase18VerifyWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(otherPub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if err := m.Verify(wasm); err == nil {
		t.Errorf("wrong-key wasm should fail verify")
	}
}

func TestPhase18VerifyNoTrustedKeys(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if err := m.Verify(wasm); err == nil {
		t.Errorf("no trusted keys should fail verify")
	}
}

func TestPhase18Install(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	name, err := m.Install(wasm)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if name != "hello" {
		t.Errorf("install name = %q, want hello", name)
	}
	// Verify the file is on disk
	st, err := fs.Stat("/boot/modules/hello.wasm")
	if err != nil {
		t.Fatalf("stat installed: %v", err)
	}
	if st.Size != uint32(len(wasm)) {
		t.Errorf("installed size = %d, want %d", st.Size, len(wasm))
	}
}

func TestPhase18InstallRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	// No trusted keys
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if _, err := m.Install(wasm); err == nil {
		t.Errorf("install without trusted keys should fail")
	}
	if _, ok := fs.files["/boot/modules/hello.wasm"]; ok {
		t.Errorf("rejected install should not write file")
	}
}

func TestPhase18List(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	for _, name := range []string{"hello", "world", "foo"} {
		wasm := makeSignedWasm(t, name, 2, priv)
		if _, err := m.Install(wasm); err != nil {
			t.Fatalf("install %s: %v", name, err)
		}
	}
	// Add an unrelated file
	fs.setFile("/boot/modules/README.txt", []byte("hi"))
	mods, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	sort.Strings(mods)
	want := []string{"foo", "hello", "world"}
	if len(mods) != len(want) {
		t.Fatalf("list = %v, want %v", mods, want)
	}
	for i := range want {
		if mods[i] != want[i] {
			t.Errorf("list[%d] = %q, want %q", i, mods[i], want[i])
		}
	}
}

func TestPhase18Remove(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if _, err := m.Install(wasm); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("hello"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	mods, _ := m.List()
	if len(mods) != 0 {
		t.Errorf("after remove, list = %v, want empty", mods)
	}
}

func TestPhase18Update(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if _, err := m.Install(wasm); err != nil {
		t.Fatal(err)
	}
	// Update with a fresh build (different body, same name, valid signature).
	updated := makeSignedWasm(t, "hello", 2, priv)
	name, err := m.Update(updated)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if name != "hello" {
		t.Errorf("update name = %q, want hello", name)
	}
	// Verify the installed file is the updated one.
	st, err := fs.Stat("/boot/modules/hello.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if st.Size != uint32(len(updated)) {
		t.Errorf("installed size = %d, want %d (updated)", st.Size, len(updated))
	}
}

func TestPhase18UpdateNotInstalled(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	// Module not in /boot/modules yet — Update must reject.
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if _, err := m.Update(wasm); err == nil {
		t.Error("update of uninstalled module should fail")
	}
}

func TestPhase18UpdateRejectedBySig(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	m := New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if _, err := m.Install(wasm); err != nil {
		t.Fatal(err)
	}
	// Tamper: flip a byte in the signature section.
	tampered := make([]byte, len(wasm))
	copy(tampered, wasm)
	// Find the 'sig' section body and flip a byte there.
	sigStart, sigTotal, found := findSectionSize(tampered, "sig")
	if !found || sigTotal <= 64 {
		t.Fatal("sig section not found or too small for tampering")
	}
	sigPayloadStart := sigStart + sigTotal - 64 // last 64 bytes are the ed25519 sig
	if sigPayloadStart > 0 && sigPayloadStart < len(tampered) {
		tampered[sigPayloadStart] ^= 0x01
	}
	if _, err := m.Update(tampered); err == nil {
		t.Error("update with tampered signature should fail verification")
	}
}

func TestPhase18ABIVerExtraction(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	fs.setFile("/etc/trusted", []byte(hex.EncodeToString(pub)))
	_ = New(fs, "/boot/modules")
	wasm := makeSignedWasm(t, "hello", 2, priv)
	if v := ABIVer(wasm); v != 2 {
		t.Errorf("ABIVer = %d, want 2", v)
	}
	if n := ModuleName(wasm); n != "hello" {
		t.Errorf("ModuleName = %q, want hello", n)
	}
}

// TestPhase18ModuleNameCustomSection verifies that ModuleName prefers the
// "module_name" custom section over the legacy "name" section when present.
func TestPhase18ModuleNameCustomSection(t *testing.T) {
	wasm := buildWasmWithSections(t, "", 2)
	wasm = appendCustomSection(wasm, "module_name", []byte("hello"))
	wasm = appendCustomSection(wasm, "name", []byte("legacy_blob_with_function_names"))
	if n := ModuleName(wasm); n != "hello" {
		t.Errorf("ModuleName with module_name section = %q, want hello", n)
	}
}

// TestPhase18ModuleNameFallback verifies that ModuleName falls back to the
// "name" custom section when no "module_name" section is present.
func TestPhase18ModuleNameFallback(t *testing.T) {
	wasm := buildWasmWithSections(t, "hello", 2)
	if n := ModuleName(wasm); n != "hello" {
		t.Errorf("ModuleName fallback to name = %q, want hello", n)
	}
}

func TestPhase18TrustedKeyLoading(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub, _, _ := ed25519.GenerateKey(nil)
	fs := newMemFS()
	content := "# trusted public keys\n" +
		hex.EncodeToString(pub) + "\n" +
		"\n" +
		"not-hex-data\n" +
		hex.EncodeToString(priv) + "\n" // wrong size, skipped
	fs.setFile("/etc/trusted", []byte(content))
	m := New(fs, "/boot/modules")
	if m.TrustedKeyCount() != 1 {
		t.Errorf("want 1 valid key, got %d", m.TrustedKeyCount())
	}
}
