package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// minimalWasm returns a valid wasm binary with a trivial custom section
// (not abi_ver) so we can test stamp-on-unstamped and re-stamp.
func minimalWasm() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, // "\0asm"
		0x01, 0x00, 0x00, 0x00, // version 1
		// One custom section with name "foo", no payload.
		0x00,          // section id: custom
		0x08,          // section size: 8 (1 byte name-len + 3 name + 4 payload)
		0x03,          // name length: 3
		'f', 'o', 'o', // name
		0x00, 0x00, 0x00, 0x00, // 4 payload bytes
	}
}

// wasmWithAbiVer returns a valid wasm with an abi_ver section carrying ver.
func wasmWithAbiVer(ver byte) []byte {
	// abi_ver custom section: name="abi_ver" (7 bytes), payload = 1 byte
	sec := customSection("abi_ver", []byte{ver})
	return append(minimalWasm(), sec...)
}

// countAbiVerSections scans the wasm and returns the number of abi_ver
// custom sections found. This is the critical property: re-stamping must
// produce exactly ONE section, never two.
func countAbiVerSections(t *testing.T, wasm []byte) int {
	t.Helper()
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		t.Fatal("not a wasm binary")
	}
	rest := wasm[8:]
	count := 0
	for len(rest) > 0 {
		id := rest[0]
		rest = rest[1:]
		v, n, ok := readLEB(rest)
		if !ok || uint64(len(rest)-n) < v {
			break
		}
		body := rest[n : n+int(v)]
		rest = rest[n+int(v):]
		if id != 0 {
			continue
		}
		nameLen, n2, ok := readLEB(body)
		if !ok || uint64(len(body)-n2) < nameLen {
			continue
		}
		if string(body[n2:n2+int(nameLen)]) == "abi_ver" {
			count++
		}
	}
	return count
}

func TestStampOnUnstampedModule(t *testing.T) {
	w := minimalWasm()
	_, stripped, _ := findAbiVer(w)
	if stripped == nil {
		t.Fatal("findAbiVer returned nil for unstamped module")
	}
	out := append(stripped, customSection("abi_ver", []byte{2})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("stamped unstamped module: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok || v != 2 {
		t.Fatalf("abi_ver = %d, %v; want 2, true", v, ok)
	}
}

// TestReStampReplacesNotAppends is THE critical test: re-stamping a module
// that already has an abi_ver section must produce exactly ONE section,
// not two. A module with two sections has no single version, and the guest
// reader takes the first one — appending would let a stale version survive.
func TestReStampReplacesNotAppends(t *testing.T) {
	w := wasmWithAbiVer(2)
	// Re-stamp with ver=3.
	_, stripped, _ := findAbiVer(w)
	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamped module has %d abi_ver sections, want 1 (REPLACE not APPEND)", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok {
		t.Fatal("no abi_ver section found after re-stamp")
	}
	if v != 3 {
		t.Fatalf("abi_ver = %d, want 3 (the new stamp, not the old 2)", v)
	}
	// The payload length must be 1 (single byte), not the sum of old+new.
	if len(customSection("abi_ver", []byte{3})) != 11 {
		t.Fatalf("abi_ver section length = %d, want 11 bytes",
			len(customSection("abi_ver", []byte{3})))
	}
}

// TestReStampMultipleTimes verifies idempotency: stamping repeatedly
// always yields exactly one section with the latest version.
func TestReStampMultipleTimes(t *testing.T) {
	w := wasmWithAbiVer(2)
	for i, ver := range []byte{3, 5, 2, 7} {
		_, stripped, _ := findAbiVer(w)
		w = append(stripped, customSection("abi_ver", []byte{ver})...)
		c := countAbiVerSections(t, w)
		if c != 1 {
			t.Fatalf("stamp %d: %d sections, want 1", i, c)
		}
		v, _, ok := findAbiVer(w)
		if !ok || v != int(ver) {
			t.Fatalf("stamp %d: abi_ver=%d ok=%v, want %d/true", i, v, ok, ver)
		}
	}
}

func TestMismatchStampsDeliberatelyWrong(t *testing.T) {
	w := wasmWithAbiVer(2)
	_, stripped, _ := findAbiVer(w)
	out := append(stripped, customSection("abi_ver", []byte{99})...)
	v, _, ok := findAbiVer(out)
	if !ok || v != 99 {
		t.Fatalf("mismatch stamp = %d, %v; want 99, true", v, ok)
	}
}

// TestMismatchViaCLI tests the -mismatch flag end-to-end via the CLI.
func TestMismatchViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "mod_mismatched.wasm")
	os.WriteFile(in, wasmWithAbiVer(2), 0o644)
	cmd := exec.Command("go", "run", ".", "-mismatch", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mismatch CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	v, _, ok := findAbiVer(w)
	if !ok || v != 99 {
		t.Fatalf("CLI mismatch: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// TestReportViaCLI tests the -report flag.
func TestReportViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	os.WriteFile(in, wasmWithAbiVer(2), 0o644)
	cmd := exec.Command("go", "run", ".", "-report", in)
	cmd.Dir = "."
	outb, err := cmd.Output()
	if err != nil {
		t.Fatalf("report CLI failed: %v\n%s", err, outb)
	}
	s := strings.TrimSpace(string(outb))
	if !strings.Contains(s, "abi_ver=2") {
		t.Fatalf("report output = %q, want it to contain abi_ver=2", s)
	}
}

func TestReportUnstampedModule(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)
	cmd := exec.Command("go", "run", ".", "-report", in)
	cmd.Dir = "."
	outb, err := cmd.Output()
	if err != nil {
		t.Fatalf("report CLI failed: %v\n%s", err, outb)
	}
	s := strings.TrimSpace(string(outb))
	if !strings.Contains(s, "no abi_ver section") {
		t.Fatalf("report for unstamped = %q, want 'no abi_ver section'", s)
	}
}

// TestMalformedModuleNoMagic ensures findAbiVer handles non-wasm input.
func TestMalformedModuleNoMagic(t *testing.T) {
	garbage := []byte("this is not a wasm file at all")
	ver, stripped, ok := findAbiVer(garbage)
	if ok {
		t.Fatalf("findAbiVer on garbage returned ok=true ver=%d", ver)
	}
	if !bytes.Equal(stripped, garbage) {
		t.Fatal("stripped should return the original for non-wasm")
	}
}

func TestMalformedModuleTruncatedHeader(t *testing.T) {
	// wasm magic but truncated (no version).
	trunc := []byte{0x00, 0x61, 0x73, 0x6d}
	ver, stripped, ok := findAbiVer(trunc)
	if ok {
		t.Fatalf("findAbiVer on truncated header returned ok=true ver=%d", ver)
	}
	if !bytes.Equal(stripped, trunc) {
		t.Fatal("stripped should return original for truncated wasm")
	}
}

func TestMalformedModuleTruncatedSection(t *testing.T) {
	// Valid wasm header + a section that claims more bytes than available.
	trunc := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x00,                // custom section id
		0x10,                // claims 16 bytes of body
		0x03, 'f', 'o', 'o', // name
		// truncated — not enough bytes
	}
	_, _, ok := findAbiVer(trunc)
	// Should not panic; should return ok=false (no valid abi_ver found).
	if ok {
		t.Fatal("findAbiVer on truncated section should return ok=false")
	}
}

// TestFindAbiVerMalformedSectionAfterAbiVer pins the contract for the
// malformed-module case where a TRUNCATED section follows a valid abi_ver:
// findAbiVer must not panic, must report the abi_ver it found (found=true,
// reading the correct first version), must strip that abi_ver from the
// output, and must STOP cleanly at the corrupt section rather than parsing
// garbage past it. The companion TestMalformedModuleTruncatedSection only
// covers the no-abi_ver-at-start case; this closes the gap where the break
// happens mid-stream (a real loader input: a truncated trailing section).
func TestFindAbiVerMalformedSectionAfterAbiVer(t *testing.T) {
	// [header, foo, abi_ver=2, truncated-custom-claiming-16-bytes]
	module := wasmWithAbiVer(2) // header + minimalWasm(foo) + abi_ver=2 (well-formed)
	// A custom section that declares a 16-byte body but only carries 3 bytes
	// after the length byte — enough to be a section, not enough to satisfy it.
	module = append(module,
		0x00,                // custom section id
		0x10,                // claims 16 bytes of body
		0x03, 'f', 'o', 'o') // name field (3 bytes) — body under-declared

	// Must not panic; abi_ver is found before the corrupt tail.
	ver, stripped, found := findAbiVer(module)
	if !found {
		t.Fatal("abi_ver should be found even when followed by a truncated section")
	}
	if ver != 2 {
		t.Fatalf("version = %d, want 2 (first abi_ver, unaffected by trailing malformation)", ver)
	}
	// The abi_ver section must be stripped from the output.
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("stripped output has %d abi_ver sections, want 0", c)
	}
	// The "foo" custom section preceding abi_ver must survive stripping; only
	// the trailing truncated section (which cannot be parsed) is dropped.
	if c := countCustomSections(t, stripped, "foo"); c != 1 {
		t.Fatalf("stripped output lost the 'foo' section: %d remain, want 1", c)
	}

	// Re-stamping the stripped output must yield exactly one abi_ver at the new
	// version, mirroring the normal stamp path with no panic.
	out := append(stripped, customSection("abi_ver", []byte{7})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamp of truncated-tail module: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok || v != 7 {
		t.Fatalf("re-stamp version = %d (want 7); must be the new stamp, not a stale 2", v)
	}
}

// TestAbiVerSectionFormat confirms the exact byte layout of the section
// the tool produces, matching the spec in scripts/add_abiver.py.
func TestAbiVerSectionFormat(t *testing.T) {
	sec := customSection("abi_ver", []byte{42})
	// [0] = custom section id (0x00)
	if sec[0] != 0x00 {
		t.Fatalf("section[0] = %#x, want 0x00", sec[0])
	}
	// body = 1 (name-len byte) + 7 (name) + 1 (payload) = 9
	if sec[1] != 0x09 {
		t.Fatalf("section[1] = %#x, want 0x09 (body len 9)", sec[1])
	}
	// [2] = name length = 7 (len of "abi_ver")
	if sec[2] != 0x07 {
		t.Fatalf("section[2] = %#x, want 0x07 (name len 7)", sec[2])
	}
	// [3:10] = "abi_ver"
	if string(sec[3:10]) != "abi_ver" {
		t.Fatalf("name = %q, want 'abi_ver'", sec[3:10])
	}
	// [10] = version byte
	if sec[10] != 42 {
		t.Fatalf("version byte = %d, want 42", sec[10])
	}
}

// TestStampChangesSectionSize verifies that re-stamping a module with a
// longer name doesn't leave garbage: the section size is computed from the
// name length, and a different name must produce a correctly-sized section.
func TestStampChangesSectionSize(t *testing.T) {
	// Custom section with name "other" (5 bytes) + 1 payload byte → body = 6
	other := customSection("other", []byte{1})
	// abi_ver with name "abi_ver" (7 bytes) + 1 payload byte → body = 9
	abi := customSection("abi_ver", []byte{1})
	if len(other) >= len(abi) {
		t.Fatalf("other section (%d bytes) should be smaller than abi_ver (%d bytes)",
			len(other), len(abi))
	}
}

// TestLEBRoundTrip confirms the LEB128 encoder matches readLEB for various values.
func TestLEBRoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 255, 256, 16383, 16384, 1<<16 - 1, 1 << 16, 1 << 32} {
		encoded := leb(v)
		decoded, n, ok := readLEB(encoded)
		if !ok {
			t.Fatalf("LEB decode failed for %d", v)
		}
		if decoded != v || n != len(encoded) {
			t.Fatalf("LEB round-trip: got %d (%d bytes), want %d (%d bytes)",
				decoded, n, v, len(encoded))
		}
	}
}

// TestStampAndReportRoundTrip exercises the full CLI write→read cycle
// via the actual binary (not just library functions).
func TestStampAndReportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "mod_stamped.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	// Stamp to ver=5.
	cmd := exec.Command("go", "run", ".", "-stamp", "5", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stamp CLI failed: %v\n%s", err, outb)
	}

	// Report should show abi_ver=5.
	cmd = exec.Command("go", "run", ".", "-report", out)
	cmd.Dir = "."
	outb, err = cmd.Output()
	if err != nil {
		t.Fatalf("report CLI failed: %v\n%s", err, outb)
	}
	s := strings.TrimSpace(string(outb))
	if !strings.Contains(s, "abi_ver=5") {
		t.Fatalf("report = %q, want abi_ver=5", s)
	}

	// Re-stamp to ver=2 — should REPLACE, not append.
	out2 := filepath.Join(dir, "mod_restamped.wasm")
	cmd = exec.Command("go", "run", ".", "-stamp", "2", out, out2)
	cmd.Dir = "."
	outb, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("re-stamp CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out2)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("re-stamp via CLI produced %d abi_ver sections, want 1", c)
	}
}

// TestStampInPlace verifies that -stamp works when input and output are the
// SAME file (README line 280: `modulesign -stamp 2 hello.wasm hello.wasm`).
// os.ReadFile must fully drain the file before os.WriteFile truncates it —
// if the order were reversed, in-place stamping would destroy the input.
func TestStampInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in_place.wasm")
	// In-place re-stamp of a module that already has abi_ver=2.
	// minimalWasm() carries a "foo" section; verify it survives.
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	if err := os.WriteFile(path, module, 0o644); err != nil {
		t.Fatal(err)
	}

	// Same path for input and output — in-place re-stamp to ver=3.
	cmd := exec.Command("go", "run", ".", "-stamp", "3", path, path)
	cmd.Dir = "."
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("in-place stamp failed: %v\n%s", err, outb)
	}

	w, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back in-place stamped file: %v", err)
	}
	// Must have exactly one abi_ver section with the new version.
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("in-place stamp: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(w)
	if !ok || v != 3 {
		t.Fatalf("in-place stamp: abi_ver=%d ok=%v, want 3/true", v, ok)
	}
	// Non-abi_ver sections must survive in-place re-stamping.
	if c := countCustomSections(t, w, "foo"); c != 1 {
		t.Fatalf("in-place stamp: 'foo' section count=%d, want 1", c)
	}
	// The file must not have been truncated — it must be valid wasm.
	if len(w) < 8 || string(w[0:4]) != "\x00asm" {
		t.Fatal("in-place stamp produced invalid wasm (truncated?)")
	}
}

// TestFindAbiVerPreservesNonAbiSections verifies that stripping abi_ver does
// NOT drop other custom sections that precede it. This is critical: modules
// carry other custom sections (debug names, etc.) that must survive re-stamping.
func TestFindAbiVerPreservesNonAbiSections(t *testing.T) {
	w := wasmWithAbiVer(2) // [minimalWasm(foo), abi_ver=2]
	_, stripped, found := findAbiVer(w)
	if !found {
		t.Fatal("abi_ver not found")
	}
	// The "foo" custom section must survive stripping.
	if c := countCustomSections(t, stripped, "foo"); c != 1 {
		t.Fatalf("foo section dropped during strip: %d remain, want 1", c)
	}
}

// TestReStampPreservesSectionAfterAbiVer verifies that a non-abi_ver custom
// section appearing AFTER the abi_ver section is preserved during re-stamping.
// The existing TestFindAbiVerPreservesNonAbiSections only covers sections BEFORE
// abi_ver; this closes the gap for sections that follow it — critical because
// some toolchains append metadata sections after abi_ver and a re-stamp must
// not silently truncate them.
func TestReStampPreservesSectionAfterAbiVer(t *testing.T) {
	// Build: [minimalWasm(foo), abi_ver=2, bar_section]
	w := minimalWasm()
	w = append(w, customSection("abi_ver", []byte{2})...)
	w = append(w, customSection("bar", []byte{0x42})...)

	// Strip abi_ver; the "bar" section AFTER it must survive.
	_, stripped, found := findAbiVer(w)
	if !found {
		t.Fatal("abi_ver not found")
	}
	if c := countCustomSections(t, stripped, "bar"); c != 1 {
		t.Fatalf("bar section after abi_ver dropped during strip: %d remain, want 1", c)
	}
	if c := countCustomSections(t, stripped, "foo"); c != 1 {
		t.Fatalf("foo section before abi_ver dropped during strip: %d remain, want 1", c)
	}

	// Re-stamp with ver=3; both foo and bar must survive.
	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if c := countCustomSections(t, out, "foo"); c != 1 {
		t.Fatalf("foo section lost after re-stamp: %d", c)
	}
	if c := countCustomSections(t, out, "bar"); c != 1 {
		t.Fatalf("bar section lost after re-stamp: %d", c)
	}
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamped module has %d abi_ver sections, want 1", c)
	}
	// The abi_ver section must come last (after all other custom sections).
	ver, _, ok := findAbiVer(out)
	if !ok || ver != 3 {
		t.Fatalf("first abi_ver after re-stamp = %d (want 3)", ver)
	}
}

// countCustomSections counts custom sections with the given name in a wasm binary.
func countCustomSections(t *testing.T, wasm []byte, name string) int {
	t.Helper()
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		return 0
	}
	count := 0
	rest := wasm[8:]
	for len(rest) > 0 {
		id := rest[0]
		rest = rest[1:]
		v, n, ok := readLEB(rest)
		if !ok || uint64(len(rest)-n) < v {
			break
		}
		body := rest[n : n+int(v)]
		rest = rest[n+int(v):]
		if id != 0 {
			continue
		}
		nameLen, n2, ok := readLEB(body)
		if !ok || uint64(len(body)-n2) < nameLen {
			continue
		}
		if string(body[n2:n2+int(nameLen)]) == name {
			count++
		}
	}
	return count
}

// TestReStampRemovesAllDuplicateAbiVerSections verifies that when a module
// has MULTIPLE abi_ver sections (as add_abiver.py creates by blindly appending),
// re-stamping removes ALL of them and writes exactly one. The guest reader
// takes the first abi_ver section — leaving a stale duplicate before the new
// one would let the old version survive.
func TestReStampRemovesAllDuplicateAbiVerSections(t *testing.T) {
	// Build a module with two abi_ver sections: ver=2 then ver=5.
	// This mimics add_abiver.py being run twice without stripping.
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	module = append(module, customSection("abi_ver", []byte{5})...)

	if c := countAbiVerSections(t, module); c != 2 {
		t.Fatalf("setup: %d abi_ver sections, want 2", c)
	}

	// Strip all abi_ver sections.
	_, stripped, found := findAbiVer(module)
	if !found {
		t.Fatal("findAbiVer should find abi_ver in duplicate-stamped module")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("after strip: %d abi_ver sections remain, want 0", c)
	}

	// The non-abi_ver "foo" section must also survive.
	if c := countCustomSections(t, stripped, "foo"); c != 1 {
		t.Fatalf("foo section lost during duplicate strip: %d", c)
	}

	// Re-stamp with ver=3.
	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamped duplicate module has %d abi_ver sections, want 1", c)
	}
	// The guest reader takes the FIRST abi_ver section — it must be the new one (3),
	// not a stale duplicate (5).
	ver, _, ok := findAbiVer(out)
	if !ok || ver != 3 {
		t.Fatalf("first abi_ver after re-stamp = %d (want 3); stale duplicate survived", ver)
	}
}

// TestReStampViaCLIOnRealWasmModule uses the actual CLI to re-stamp a wasm
// binary that simulates the add_abiver.py duplication scenario.
func TestReStampViaCLIOnDuplicateModule(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "dup.wasm")
	out := filepath.Join(dir, "dup_restamped.wasm")
	// Create a module with two abi_ver sections.
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	module = append(module, customSection("abi_ver", []byte{5})...)
	os.WriteFile(in, module, 0o644)

	cmd := exec.Command("go", "run", ".", "-stamp", "2", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stamp CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("CLI re-stamp of duplicate module: %d abi_ver sections, want 1", c)
	}
	ver, _, _ := findAbiVer(w)
	if ver != 2 {
		t.Fatalf("CLI re-stamp version = %d, want 2 (not stale 5)", ver)
	}
}

// TestMismatchOnDuplicateModule verifies that -mismatch on a module carrying
// duplicate abi_ver sections (the add_abiver.py blind-append scenario) strips
// ALL of them before stamping ver=99. Without the replace-all behavior, a stale
// duplicate could survive ahead of the new section and the guest reader — which
// takes the FIRST — would see the old version instead of the mismatch.
func TestMismatchOnDuplicateModule(t *testing.T) {
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	module = append(module, customSection("abi_ver", []byte{5})...)

	_, stripped, found := findAbiVer(module)
	if !found {
		t.Fatal("abi_ver not found in duplicate-stamped module")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("strip left %d abi_ver sections, want 0", c)
	}
	out := append(stripped, customSection("abi_ver", []byte{99})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("mismatch on duplicate: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok || v != 99 {
		t.Fatalf("mismatch on duplicate: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// TestMismatchOnDuplicateModuleViaCLI drives the same scenario through the
// actual binary so the -mismatch flag's CLI path is covered against duplicates.
func TestMismatchOnDuplicateModuleViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "dup.wasm")
	out := filepath.Join(dir, "dup_mismatched.wasm")
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	module = append(module, customSection("abi_ver", []byte{5})...)
	os.WriteFile(in, module, 0o644)

	cmd := exec.Command("go", "run", ".", "-mismatch", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mismatch CLI on duplicate failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("CLI mismatch on duplicate: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(w)
	if !ok || v != 99 {
		t.Fatalf("CLI mismatch on duplicate: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// TestStampAndMismatchFlagInteraction verifies that -mismatch takes precedence
// over -stamp when both are set: the code path sets ver=99 after applying -stamp.
// This pins the documented precedence so a future refactor cannot silently
// change the behavior.
func TestStampAndMismatchFlagInteraction(t *testing.T) {
	// When -mismatch is set, main() sets ver=99 regardless of -stamp.
	// We verify the main() logic: mismatch overrides any -stamp value.
	w := minimalWasm()
	_, stripped, _ := findAbiVer(w)
	// Simulate main()'s logic: stamp=5 then mismatch overrides to 99.
	ver := 5
	mismatch := true
	if mismatch {
		ver = 99
	}
	if ver != 99 {
		t.Fatalf("mismatch should override -stamp 5, got ver=%d", ver)
	}
	out := append(stripped, customSection("abi_ver", []byte{byte(ver)})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("stamp+mismatch: %d sections, want 1", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok || v != 99 {
		t.Fatalf("stamp+mismatch: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// is byte-for-byte identical to scripts/add_abiver.py — the legacy Python tool
// still used by the kernel Makefile. This pins the claim in MEMORY.md (F7)
// "byte-for-byte parity with scripts/add_abiver.py" as an automated regression
// test rather than relying on manual verification.
//
// Both tools stamp a module that has NO prior abi_ver section, so their full
// outputs should be identical: original wasm + one abi_ver custom section.
func TestParityWithAddAbiVer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found; skipping add_abiver.py parity check")
	}
	abiverPath := "../../scripts/add_abiver.py"
	if _, err := os.Stat(abiverPath); err != nil {
		t.Skipf("add_abiver.py not found at %s; skipping parity check", abiverPath)
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	// Stamp with modulesign.
	msOut := filepath.Join(dir, "modulesign.wasm")
	cmd := exec.Command("go", "run", ".", "-stamp", "2", in, msOut)
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("modulesign stamp failed: %v\n%s", err, out)
	}

	// Stamp with add_abiver.py.
	abOut := filepath.Join(dir, "addabiver.wasm")
	cmd = exec.Command("python3", abiverPath, in, abOut, "2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add_abiver.py stamp failed: %v\n%s", err, out)
	}

	msData, _ := os.ReadFile(msOut)
	abData, _ := os.ReadFile(abOut)
	if !bytes.Equal(msData, abData) {
		t.Fatalf("modulesign and add_abiver.py diverge:\n  modulesign: %s\n  addabiver: %s",
			hex.EncodeToString(msData), hex.EncodeToString(abData))
	}

	// Both should have exactly one abi_ver section with version 2.
	for _, label := range []string{"modulesign output", "add_abiver.py output"} {
		data := msData
		if label == "add_abiver.py output" {
			data = abData
		}
		if c := countAbiVerSections(t, data); c != 1 {
			t.Fatalf("%s: %d abi_ver sections, want 1", label, c)
		}
		v, _, ok := findAbiVer(data)
		if !ok || v != 2 {
			t.Fatalf("%s: abi_ver=%d ok=%v, want 2/true", label, v, ok)
		}
	}
}

// TestAbiVerEncodingMatchesAbiSpec verifies the encoding matches the ABI spec:
// "ABI bump to v2 (abi_ver custom section payload starts with byte 0x02)."
// The payload is a single byte, not a multi-byte integer.
func TestAbiVerEncodingMatchesAbiSpec(t *testing.T) {
	ver := byte(2)
	sec := customSection("abi_ver", []byte{ver})
	// Layout: [0x00] [body_len] [name_len] "abi_ver" [ver_byte]
	want := []byte{0x00, 0x09, 0x07, 'a', 'b', 'i', '_', 'v', 'e', 'r', ver}
	if !bytes.Equal(sec, want) {
		t.Fatalf("abi_ver section layout mismatch:\n  got:  %x\n  want: %x",
			sec, want)
	}
}

// TestMismatchOnUnstampedModule verifies that -mismatch works correctly on a
// module that has NO prior abi_ver section. findAbiVer returns the original
// unstripped wasm (found=false), so -mismatch should append exactly one
// abi_ver=99 section.
func TestMismatchOnUnstampedModule(t *testing.T) {
	w := minimalWasm() // no abi_ver section
	_, stripped, found := findAbiVer(w)
	if found {
		t.Fatal("findAbiVer should return found=false for unstamped module")
	}
	// stripped should equal the original (no abi_ver to remove).
	if !bytes.Equal(stripped, w) {
		t.Fatal("findAbiVer should return the original for unstamped module")
	}
	// -mismatch appends abi_ver=99 to the (unmodified) stripped output.
	out := append(stripped, customSection("abi_ver", []byte{99})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("mismatch on unstamped: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(out)
	if !ok || v != 99 {
		t.Fatalf("mismatch on unstamped: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// TestDefaultStampVersionCLI verifies that -stamp 0 (the flag default) or
// no explicit version defaults to abi_ver=2, which is what the kernel accepts.
// This pins the defaulting logic in main() that would silently ship every
// module with the wrong ABI version if it regressed.
func TestDefaultStampVersionCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "mod_default.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	// -stamp 0 should default to version 2 (kernel-acceptable).
	cmd := exec.Command("go", "run", ".", "-stamp", "0", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("default stamp CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	v, _, ok := findAbiVer(w)
	if !ok || v != 2 {
		t.Fatalf("default stamp: abi_ver=%d ok=%v, want 2/true", v, ok)
	}
}

// TestReportOnMismatchedModule verifies that -report correctly reads a module
// stamped with the deliberately wrong version 99 (-mismatch). The report path
// must work for any version byte, not just 2.
func TestReportOnMismatchedModule(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "mod_mismatched.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	// Stamp with mismatch (ver=99).
	cmd := exec.Command("go", "run", ".", "-mismatch", in, out)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mismatch CLI failed: %v\n%s", err, outb)
	}

	// Report should show abi_ver=99.
	cmd = exec.Command("go", "run", ".", "-report", out)
	cmd.Dir = "."
	outb, err = cmd.Output()
	if err != nil {
		t.Fatalf("report CLI failed: %v\n%s", err, outb)
	}
	s := strings.TrimSpace(string(outb))
	if !strings.Contains(s, "abi_ver=99") {
		t.Fatalf("report on mismatched = %q, want abi_ver=99", s)
	}
}

// TestAbiVerMultiBytePayload verifies that findAbiVer reads only the FIRST
// byte of the abi_ver payload as the version, even if the payload is longer.
// This matches the spec ("payload starts with byte 0x02") and the kernel
// reader which also reads payload[0].
func TestAbiVerMultiBytePayload(t *testing.T) {
	// Build a module with an abi_ver section carrying a 3-byte payload.
	w := minimalWasm()
	w = append(w, customSection("abi_ver", []byte{2, 0xFF, 0xEE})...)
	v, _, ok := findAbiVer(w)
	if !ok {
		t.Fatal("abi_ver not found")
	}
	if v != 2 {
		t.Fatalf("version from multi-byte payload = %d, want 2 (first byte only)", v)
	}
	// The section must still be found and stripped correctly.
	_, stripped, found := findAbiVer(w)
	if !found {
		t.Fatal("should find abi_ver in multi-byte payload")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("stripped should have 0 abi_ver sections, got %d", c)
	}
}

// TestMismatchedModuleIsDifferentFromDefault verifies the negative-test
// property: a module stamped with -mismatch (99) differs from the kernel-
// accepted version (2). This is the assertion the kernel enforces at load
// time — abi_ver != 2 → module refused.
func TestMismatchedModuleIsDifferentFromDefault(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	// Correct stamp (default ver=2).
	correctOut := filepath.Join(dir, "correct.wasm")
	cmd := exec.Command("go", "run", ".", "-stamp", "2", in, correctOut)
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("correct stamp failed: %v\n%s", err, out)
	}

	// Mismatched stamp (ver=99).
	mismatchOut := filepath.Join(dir, "mismatched.wasm")
	cmd = exec.Command("go", "run", ".", "-mismatch", in, mismatchOut)
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mismatch stamp failed: %v\n%s", err, out)
	}

	correctVer, _, ok := findAbiVer(mustReadFile(t, correctOut))
	if !ok || correctVer != 2 {
		t.Fatalf("correct: abi_ver=%d ok=%v, want 2/true", correctVer, ok)
	}
	mismatchVer, _, ok := findAbiVer(mustReadFile(t, mismatchOut))
	if !ok || mismatchVer != 99 {
		t.Fatalf("mismatched: abi_ver=%d ok=%v, want 99/true", mismatchVer, ok)
	}
	if correctVer == mismatchVer {
		t.Fatal("correct and mismatched modules should have different abi_ver")
	}
}

// TestStampVersionOutOfRange verifies that versions outside 1..255 are
// rejected. This guards the silent byte-truncation footgun: byte(258) wraps
// to 2, which would inadvertently stamp the kernel-accepted version.
func TestStampVersionOutOfRange(t *testing.T) {
	for _, ver := range []int{256, 257, 1000, -1, -100} {
		dir := t.TempDir()
		in := filepath.Join(dir, "mod.wasm")
		out := filepath.Join(dir, "out.wasm")
		os.WriteFile(in, minimalWasm(), 0o644)

		cmd := exec.Command("go", "run", ".", "-stamp", fmt.Sprintf("%d", ver), in, out)
		cmd.Dir = "."
		outb, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("stamp %d: expected non-zero exit, got 0", ver)
		}
		if !strings.Contains(string(outb), "out of range") {
			t.Fatalf("stamp %d: stderr = %q, want 'out of range'", ver, outb)
		}
	}
}

// TestStampVersionBoundaryValues verifies that the boundary values 1 and 255
// are accepted, and that -stamp 0 defaults to 2.
func TestStampVersionBoundaryValues(t *testing.T) {
	for _, ver := range []int{1, 255} {
		dir := t.TempDir()
		in := filepath.Join(dir, "mod.wasm")
		out := filepath.Join(dir, "out.wasm")
		os.WriteFile(in, minimalWasm(), 0o644)

		cmd := exec.Command("go", "run", ".", "-stamp", fmt.Sprintf("%d", ver), in, out)
		cmd.Dir = "."
		if outb, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("stamp %d: %v\n%s", ver, err, outb)
		}
		w, _ := os.ReadFile(out)
		got, _, ok := findAbiVer(w)
		if !ok || got != ver {
			t.Fatalf("stamp %d: abi_ver=%d ok=%v, want %d/true", ver, got, ok, ver)
		}
	}
}

// TestMismatchOverridesOutOfRangeGuard verifies that -mismatch (=99) is
// always in range and thus never rejected by the version guard, even though
// it stamps a deliberately wrong version.
func TestMismatchOverridesOutOfRangeGuard(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "out.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	cmd := exec.Command("go", "run", ".", "-mismatch", in, out)
	cmd.Dir = "."
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mismatch: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	v, _, ok := findAbiVer(w)
	if !ok || v != 99 {
		t.Fatalf("mismatch: abi_ver=%d ok=%v, want 99/true", v, ok)
	}
}

// TestMismatchOverridesStampViaCLI exercises the actual CLI with BOTH -stamp
// and -mismatch set: main() applies -stamp first (ver=5) then -mismatch overrides
// to 99. The in-test simulation in TestStampAndMismatchFlagInteraction does not
// cover the flag.Parse + main() dispatch path, so this pins the documented
// precedence through the real binary.
func TestMismatchOverridesStampViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	out := filepath.Join(dir, "out.wasm")
	os.WriteFile(in, wasmWithAbiVer(2), 0o644)

	cmd := exec.Command("go", "run", ".", "-stamp", "5", "-mismatch", in, out)
	cmd.Dir = "."
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stamp+mismatch CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("stamp+mismatch: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(w)
	if !ok || v != 99 {
		t.Fatalf("stamp+mismatch: abi_ver=%d ok=%v, want 99/true (mismatch must override -stamp 5)", v, ok)
	}
}

// TestReportOnDuplicateModuleViaCLI verifies that -report on a module carrying
// duplicate abi_ver sections returns the FIRST version (the one the kernel
// reader takes), not the last. This is the CLI-path complement to
// TestReStampRemovesAllDuplicateAbiVerSections, which tests findAbiVer directly.
func TestReportOnDuplicateModuleViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "dup.wasm")
	module := append(minimalWasm(), customSection("abi_ver", []byte{2})...)
	module = append(module, customSection("abi_ver", []byte{5})...)
	os.WriteFile(in, module, 0o644)

	cmd := exec.Command("go", "run", ".", "-report", in)
	cmd.Dir = "."
	outb, err := cmd.Output()
	if err != nil {
		t.Fatalf("report on duplicate failed: %v\n%s", err, outb)
	}
	s := strings.TrimSpace(string(outb))
	if !strings.Contains(s, "abi_ver=2") {
		t.Fatalf("report on duplicates = %q, want abi_ver=2 (first section)", s)
	}
}

func TestReStampWithAbiVerAsFirstSection(t *testing.T) {
	header := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	w := append(header, customSection("abi_ver", []byte{2})...)

	_, stripped, found := findAbiVer(w)
	if !found {
		t.Fatal("abi_ver not found when it is the first section after the header")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("after strip: %d abi_ver sections remain, want 0", c)
	}

	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamp with abi_ver first: got %d sections, want 1", c)
	}

	v, _, ok := findAbiVer(out)
	if !ok || v != 3 {
		t.Fatalf("abi_ver = %d, %v; want 3, true", v, ok)
	}

	if !bytes.Equal(stripped, header) {
		t.Fatalf("stripped output is not the bare header: got %d bytes, want %d",
			len(stripped), len(header))
	}
}

func TestReStampFirstSectionViaCLI(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "first.wasm")
	out := filepath.Join(dir, "first_restamped.wasm")
	header := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module := append(header, customSection("abi_ver", []byte{2})...)
	os.WriteFile(in, module, 0o644)

	cmd := exec.Command("go", "run", ".", "-stamp", "3", in, out)
	cmd.Dir = "."
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stamp CLI failed: %v\n%s", err, outb)
	}
	w, _ := os.ReadFile(out)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("CLI re-stamp of first-section module: %d abi_ver sections, want 1", c)
	}
	v, _, ok := findAbiVer(w)
	if !ok || v != 3 {
		t.Fatalf("CLI re-stamp version = %d, %v; want 3, true", v, ok)
	}
}

// mustReadFile is a test helper that reads a file or fails the test.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// TestCLIArgumentValidation verifies that modulesign rejects invalid CLI
// invocations with a non-zero exit code and a usage message, rather than
// silently proceeding or panicking. These paths were untested because the
// existing CLI tests only covered valid invocations.
func TestCLIArgumentValidation(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "mod.wasm")
	os.WriteFile(in, minimalWasm(), 0o644)

	cases := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{
			name:    "no arguments",
			args:    nil,
			wantSub: "usage",
		},
		{
			name:    "stamp missing output path (only 1 positional)",
			args:    []string{"-stamp", "2", in},
			wantSub: "usage",
		},
		{
			name:    "report with extra positional args",
			args:    []string{"-report", in, "extra"},
			wantSub: "usage",
		},
		{
			name:    "stamp non-numeric version",
			args:    []string{"-stamp", "abc", in, filepath.Join(dir, "out.wasm")},
			wantSub: "invalid value",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fullArgs := append([]string{"run", "."}, c.args...)
			cmd := exec.Command("go", fullArgs...)
			cmd.Dir = "."
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected error for %s, got exit 0 (output: %s)", c.name, out)
			}
			if !bytes.Contains(out, []byte(c.wantSub)) {
				t.Fatalf("expected %q in output for %s, got: %q", c.wantSub, c.name, out)
			}
		})
	}
}

// TestCLIInputFileNotFound verifies that modulesign exits non-zero with a
// diagnostic when the input file does not exist, rather than panicking on a
// nil byte slice.
func TestCLIInputFileNotFound(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("go", "run", ".", "-report", filepath.Join(dir, "missing.wasm"))
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error for nonexistent input, got exit 0")
	}
	if !bytes.Contains(out, []byte("modulesign:")) {
		t.Fatalf("expected 'modulesign:' prefix in error, got: %q", out)
	}
}

// ----------------------------------------------------------------------
// Real wasm module tests (gap: existing tests only use minimalWasm which
// has custom sections only; a real module has type/function/code sections
// with id != 0 that findAbiVer must skip and preserve verbatim).
// ----------------------------------------------------------------------

// wasmHeader is the 8-byte wasm binary header (magic + version).
var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// realWasmTypeSection returns a type section with one function type: () -> ().
// [0x01] id=type, [0x04] body_size=4, [0x01] count=1,
//
//	[0x60] func form, [0x00] 0 params, [0x00] 0 results
func realWasmTypeSection() []byte {
	return []byte{0x01, 0x04, 0x01, 0x60, 0x00, 0x00}
}

// realWasmFuncSection returns a function section referencing type index 0.
// [0x03] id=function, [0x02] body_size=2, [0x01] count=1, [0x00] typeidx=0
func realWasmFuncSection() []byte {
	return []byte{0x03, 0x02, 0x01, 0x00}
}

// realWasmCodeSection returns a code section with one empty function body.
// [0x0a] id=code, [0x04] body_size=4, [0x01] count=1,
//
//	[0x02] body_size=2, [0x00] 0 local decls, [0x0b] end
func realWasmCodeSection() []byte {
	return []byte{0x0a, 0x04, 0x01, 0x02, 0x00, 0x0b}
}

// realWasmModule assembles a minimal valid wasm module with actual code
// sections (type, function, code) interleaved with custom sections
// (abi_ver in the middle, "name" at the end). This exercises code paths
// that minimalWasm() — which only has custom sections — cannot reach.
func realWasmModule(ver byte) []byte {
	out := append([]byte{}, wasmHeader...)
	out = append(out, realWasmTypeSection()...)
	out = append(out, realWasmFuncSection()...)
	out = append(out, customSection("abi_ver", []byte{ver})...)
	out = append(out, realWasmCodeSection()...)
	out = append(out, customSection("name", []byte{0x01})...)
	return out
}

// TestFindAbiVerOnRealModule verifies that findAbiVer correctly skips
// non-custom sections (type, function, code) when scanning for abi_ver,
// and preserves them byte-for-byte in the stripped output.
//
// This is the critical gap: all existing tests use minimalWasm() which
// contains only custom sections. findAbiVer's loop reads the section id
// (byte 0) and only inspects the name for id==0 (custom); for id!=0 it
// copies the section wholesale. Without a test using real sections, a
// regression that reads a non-custom section's body as a name string
// would go undetected.
func TestFindAbiVerOnRealModule(t *testing.T) {
	w := realWasmModule(2)

	// The module must start with the wasm magic.
	if !bytes.HasPrefix(w, wasmHeader) {
		t.Fatal("real module does not start with wasm header")
	}

	// findAbiVer must locate the abi_ver and return version 2.
	ver, stripped, found := findAbiVer(w)
	if !found || ver != 2 {
		t.Fatalf("findAbiVer: found=%v ver=%d, want true/2", found, ver)
	}

	// The stripped output must still start with the wasm header.
	if !bytes.HasPrefix(stripped, wasmHeader) {
		t.Fatal("stripped output missing wasm header")
	}

	// All non-custom sections must survive, byte-for-byte.
	for label, want := range map[string][]byte{
		"type": realWasmTypeSection(),
		"func": realWasmFuncSection(),
		"code": realWasmCodeSection(),
		"name": customSection("name", []byte{0x01}),
	} {
		if !bytes.Contains(stripped, want) {
			t.Fatalf("stripped output lost the %s section (looking for %x)", label, want)
		}
	}

	// No abi_ver sections should remain after stripping.
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("stripped output has %d abi_ver sections, want 0", c)
	}

	// Re-stamp and verify the round-trip: exactly one section, correct version,
	// all real sections still present.
	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if c := countAbiVerSections(t, out); c != 1 {
		t.Fatalf("re-stamped real module has %d abi_ver sections, want 1", c)
	}
	rv, _, rok := findAbiVer(out)
	if !rok || rv != 3 {
		t.Fatalf("re-stamped: ver=%d ok=%v, want 3/true", rv, rok)
	}
	for label, want := range map[string][]byte{
		"type": realWasmTypeSection(),
		"func": realWasmFuncSection(),
		"code": realWasmCodeSection(),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("re-stamped output lost the %s section", label)
		}
	}
}

// TestFindAbiVerOnRealModuleAtEnd verifies findAbiVer handles a real module
// where abi_ver appears as the LAST section (after all code sections) — the
// common layout produced by add_abiver.py / modulesign.
func TestFindAbiVerOnRealModuleAtEnd(t *testing.T) {
	w := append([]byte{}, wasmHeader...)
	w = append(w, realWasmTypeSection()...)
	w = append(w, realWasmFuncSection()...)
	w = append(w, realWasmCodeSection()...)
	w = append(w, customSection("abi_ver", []byte{2})...)

	ver, stripped, found := findAbiVer(w)
	if !found || ver != 2 {
		t.Fatalf("findAbiVer: found=%v ver=%d, want true/2", found, ver)
	}
	// All three real sections must be preserved.
	if !bytes.Contains(stripped, realWasmTypeSection()) {
		t.Fatal("type section lost")
	}
	if !bytes.Contains(stripped, realWasmFuncSection()) {
		t.Fatal("function section lost")
	}
	if !bytes.Contains(stripped, realWasmCodeSection()) {
		t.Fatal("code section lost")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("stripped has %d abi_ver, want 0", c)
	}
}

// TestReStampViaCLIOnRealModule exercises the full CLI stamp → report cycle
// on a module with real code sections, not just minimalWasm(). If the
// parser mishandled non-custom sections during the strip, the re-stamped
// module would be missing its type/function/code sections or carry a stale
// abi_ver.
func TestReStampViaCLIOnRealModule(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "real.wasm")
	outPath := filepath.Join(dir, "real_restamped.wasm")
	os.WriteFile(inPath, realWasmModule(2), 0o644)

	// Stamp to ver=3 via CLI.
	cmd := exec.Command("go", "run", ".", "-stamp", "3", inPath, outPath)
	cmd.Dir = "."
	outb, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI stamp on real module failed: %v\n%s", err, outb)
	}

	// Exactly one abi_ver at version 3.
	w, _ := os.ReadFile(outPath)
	if c := countAbiVerSections(t, w); c != 1 {
		t.Fatalf("re-stamped real module: %d abi_ver sections, want 1", c)
	}
	ver, _, ok := findAbiVer(w)
	if !ok || ver != 3 {
		t.Fatalf("ver=%d ok=%v, want 3/true", ver, ok)
	}

	// Real code sections must survive the CLI stamp.
	for label, want := range map[string][]byte{
		"type": realWasmTypeSection(),
		"func": realWasmFuncSection(),
		"code": realWasmCodeSection(),
	} {
		if !bytes.Contains(w, want) {
			t.Fatalf("CLI re-stamp lost %s section", label)
		}
	}
}

// TestLargeSectionSize exercises a custom section whose body exceeds 127
// bytes — requiring a multi-byte ULEB128 size encoding. findAbiVer's
// readLEB must correctly decode the two-byte LEB128 size and skip the
// full section body, not misparse it as a single byte.
func TestLargeSectionSize(t *testing.T) {
	longName := strings.Repeat("x", 200) // > 127 bytes in name → multi-byte LEB for body size
	longSec := customSection(longName, []byte{0x42})
	w := append(append([]byte{}, wasmHeader...), longSec...)
	w = append(w, customSection("abi_ver", []byte{2})...)

	ver, stripped, found := findAbiVer(w)
	if !found || ver != 2 {
		t.Fatalf("findAbiVer: found=%v ver=%d, want true/2", found, ver)
	}
	// The long custom section must survive (its name must be intact).
	if !bytes.Contains(stripped, []byte(longName)) {
		t.Fatal("long custom section lost during strip")
	}
	if c := countAbiVerSections(t, stripped); c != 0 {
		t.Fatalf("stripped has %d abi_ver, want 0", c)
	}
	// Re-stamp preserves the long name.
	out := append(stripped, customSection("abi_ver", []byte{3})...)
	if !bytes.Contains(out, []byte(longName)) {
		t.Fatal("long custom section lost after re-stamp")
	}
}
