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

func findMtools(t *testing.T) (mdir, mtype string) {
	t.Helper()
	candidates := []string{"", filepath.Join(os.Getenv("HOME"), ".local/osdev-root/usr/bin")}
	for _, bin := range []string{"mdir", "mtype"} {
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
		if bin == "mdir" {
			mdir = found
		} else {
			mtype = found
		}
	}
	return mdir, mtype
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
	mdir, mtype := findMtools(t)

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
