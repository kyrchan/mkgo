package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"64M", 64 << 20, true},
		{"64m", 64 << 20, true}, // suffix is case-insensitive
		{"512k", 512 << 10, true},
		{"1G", 1 << 30, true},
		{"4096", 4096, true},
		{" 8M ", 8 << 20, true},
		{"", 0, false},
		{"0", 0, false},
		{"-1", 0, false},
		{"abc", 0, false},
		{"12X", 0, false}, // unknown suffix treated as part of the number
		{"9223372036854775807M", 0, false},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseSize(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseSize(%q) unexpectedly succeeded (%d)", c.in, got)
		}
	}
}

func TestParseMapping(t *testing.T) {
	for _, c := range []struct {
		in       string
		src, dst string
		ok       bool
	}{
		{"/host/app.wasm:/vm/app", "/host/app.wasm", "/vm/app", true},
		{"", "", "", false},
		{":/vm/app", "", "", false},
		{"/host/app.wasm:", "", "", false},
		{"/host/app.wasm", "", "", false},
	} {
		src, dst, err := parseMapping(c.in)
		if c.ok {
			if err != nil || src != c.src || dst != c.dst {
				t.Errorf("parseMapping(%q) = %q, %q, %v; want %q, %q",
					c.in, src, dst, err, c.src, c.dst)
			}
		} else if err == nil {
			t.Errorf("parseMapping(%q) unexpectedly succeeded", c.in)
		}
	}
}

func TestFlushToFilePublishesExactImage(t *testing.T) {
	dev := NewMemBlock(64 << 10)
	for i := range dev.data {
		dev.data[i] = byte(i * 31)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "disk.img")
	if err := FlushToFile(dev, out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dev.data) {
		t.Fatal("published image differs from block device")
	}
	tmp, err := filepath.Glob(filepath.Join(dir, ".disk.img.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmp) != 0 {
		t.Fatalf("temporary output files remain: %v", tmp)
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{
		64 << 20: "64MiB",
		8 << 10:  "8KiB",
		100:      "100",
	} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q want %q", in, got, want)
		}
	}
}

func TestGeometryClampsHugeVolumes(t *testing.T) {
	// 16 GiB raw: cylinder counts must clamp to the BIOS-visible maximum
	// for both converters without materializing any data.
	const huge = int64(16) << 30
	if cyl := vdiCylinders(huge); cyl != vdiMaxCyls {
		t.Fatalf("vdi cylinders = %d want clamp %d", cyl, vdiMaxCyls)
	}
	desc := vmdkDescriptor(huge, 0xdeadbeef, "x-flat.vmdk")
	if !strings.Contains(desc, `ddb.geometry.cylinders = "16383"`) {
		t.Fatalf("vmdk cylinders not clamped:\n%s", desc)
	}
	// Sub-geometry tiny disks still yield a usable (>=1) cylinder count.
	if cyl := vdiCylinders(256 << 10); cyl != 1 { // 256 KiB < one cylinder
		t.Fatalf("tiny vdi cylinders = %d want 1", cyl)
	}
}

func TestVolumeRejectsBadSizesAndLabels(t *testing.T) {
	dev := NewMemBlock(64 << 20)
	if _, err := NewVolume(dev, 12345, ""); err == nil { // not sector-aligned
		t.Fatal("unaligned size accepted")
	}
	if _, err := NewVolume(dev, -1, ""); err == nil {
		t.Fatal("negative size accepted")
	}
	longLabel := strings.Repeat("x", 12)
	if _, err := NewVolume(dev, 64<<20, longLabel); err == nil ||
		!strings.Contains(err.Error(), "label") {
		t.Fatalf("want label-length error, got %v", err)
	}
}

func TestDuplicateFileRejected(t *testing.T) {
	size := int64(8 << 20)
	dev := NewMemBlock(size)
	v, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, v, FileSource{Name: "/etc/motd", Data: []byte("a")})
	err = v.Add(FileSource{Name: "/etc/motd", Data: []byte("b")})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestMkDirIdempotent(t *testing.T) {
	size := int64(8 << 20)
	dev := NewMemBlock(size)
	v, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := v.MkDir("/boot/modules"); err != nil {
			t.Fatalf("MkDir pass %d: %v", i, err)
		}
	}
	mustAdd(t, v, FileSource{Name: "/boot/modules/x.wasm", Data: []byte("x")})
	if err := v.Flush(); err != nil {
		t.Fatal(err)
	}
	fr, _ := newFatReader(dev.Bytes())
	kids, _ := fr.listDir(mustFind(t, fr, "/boot/modules").start)
	n := 0
	for _, k := range kids {
		if k.name == "x.wasm" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("x.wasm appears %d times after repeated MkDir", n)
	}
}

// TestPositionalFilePlacement verifies that bare positional "src:dst"
// arguments are accepted and written to the image, mirroring how the
// Makefile's disk rules pass payloads (e.g. "build/hello.wasm:/vm/app")
// without the -file flag. flag.Parse() silently drops positionals, so a
// regression here makes every QEMU boot gate fail at the loader with
// "file not found". This test catches that class directly by round-tripping
// a positional placement through the CLI reader.
func TestPositionalFilePlacement(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "hello.wasm")
	const payload = "hello from C\n"
	if err := os.WriteFile(src, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	efi := filepath.Join(dir, "BOOTX64.EFI")
	if err := os.WriteFile(efi, []byte("efi"), 0o644); err != nil {
		t.Fatal(err)
	}

	imgPath := filepath.Join(dir, "disk.img")
	// Positional arg, exactly as the Makefile passes it (no -file/-app).
	if out, err := exec.Command("go", "run", ".", "-o", imgPath,
		"-size", "64M", "-efi", efi, src+":/vm/app").CombinedOutput(); err != nil {
		t.Fatalf("img build failed: %v\n%s", err, out)
	}

	// Read /vm/app back through the -read path; its absence proves the
	// positional arg was ignored.
	out, err := exec.Command("go", "run", ".", "-in", imgPath, "-read", "/vm/app").
		CombinedOutput()
	if err != nil {
		t.Fatalf("img read failed: %v\n%s", err, out)
	}
	if string(out) != payload {
		t.Fatalf("positional placement content = %q, want %q", out, payload)
	}
}

// mustFind locates a path in a built image and fails the test if it is
// absent. It moved here with the FAT reader's promotion to readback.go.
func mustFind(t interface{ Fatalf(string, ...any) }, fr *fatReader, path string) rdEntry {
	e, ok, err := fr.find(path)
	if err != nil || !ok {
		t.Fatalf("image: %s not found (err=%v)", path, err)
	}
	return e
}
