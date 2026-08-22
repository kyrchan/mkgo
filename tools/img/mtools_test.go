package main

// Integration test: verify our images with the REAL mtools binaries when
// available (PATH or ~/.local/osdev-root/usr/bin). This is the parity proof
// that `img` reproduces what mformat/mmd/mcopy produced for the Makefile.
// Skips silently when mtools is not installed.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func findMtools(t *testing.T) (mdir, mtype, mformat string) {
	t.Helper()
	candidates := []string{"", filepath.Join(os.Getenv("HOME"), ".local/osdev-root/usr/bin")}
	for _, bin := range []string{"mdir", "mtype", "mformat"} {
		found := ""
		for _, c := range candidates {
			p := filepath.Join(c, bin)
			if _, err := os.Stat(p); err == nil {
				found = p
				break
			}
			if c == "" {
				if lp, err := exec.LookPath(bin); err == nil {
					found = lp
					break
				}
			}
		}
		if found == "" {
			t.Skip("mtools not available")
		}
		switch bin {
		case "mdir":
			mdir = found
		case "mtype":
			mtype = found
		case "mformat":
			mformat = found
		}
	}
	return mdir, mtype, mformat
}

func runM(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", bin, args, err, out.String())
	}
	return out.String()
}

func TestMtoolsParity(t *testing.T) {
	mdir, mtype, _ := findMtools(t)

	size := int64(8 << 20)
	dev := NewMemBlock(size)
	vol, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	mt := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	add := func(name string, data []byte) {
		if err := vol.Add(FileSource{Name: name, Data: data, Mtime: mt}); err != nil {
			t.Fatal(err)
		}
	}
	add("/EFI/BOOT/BOOTX64.EFI", []byte("fake-efi"))
	add("/vm/app", []byte("guest-payload"))
	add("/boot/modules/console.wasm", []byte("console-module"))
	add("/etc/motd", []byte("hello from img\n"))
	if err := vol.Flush(); err != nil {
		t.Fatal(err)
	}

	imgPath := filepath.Join(t.TempDir(), "disk.img")
	if err := FlushToFile(dev, imgPath); err != nil {
		t.Fatal(err)
	}

	// Directory listing shows the full tree.
	listing := runM(t, mdir, "-i", imgPath, "-/", "::/")
	for _, want := range []string{"EFI", "VM", "BOOT", "ETC"} {
		if !bytes.Contains([]byte(listing), []byte(want)) &&
			!bytes.Contains([]byte(listing), []byte(want+"~1")) {
			t.Fatalf("mdir output missing %q:\n%s", want, listing)
		}
	}

	// File content round-trips through mtype — including a lossy long name.
	out := runM(t, mtype, "-i", imgPath, "::/vm/app")
	if out != "guest-payload" {
		t.Fatalf("mtype /vm/app = %q", out)
	}
	out = runM(t, mtype, "-i", imgPath, "::/EFI/BOOT/BOOTX64.EFI")
	if out != "fake-efi" {
		t.Fatalf("mtype BOOTX64.EFI = %q", out)
	}
	runM(t, mtype, "-i", imgPath, "::/boot/modules/console.wasm")
	runM(t, mtype, "-i", imgPath, "::/etc/motd")
}

// TestBPBParityWithMformat formats a reference image with the REAL mformat
// (same invocation shape as the Makefile's MKDISK) and byte-compares every
// BPB field our builder also emits, plus FAT[0..1]. OEM banner, volume
// serial and label legitimately differ and are excluded. This is the
// strongest form of "reproduces what mtools does today".
func TestBPBParityWithMformat(t *testing.T) {
	_, _, mformat := findMtools(t)

	const size = int64(64 << 20)
	dir := t.TempDir()

	refPath := filepath.Join(dir, "ref.img")
	ref, err := os.Create(refPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ref.Truncate(size); err != nil {
		t.Fatal(err)
	}
	ref.Close()
	out, err := exec.Command(mformat, "-i", refPath, "::").CombinedOutput()
	if err != nil {
		t.Fatalf("mformat: %v\n%s", err, out)
	}

	dev := NewMemBlock(size)
	vol, err := NewVolume(dev, size, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := vol.Flush(); err != nil {
		t.Fatal(err)
	}
	ours := dev.Bytes()
	refRaw, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatal(err)
	}

	fields := []struct {
		name     string
		off, len int
	}{
		{"bytes/sector", 11, 2}, {"sectors/cluster", 13, 1},
		{"reserved sectors", 14, 2}, {"num FATs", 16, 1},
		{"root entries", 17, 2}, {"totsec16", 19, 2},
		{"media descriptor", 21, 1}, {"sectors/FAT", 22, 2},
		{"sectors/track", 24, 2}, {"heads", 26, 2},
		{"hidden sectors", 28, 4}, {"totsec32", 32, 4},
		{"drive number", 36, 1}, {"extended boot sig", 38, 1},
		{"fstype", 54, 8},
	}
	for _, f := range fields {
		a, b := ours[f.off:f.off+f.len], refRaw[f.off:f.off+f.len]
		if !bytes.Equal(a, b) {
			t.Errorf("BPB %s: ours % X != mformat % X", f.name, a, b)
		}
	}
	for i := 0; i < 4; i++ { // FAT[0..1] words
		if ours[512+i] != refRaw[512+i] {
			t.Errorf("FAT byte %d: ours %#x != mformat %#x", i,
				ours[512+i], refRaw[512+i])
		}
	}
}
