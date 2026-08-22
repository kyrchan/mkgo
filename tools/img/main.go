// img: builds the bootable FAT16 disk image end-to-end, replacing the
// mtools (mformat/mmd/mcopy) pipeline in the Makefile.
//
// Layout reproduced from the mtools flow (see Makefile MKDISK/MKDISKP4):
//
//	/EFI/BOOT/BOOTX64.EFI   kernel PE32+ (from build/)
//	/vm/app                 guest payload
//	/boot/modules/*.wasm    service modules (console, login, ...)
//	/etc/*                  seeded config (templates: tools/img/templates)
//
// Usage:
//
//	img -o out.img -efi build/BOOTX64.EFI [-app payload] [-modules dir]
//	    [-seed dir] [-size 64M] [-label NO NAME] [-vmdk out.vmdk] [-vdi out.vdi]
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultSize = "64M"

func main() {
	var (
		out     = flag.String("o", "", "output raw image path (required)")
		sizeStr = flag.String("size", defaultSize, "image size (K/M/G suffixes)")
		efi     = flag.String("efi", "", "BOOTX64.EFI payload (required)")
		app     = flag.String("app", "", "single guest payload -> /vm/app")
		modules = flag.String("modules", "", "directory copied under /boot/modules")
		seed    = flag.String("seed", "", "directory copied under /etc")
		label   = flag.String("label", "NO NAME", "volume label (max 11 chars)")
		vmdkOut = flag.String("vmdk", "", "also write VMware monolithicFlat descriptor")
		vdiOut  = flag.String("vdi", "", "also write VirtualBox fixed VDI")
	)
	flag.Usage = usage
	flag.Parse()

	fail := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "img: "+f+"\n", a...)
		os.Exit(1)
	}
	if *out == "" || *efi == "" {
		usage()
		os.Exit(2)
	}
	size, err := parseSize(*sizeStr)
	if err != nil {
		fail("%v", err)
	}

	// Build in memory; flush once. Keeps failure modes atomic and testable.
	dev := NewMemBlock(size)
	vol, err := NewVolume(dev, size, *label)
	if err != nil {
		fail("%v", err)
	}

	efiData, err := os.ReadFile(*efi)
	if err != nil {
		fail("%v", err)
	}
	must := func(err error) {
		if err != nil {
			fail("%v", err)
		}
	}
	must(vol.MkDir("/EFI/BOOT"))
	must(vol.Add(FileSource{Name: "/EFI/BOOT/BOOTX64.EFI", Data: efiData,
		Mtime: modTime(*efi)}))
	must(vol.MkDir("/vm"))
	must(vol.MkDir("/boot/modules"))
	must(vol.MkDir("/etc"))

	if *app != "" {
		data, err := os.ReadFile(*app)
		if err != nil {
			fail("%v", err)
		}
		must(vol.Add(FileSource{Name: "/vm/app", Data: data, Mtime: modTime(*app)}))
	}
	if *modules != "" {
		must(vol.AddTree(*modules, "/boot/modules"))
	}
	if *seed != "" {
		must(vol.AddTree(*seed, "/etc"))
	}
	if err := vol.Flush(); err != nil {
		fail("%v", err)
	}
	if err := FlushToFile(dev, *out); err != nil {
		fail("%v", err)
	}

	if *vmdkOut != "" {
		if err := WriteVMDK(*out, *vmdkOut); err != nil {
			fail("vmdk: %v", err)
		}
	}
	if *vdiOut != "" {
		if err := WriteVDI(*out, *vdiOut); err != nil {
			fail("vdi: %v", err)
		}
	}

	fmt.Printf("img: %s  %s  spc=%d clusters=%d fat=%dx%d files=%d bytes=%d\n",
		*out, humanSize(size), vol.spc, vol.clusters, numFATs, vol.fatSecs,
		vol.fileCnt, vol.byteCnt)
	for _, extra := range []string{*vmdkOut, *vdiOut} {
		if extra != "" {
			fmt.Printf("img: wrote %s\n", extra)
		}
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: img -o out.img -efi build/BOOTX64.EFI [options]

  -o PATH        output raw FAT16 image (required)
  -efi PATH      BOOTX64.EFI payload written to /EFI/BOOT (required)
  -app PATH      single guest payload written to /vm/app
  -modules DIR   directory tree copied to /boot/modules
  -seed DIR      directory tree copied to /etc (see tools/img/templates)
  -size SIZE     total image size with K/M/G suffix (default 64M)
  -label NAME    volume label, max 11 chars (default "NO NAME")
  -vmdk PATH     also emit VMware monolithicFlat descriptor (+-flat.vmdk)
  -vdi PATH      also emit VirtualBox fixed VDI
`)
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	switch c := s[len(s)-1]; c | 0x20 {
	case 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return n * mult, nil
}

func humanSize(n int64) string {
	switch {
	case n%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", n/(1<<20))
	case n%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", n/(1<<10))
	default:
		return fmt.Sprintf("%d", n)
	}
}

// modTime returns the file's mtime, or the zero Time when unavailable
// (the volume builder then stamps 1980-01-01).
func modTime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}
