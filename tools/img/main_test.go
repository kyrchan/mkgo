package main

import (
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
		{"abc", 0, false},
		{"12X", 0, false}, // unknown suffix treated as part of the number
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
