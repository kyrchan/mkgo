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
func buildWasmWithSections(t *testing.T, name string, abiVer byte) []byte {
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
	section := append(leb(len(name)), []byte(name)...)
	section = append(section, payload...)
	buf = append(buf, 0)
	buf = append(buf, leb(len(section))...)
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
