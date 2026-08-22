package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"
)

func buildVol(t *testing.T, size int64, add func(v *Volume)) *MemBlock {
	t.Helper()
	dev := NewMemBlock(size)
	v, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatalf("NewVolume: %v", err)
	}
	add(v)
	if err := v.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return dev
}

func TestGeometryMatchesMtools64MiB(t *testing.T) {
	// Ground truth from `mformat -i ref.img ::` + `minfo` on a 64 MiB disk.
	spc, fatSecs, clusters, err := pickGeometry(131072)
	if err != nil {
		t.Fatal(err)
	}
	if spc != 2 || fatSecs != 255 || clusters != 65264 {
		t.Fatalf("geometry = spc %d fat %d clus %d; want 2/255/65264 (mtools parity)",
			spc, fatSecs, clusters)
	}
}

func TestBootSectorGoldenFields(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {})
	bs := dev.Bytes()[:512]
	g16 := func(o int) int { return int(binary.LittleEndian.Uint16(bs[o:])) }
	if bs[0] != 0xEB || bs[2] != 0x90 {
		t.Fatalf("jmp bytes % X", bs[0:3])
	}
	if g16(11) != 512 || bs[13] != 2 || g16(14) != 1 ||
		int(bs[16]) != numFATs || g16(17) != rootEntries ||
		g16(19) != 0 || bs[21] != mediaDescriptor ||
		g16(22) != 255 || g16(24) != 63 || g16(26) != 16 ||
		binary.LittleEndian.Uint32(bs[28:]) != 0 ||
		binary.LittleEndian.Uint32(bs[32:]) != 131072 ||
		bs[36] != driveNumber || bs[38] != extBootSig {
		t.Fatalf("BPB fields wrong")
	}
	if string(bs[54:62]) != "FAT16   " {
		t.Fatalf("fstype %q", bs[54:62])
	}
	if bs[510] != 0x55 || bs[511] != 0xAA {
		t.Fatal("missing boot signature")
	}
}

func TestRoundTripFullLayout(t *testing.T) {
	mt := time.Date(2026, 8, 22, 12, 34, 56, 0, time.UTC)
	payload := make([]byte, 10000) // spans multiple 1KiB clusters
	rand.Read(payload)

	var vol *Volume
	dev := buildVol(t, 64<<20, func(v *Volume) {
		vol = v
		mustAdd(t, v, FileSource{"/EFI/BOOT/BOOTX64.EFI", []byte("efi-bytes"), mt})
		mustAdd(t, v, FileSource{"/vm/app", payload, mt})
		mustAdd(t, v, FileSource{"/boot/modules/console.wasm", []byte("console"), mt})
		mustAdd(t, v, FileSource{"/boot/modules/login.wasm", []byte("login"), mt})
		mustAdd(t, v, FileSource{"/etc/motd", []byte("welcome\n"), mt})
	})

	fr, err := newFatReader(dev.Bytes())
	if err != nil {
		t.Fatalf("readback parse: %v", err)
	}
	for path, want := range map[string][]byte{
		"/EFI/BOOT/BOOTX64.EFI":      []byte("efi-bytes"),
		"/vm/app":                    payload,
		"/boot/modules/console.wasm": []byte("console"),
		"/boot/modules/login.wasm":   []byte("login"),
		"/etc/motd":                  []byte("welcome\n"),
	} {
		got, err := fr.readFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s content mismatch (%d vs %d bytes)", path, len(got), len(want))
		}
	}
	if got := vol.fileCnt; got != 5 {
		t.Fatalf("fileCnt=%d", got)
	}
}

func TestDirectoriesAreDirectoriesWithDotEntries(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		v.MkDir("/EFI/BOOT")
		v.MkDir("/etc")
		mustAdd(t, v, FileSource{Name: "/boot/modules/m.wasm", Data: []byte("m")})
		mustAdd(t, v, FileSource{Name: "/vm/app", Data: []byte("a")})
	})
	fr, _ := newFatReader(dev.Bytes())
	root, err := fr.listDir(0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]byte{}
	for _, e := range root {
		seen[e.name] = e.attr
	}
	for _, d := range []string{"EFI", "vm", "boot", "etc"} {
		if seen[d]&attrDir == 0 {
			t.Fatalf("%s missing or not a directory (attr %#x)", d, seen[d])
		}
	}
	// Subdirectory dot entries point at self and parent (root => 0).
	e, ok, err := fr.find("/boot")
	if err != nil || !ok {
		t.Fatal("/boot not found")
	}
	kids, _ := fr.listDir(e.start)
	if len(kids) < 3 || kids[0].name != "." || kids[1].name != ".." {
		t.Fatalf("dot entries missing: %+v", kids)
	}
	if kids[0].start != e.start || kids[1].start != 0 {
		t.Fatalf("dot starts wrong: .=%d ..=%d (self=%d)",
			kids[0].start, kids[1].start, e.start)
	}
	// /boot/modules' ".." must reference /boot's start cluster.
	mods, ok, _ := fr.find("/boot/modules")
	if !ok {
		t.Fatal("modules not found")
	}
	mkids, _ := fr.listDir(mods.start)
	if mkids[1].start != e.start {
		t.Fatalf("modules '..'=%d want /boot start %d", mkids[1].start, e.start)
	}
}

func TestLFNLongNamesAndCollisions(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		// console.wasm and login.wasm both mangle to CONSO~1.WAS candidates;
		// the resolver must hand out distinct tails while LFNs keep real names.
		mustAdd(t, v, FileSource{Name: "/boot/modules/console.wasm", Data: []byte("c")})
		mustAdd(t, v, FileSource{Name: "/boot/modules/console2.wasm", Data: []byte("c2")})
		mustAdd(t, v, FileSource{
			Name: "/home/user/a_very_long_file_name_beyond_eight_three.txt",
			Data: []byte("long name body")})
	})
	fr, _ := newFatReader(dev.Bytes())
	for path, body := range map[string]string{
		"/boot/modules/console.wasm":                              "c",
		"/boot/modules/console2.wasm":                             "c2",
		"/home/user/a_very_long_file_name_beyond_eight_three.txt": "long name body",
	} {
		got, err := fr.readFile(path)
		if err != nil {
			t.Fatalf("lfn read %s: %v", path, err)
		}
		if string(got) != body {
			t.Fatalf("%s = %q", path, got)
		}
	}
	// The two colliding modules must carry DIFFERENT short names on disk,
	// and both long names must survive intact.
	c1, _, err := fr.find("/boot/modules/console.wasm")
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := fr.find("/boot/modules/console2.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if c1.short == c2.short {
		t.Fatalf("collision unresolved: both %q", string(c1.short[:]))
	}
}

func TestRootOverflowRejected(t *testing.T) {
	size := int64(8 << 20)
	dev := NewMemBlock(size)
	v, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ { // > 512 fixed root slots
		mustAdd(t, v, FileSource{Name: fmt.Sprintf("/f%03d.txt", i),
			Data: []byte("x")})
	}
	if err := v.Flush(); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("want root overflow error, got %v", err)
	}
}

func TestCapacityExhaustionReported(t *testing.T) {
	size := int64(8 << 20) // ~16k clusters of 512B after geometry
	dev := NewMemBlock(size)
	v, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	huge := make([]byte, 9<<20) // bigger than the whole volume
	if err := v.Add(FileSource{Name: "/vm/app", Data: huge}); err != nil {
		t.Fatalf("add: %v", err)
	}
	err = v.Flush()
	if err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("want image-full error, got %v", err)
	}
}

func TestDeterministicImages(t *testing.T) {
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fill := func(v *Volume) {
		mustAdd(t, v, FileSource{"/EFI/BOOT/BOOTX64.EFI", []byte("e"), mt})
		mustAdd(t, v, FileSource{"/vm/app", []byte("app"), mt})
		mustAdd(t, v, FileSource{"/etc/motd", []byte("hi"), mt})
	}
	a := buildVol(t, 64<<20, fill)
	b := buildVol(t, 64<<20, fill)
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("identical inputs produced different images")
	}
}

func TestZeroLengthFile(t *testing.T) {
	dev := buildVol(t, 64<<20, func(v *Volume) {
		mustAdd(t, v, FileSource{Name: "/vm/app", Data: nil})
	})
	fr, _ := newFatReader(dev.Bytes())
	e, ok, err := fr.find("/vm/app")
	if err != nil || !ok {
		t.Fatalf("find /vm/app: %v ok=%v", err, ok)
	}
	if e.size != 0 || e.start != 0 {
		t.Fatalf("empty file start=%d size=%d; want 0/0", e.start, e.size)
	}
}

func TestDosDateTimeClamps(t *testing.T) {
	d, tm := dosDateTime(time.Date(1960, 5, 4, 25, 70, 90, 0, time.UTC))
	if d>>9 != 0 {
		t.Fatalf("year not clamped to 1980: %d", d>>9)
	}
	d, _ = dosDateTime(time.Date(2200, 12, 31, 23, 59, 58, 0, time.UTC))
	if y := d >> 9; y != 2107-1980 {
		t.Fatalf("want year 2107, got %d", y+1980)
	}
	d, tm = dosDateTime(time.Date(2026, 8, 22, 13, 45, 51, 0, time.UTC))
	if wd, mo, day := decodeDos(d); wd != 2026 || mo != 8 || day != 22 ||
		decodeTime(tm) != "13:45:50" {
		t.Fatalf("round trip off: %04d-%02d-%02d %s", wd, mo, day, decodeTime(tm))
	}
}

func decodeDos(d uint16) (y, m, day int) {
	return int(d>>9) + 1980, int(d>>5) & 0xF, int(d & 0x1F)
}

func decodeTime(t uint16) string {
	return fmt.Sprintf("%02d:%02d:%02d", t>>11, (t>>5)&0x3F, (t&0x1F)*2)
}

// helpers ---------------------------------------------------------------

func mustAdd(t *testing.T, v *Volume, s FileSource) {
	t.Helper()
	if s.Mtime.IsZero() {
		s.Mtime = time.Unix(0, 0)
	}
	if err := v.Add(s); err != nil {
		t.Fatalf("Add %s: %v", s.Name, err)
	}
}
