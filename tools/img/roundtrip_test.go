package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestReadCLIErrors verifies that the -read CLI mode handles error conditions
// gracefully: non-existent image file, non-FAT16 input, and missing file paths
// all produce diagnostic output and a non-zero exit code rather than silent
// success or a panic. These paths were previously untested because img was
// test-only; the promoted reader exposed them to the CLI.
func TestReadCLIErrors(t *testing.T) {
	dir := t.TempDir()

	// Build a valid image for the "file not found in image" case.
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efiPath, []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "run", ".", "-o", imgPath, "-efi", efiPath).
		CombinedOutput(); err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	// Non-FAT16 file (shorter than 512 bytes, no boot signature).
	badPath := filepath.Join(dir, "notfat.bin")
	os.WriteFile(badPath, []byte("not a disk image"), 0o644)

	cases := []struct {
		name     string
		img      string
		readPath string
		wantSub  string
	}{
		{"nonexistent image file", filepath.Join(dir, "missing.img"), "/etc/motd", "no such file"},
		{"not a FAT16 image", badPath, "/etc/motd", "55AA boot signature"},
		{"file not found in valid image", imgPath, "/nonexistent", "not found"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("go", "run", ".", "-in", c.img, "-read", c.readPath)
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

// TestWriteCLIErrors verifies that the write-mode CLI (build image) rejects
// invalid invocations with a non-zero exit code and a diagnostic message,
// rather than silently proceeding or panicking. These paths were untested:
// the read-mode error paths (TestReadCLIErrors) had full CLI coverage, but the
// write-mode usage/invalid-input paths were never exercised through `go run`.
func TestWriteCLIErrors(t *testing.T) {
	dir := t.TempDir()
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	os.WriteFile(efiPath, []byte("efi"), 0o644)

	cases := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{
			name:    "missing -o flag",
			args:    []string{"-efi", efiPath},
			wantSub: "usage:",
		},
		{
			name:    "missing -efi flag",
			args:    []string{"-o", filepath.Join(dir, "out.img")},
			wantSub: "usage:",
		},
		{
			name:    "invalid -size",
			args:    []string{"-o", filepath.Join(dir, "out.img"), "-efi", efiPath, "-size", "abc"},
			wantSub: "bad size",
		},
		{
			name:    "invalid -file mapping (no colon)",
			args:    []string{"-o", filepath.Join(dir, "out.img"), "-efi", efiPath, "-file", "no_colon"},
			wantSub: "wants src:dst",
		},
		{
			name:    "missing input file for -file",
			args:    []string{"-o", filepath.Join(dir, "out.img"), "-efi", efiPath, "-file", filepath.Join(dir, "missing.txt") + ":/etc/motd"},
			wantSub: "img:",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("go", append([]string{"run", "."}, c.args...)...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected non-zero exit for %s, got 0 (output: %s)", c.name, out)
			}
			if !bytes.Contains(out, []byte(c.wantSub)) {
				t.Fatalf("%s: expected %q in output, got: %q", c.name, c.wantSub, out)
			}
		})
	}
}

// TestWriteCLIUsageWithNoArgs verifies that running img with no arguments
// prints usage and exits with code 2 (the flag package's convention for
// usage errors on missing required flags).
func TestWriteCLIUsageWithNoArgs(t *testing.T) {
	cmd := exec.Command("go", "run", ".")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for no-args invocation, got 0")
	}
	if !bytes.Contains(out, []byte("usage:")) {
		t.Fatalf("expected usage message in output, got: %q", out)
	}
}

// TestReadCLIEmptyReadFlag verifies that omitting -read (or passing empty) in
// read mode exits with code 2 and a usage message rather than reading garbage
// or crashing. The -in flag activates read mode, but -read still must carry a
// path.
func TestReadCLIEmptyReadFlag(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	os.WriteFile(efiPath, []byte("efi"), 0o644)
	out, err := exec.Command("go", "run", ".", "-o", imgPath, "-efi", efiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	// Omitting -read entirely: runRead is called with an empty fatPath.
	cmd := exec.Command("go", "run", ".", "-in", imgPath)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error when -read is omitted, got exit 0")
	}
	if !bytes.Contains(out, []byte("requires a FAT path")) {
		t.Fatalf("expected usage message, got: %q", out)
	}
}

// TestRoundTripCLIDrivesBuilderAndReader is the M3 round-trip gate for
// tools/img: build a known image, read its files back via the CLI, and
// compare against the inputs byte-for-byte.
//
// This turns "the builder is correct" from an assumption into a test.
func TestRoundTripCLIDrivesBuilderAndReader(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")

	// Known inputs.
	motdContent := []byte("hello kernel land\n")
	wasmContent := []byte("\x00asm\x01\x00\x00\x00hello-wasm")
	payloadContent := bytes.Repeat([]byte("payload!"), 256) // > 1 sector

	// Build the image via the img CLI (simulates make disk-*img).
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efiPath, []byte("efi-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	motdPath := filepath.Join(dir, "motd")
	wasmPath := filepath.Join(dir, "app.wasm")
	payloadPath := filepath.Join(dir, "payload.bin")
	os.WriteFile(motdPath, motdContent, 0o644)
	os.WriteFile(wasmPath, wasmContent, 0o644)
	os.WriteFile(payloadPath, payloadContent, 0o644)

	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-file", motdPath+":/etc/motd",
		"-file", wasmPath+":/boot/modules/app.wasm",
		"-file", payloadPath+":/vm/app",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	// Read each file back via the CLI and compare.
	cases := []struct {
		fatPath string
		want    []byte
	}{
		{"/EFI/BOOT/BOOTX64.EFI", []byte("efi-bytes")},
		{"/etc/motd", motdContent},
		{"/boot/modules/app.wasm", wasmContent},
		{"/vm/app", payloadContent},
	}

	for _, c := range cases {
		got, err := imgRead(t, ".", imgPath, c.fatPath)
		if err != nil {
			t.Fatalf("read %s: %v", c.fatPath, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Fatalf("%s: got %d bytes, want %d bytes",
				c.fatPath, len(got), len(c.want))
		}
	}
}

// imgRead runs `img -in IMG -read PATH` via go run and returns stdout.
func imgRead(t *testing.T, srcDir, imgPath, fatPath string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "-in", imgPath, "-read", fatPath)
	cmd.Dir = srcDir
	return cmd.Output()
}

// TestRoundTripAPICallsReadFile exercises the fatReader.readFile method directly
// (without the CLI) to validate the promoted reader API.
func TestRoundTripAPICallsReadFile(t *testing.T) {
	mt := time.Date(2026, 8, 22, 12, 34, 56, 0, time.UTC)
	wantEfi := []byte("efi-bytes")
	wantMotd := []byte("hello from /etc/motd\n")
	wantWasm := []byte("\x00asm\x01\x00\x00\x00hello-wasm")

	dev := buildVol(t, 64<<20, func(v *Volume) {
		mustAdd(t, v, FileSource{"/EFI/BOOT/BOOTX64.EFI", wantEfi, mt})
		mustAdd(t, v, FileSource{"/etc/motd", wantMotd, mt})
		mustAdd(t, v, FileSource{"/boot/modules/app.wasm", wantWasm, mt})
	})

	fr, err := newFatReader(dev.Bytes())
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	for path, want := range map[string][]byte{
		"/EFI/BOOT/BOOTX64.EFI":  wantEfi,
		"/etc/motd":              wantMotd,
		"/boot/modules/app.wasm": wantWasm,
	} {
		got, err := fr.readFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: content mismatch (%d vs %d bytes)", path, len(got), len(want))
		}
	}
}

// TestReadFileMissing verifies the reader returns a clean error for a
// nonexistent file.
func TestReadFileMissing(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		mustAdd(t, v, FileSource{Name: "/etc/motd", Data: []byte("hi")})
	})
	fr, err := newFatReader(dev.Bytes())
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	_, err = fr.readFile("/nonexistent")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not found")) {
		t.Fatalf("error missing 'not found': %v", err)
	}
}

// TestFindRootResolvesRootDirectory verifies that find("/") and find("")
// resolve to the root directory entry, not "not found". This is a regression
// for a bug where strings.Split(strings.Trim("/", "/"), "/") returned [""],
// so the empty segment matched nothing and root appeared absent — making
// readFile("/") report "not found" instead of "is a directory".
func TestFindRootResolvesRootDirectory(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		mustAdd(t, v, FileSource{Name: "/etc/motd", Data: []byte("hi")})
	})
	fr, err := newFatReader(dev.Bytes())
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	for _, p := range []string{"/", "", "//", "/etc/"} {
		e, ok, err := fr.find(p)
		if err != nil || !ok {
			t.Fatalf("find(%q) = ok=%v err=%v, want ok=true", p, ok, err)
		}
		if e.attr&attrDir == 0 {
			t.Fatalf("find(%q): root entry is not a directory (attr=%#x)", p, e.attr)
		}
	}
}

// TestReadFileOnDirectory verifies that readFile rejects directory paths with
// an "is a directory" error rather than returning garbage or "not found".
func TestReadFileOnDirectory(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		mustAdd(t, v, FileSource{Name: "/etc/motd", Data: []byte("hi")})
	})
	fr, err := newFatReader(dev.Bytes())
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	for _, p := range []string{"/", "/etc"} {
		_, err := fr.readFile(p)
		if err == nil {
			t.Fatalf("readFile(%q): expected error, got nil", p)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("directory")) {
			t.Fatalf("readFile(%q): error missing 'directory': %v", p, err)
		}
	}
}

// TestReadCLIDirectory verifies that the CLI -read mode correctly rejects a
// directory path with a non-zero exit code and a diagnostic message.
func TestReadCLIDirectory(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efiPath, []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "run", ".", "-o", imgPath, "-efi", efiPath).
		CombinedOutput(); err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}
	// Reading "/" or "/etc" should fail: directories are not readable files.
	for _, p := range []string{"/", "/etc"} {
		cmd := exec.Command("go", "run", ".", "-in", imgPath, "-read", p)
		cmd.Dir = "."
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("read %s: expected non-zero exit, got 0 (output: %s)", p, out)
		}
		if !bytes.Contains(out, []byte("directory")) {
			t.Fatalf("read %s: expected 'directory' in output, got: %q", p, out)
		}
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_LFN verifies that long file names
// (>8.3) survive the CLI round-trip: the builder emits LFN entries, and the
// independent reader reconstructs the original name and content. The basic
// round-trip test only used short names; this closes the LFN gap through the
// full CLI path (go run . -o ... -file ... | go run . -in ... -read ...).
func TestRoundTripCLIDrivesBuilderAndReader_LFN(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efiPath, []byte("efi-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	lfnContent := []byte("long name body content\n")
	lfnPath := filepath.Join(dir, "lfn.txt")
	if err := os.WriteFile(lfnPath, lfnContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// LFN name > 8.3: requires LFN directory entries to round-trip.
	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-file", lfnPath+":/etc/a_very_long_file_name_beyond_eight_three.txt",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	got, err := imgRead(t, ".", imgPath, "/etc/a_very_long_file_name_beyond_eight_three.txt")
	if err != nil {
		t.Fatalf("read LFN: %v", err)
	}
	if !bytes.Equal(got, lfnContent) {
		t.Fatalf("LFN content mismatch (%d vs %d bytes)", len(got), len(lfnContent))
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_WithSeed verifies the -seed directory
// option: a directory tree is copied into /etc/ on the image, then every file
// is read back via the independent reader and compared byte-for-byte. This
// makes the seed-based config flow (init.conf, /etc/users, /etc/motd) auditable
// instead of trusted by assumption.
func TestRoundTripCLIDrivesBuilderAndReader_WithSeed(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	seedDir := filepath.Join(dir, "templates")

	// Seed files are at the ROOT of the seed dir; AddTree copies them
	// under /etc/ (e.g. seedDir/motd -> /etc/motd, seedDir/sub/deep.cfg -> /etc/sub/deep.cfg).
	files := map[string]string{
		"motd":         "Welcome to the capability microkernel\n",
		"kernel.conf":  "quantum_ms=10\nlog_level=info\n",
		"users/root":   "root:0:hash:fff\n",
		"init.conf":    "console console.wasm 300\n",
		"sub/deep.cfg": "nested-value=42\n",
	}
	for p, content := range files {
		full := filepath.Join(seedDir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	efiContent := []byte("efi-bytes")
	if err := os.WriteFile(efiPath, efiContent, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-seed", seedDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	for seedPath, want := range files {
		fatPath := "/etc/" + seedPath
		got, err := imgRead(t, ".", imgPath, fatPath)
		if err != nil {
			t.Fatalf("read %s: %v", fatPath, err)
		}
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("%s: got %q, want %q", fatPath, got, want)
		}
	}

	// The -efi file must also round-trip alongside -seed.
	got, err := imgRead(t, ".", imgPath, "/EFI/BOOT/BOOTX64.EFI")
	if err != nil {
		t.Fatalf("read EFI: %v", err)
	}
	if !bytes.Equal(got, efiContent) {
		t.Fatalf("EFI content mismatch")
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_Modules verifies the -modules directory
// option: every .wasm file in the directory tree lands under /boot/modules/ and
// is read back correctly through the CLI reader.
func TestRoundTripCLIDrivesBuilderAndReader_Modules(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	modulesDir := filepath.Join(dir, "modules")

	mods := map[string]string{
		"console.wasm": "console-module-bytes",
		"login.wasm":   "login-module-bytes",
		"fs.wasm":      "fs-module-bytes",
	}
	if err := os.MkdirAll(modulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range mods {
		if err := os.WriteFile(filepath.Join(modulesDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(efiPath, []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-modules", modulesDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	for name, want := range mods {
		got, err := imgRead(t, ".", imgPath, "/boot/modules/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, []byte(want)) {
			t.Fatalf("%s content mismatch", name)
		}
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_VMDKAndVDI verifies the CLI integration
// of the -vmdk and -vdi flags: building an image with both flags produces the
// expected descriptor files alongside the raw image, and the VDI is readable
// as a valid VirtualBox fixed image (correct signature + verbatim payload).
// The API-level WriteVMDK/WriteVDI are covered in conv_test.go; this test
// closes the CLI-flag-parsing gap so the full build path is auditable.
//
// Uses -size 8M (instead of the 64M default): the VDI wrapper embeds the entire
// raw image and the VMDK flat is a full copy, so the test allocates ~3x the
// image size in the temp dir. 64M → ~192MB tripped /tmp exhaustion on
// resource-constrained hosts (LANE-NOTES); 8M → ~24MB is well within limits
// while still exercising every VDI/VMDK code path.
func TestRoundTripCLIDrivesBuilderAndReader_VMDKAndVDI(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	vmdkPath := filepath.Join(dir, "disk.vmdk")
	vdiPath := filepath.Join(dir, "disk.vdi")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	efiContent := []byte("efi-bytes")
	if err := os.WriteFile(efiPath, efiContent, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".",
		"-o", imgPath,
		"-size", "8M",
		"-efi", efiPath,
		"-vmdk", vmdkPath,
		"-vdi", vdiPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build with -vmdk/-vdi failed: %v\n%s", err, out)
	}

	// Raw image must still round-trip (the -vmdk/-vdi flags don't interfere).
	got, err := imgRead(t, ".", imgPath, "/EFI/BOOT/BOOTX64.EFI")
	if err != nil {
		t.Fatalf("read EFI from raw image: %v", err)
	}
	if !bytes.Equal(got, efiContent) {
		t.Fatalf("EFI content mismatch in raw image")
	}

	// VMDK descriptor must exist and contain the monolithicFlat marker.
	vmdkDesc, err := os.ReadFile(vmdkPath)
	if err != nil {
		t.Fatalf("read .vmdk: %v", err)
	}
	if !bytes.Contains(vmdkDesc, []byte(`createType="monolithicFlat"`)) {
		t.Fatalf("VMDK descriptor missing monolithicFlat marker:\n%s", vmdkDesc)
	}
	// VMDK flat extent must be a byte-exact copy of the raw image.
	vmdkFlat, err := os.ReadFile(filepath.Join(dir, "disk-flat.vmdk"))
	if err != nil {
		t.Fatalf("read -flat.vmdk: %v", err)
	}
	rawImg, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(vmdkFlat, rawImg) {
		t.Fatal("VMDK flat extent does not match raw image")
	}

	// VDI must exist, have the correct signature, and embed the raw payload.
	vdiData, err := os.ReadFile(vdiPath)
	if err != nil {
		t.Fatalf("read .vdi: %v", err)
	}
	if len(vdiData) < vdiDataOffset+512 {
		t.Fatalf("VDI too short: %d bytes", len(vdiData))
	}
	if !bytes.HasPrefix(vdiData, []byte("<<< Oracle VM VirtualBox Disk Image >>>\n")) {
		t.Fatalf("VDI pre-header mismatch: %q", vdiData[:39])
	}
	if binary.LittleEndian.Uint32(vdiData[0x040:]) != vdiSignature {
		t.Fatalf("VDI signature = %#x, want %#x", binary.LittleEndian.Uint32(vdiData[0x040:]), vdiSignature)
	}
	if !bytes.Equal(vdiData[vdiDataOffset:], rawImg) {
		t.Fatal("VDI payload diverges from raw image")
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_App verifies the -app flag writes a
// payload to /vm/app in the image and the independent reader reads it back.
func TestRoundTripCLIDrivesBuilderAndReader_App(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	appPath := filepath.Join(dir, "payload.bin")
	appContent := []byte("hello guest payload\n")

	os.WriteFile(efiPath, []byte("efi"), 0o644)
	os.WriteFile(appPath, appContent, 0o644)

	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-app", appPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build with -app failed: %v\n%s", err, out)
	}

	got, err := imgRead(t, ".", imgPath, "/vm/app")
	if err != nil {
		t.Fatalf("read /vm/app: %v", err)
	}
	if !bytes.Equal(got, appContent) {
		t.Fatalf("/vm/app: got %q, want %q", got, appContent)
	}
}

// TestRoundTripCLIDrivesBuilderAndReader_DeepNested verifies that deeply
// nested directory trees survive the CLI round-trip. The FAT16 reader must
// follow chained subdirectory clusters through multiple levels.
func TestRoundTripCLIDrivesBuilderAndReader_DeepNested(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	efiPath := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efiPath, []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deeply nested file: /a/b/c/d/e/f/deep.txt
	// Requires allocating cluster chains for each directory level.
	deepContent := []byte("deep-nested-content\n")
	deepPath := filepath.Join(dir, "deep.txt")
	if err := os.WriteFile(deepPath, deepContent, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "run", ".", "-o", imgPath,
		"-efi", efiPath,
		"-file", deepPath+":/a/b/c/d/e/f/deep.txt",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	// Read the deeply nested file back via the CLI.
	got, err := imgRead(t, ".", imgPath, "/a/b/c/d/e/f/deep.txt")
	if err != nil {
		t.Fatalf("read deep: %v", err)
	}
	if !bytes.Equal(got, deepContent) {
		t.Fatalf("deep content mismatch: got %q, want %q", got, deepContent)
	}

	// Intermediate directories must exist and be directories, verified
	// through the independent reader (not the builder's own view).
	raw, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	fr, err := newFatReader(raw)
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	for _, dirPath := range []string{"/a", "/a/b", "/a/b/c", "/a/b/c/d", "/a/b/c/d/e", "/a/b/c/d/e/f"} {
		e, ok, err := fr.find(dirPath)
		if err != nil || !ok {
			t.Fatalf("directory %s not found (ok=%v err=%v)", dirPath, ok, err)
		}
		if e.attr&attrDir == 0 {
			t.Fatalf("%s is not a directory (attr=%#x)", dirPath, e.attr)
		}
	}
}
