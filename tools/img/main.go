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
//	img -o out.img -efi build/BOOTX64.EFI [options]
//	    [-app payload] [-file src:dst ...] [-modules dir] [-seed dir]
//	    [-size 64M] [-label NO NAME] [-vmdk out.vmdk] [-vdi out.vdi] [-kfs-seed dir]
//
//	img -in disk.img -read /path/to/file   # read a file back from a FAT16 image
package main

import (
	"flag"
	"fmt"
	"kernel.lane/kfs"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultSize = "64M"

// fileFlag implements flag.Value for the repeatable -file src:dst option.
type fileFlag []string

func (f *fileFlag) String() string { return strings.Join(*f, ",") }

func (f *fileFlag) Set(v string) error {
	if strings.IndexByte(v, ':') < 0 {
		return fmt.Errorf("img: -file wants src:dst, got %q", v)
	}
	*f = append(*f, v)
	return nil
}

func main() {
	var (
		out     = flag.String("o", "", "output raw image path (required for write mode)")
		sizeStr = flag.String("size", defaultSize, "image size (K/M/G suffixes)")
		efi     = flag.String("efi", "", "BOOTX64.EFI payload written to /EFI/BOOT (required for write mode)")
		app     = flag.String("app", "", "single guest payload written to /vm/app")
		files   fileFlag
		modules = flag.String("modules", "", "directory tree copied to /boot/modules")
		seed    = flag.String("seed", "", "directory tree copied to /etc (see tools/img/templates)")
		label   = flag.String("label", "NO NAME", "volume label (max 11 chars)")
		vmdkOut = flag.String("vmdk", "", "also write VMware monolithicFlat descriptor")
		vdiOut  = flag.String("vdi", "", "also write VirtualBox fixed VDI")
		inImg   = flag.String("in", "", "input image to read from (read mode)")
		read    = flag.String("read", "", "path to read from input image (use with -in)")
		kfsSeed = flag.String("kfs-seed", "", "host directory tree to place in a KFS1 volume (see -kfs-prefix)")
		kfsPre  = flag.String("kfs-prefix", "/", "path inside the KFS1 volume that -kfs-seed is rooted at")
	)
	flag.Var(&files, "file", "individual src:dst file placement (repeatable)")
	flag.Usage = usage
	flag.Parse()

	if *inImg != "" {
		runRead(*inImg, *read)
		return
	}

	fail := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "img: "+f+"\n", a...)
		os.Exit(1)
	}
	// A bootable image needs -efi. A DATA-ONLY image (a KFS1 volume with no
	// loader on it) does not: that is what a gate seeds as a second block
	// device, where an EFI payload would be dead weight.
	if *out == "" || (*efi == "" && *kfsSeed == "") {
		usage()
		os.Exit(2)
	}
	size, err := parseSize(*sizeStr)
	if err != nil {
		fail("%v", err)
	}

	// Build in memory; flush once. Keeps failure modes atomic and testable.
	dev := NewMemBlock(size)

	// When a KFS1 volume is requested, the FAT16 volume is sized to the first
	// half of the image and KFS1 starts at that boundary. fs.wasm mounts KFS1
	// and no longer formats implicitly, so a gate image that boots it must
	// already carry a valid volume -- but the loader still needs FAT16 at
	// sector 0 for BOOTX64.EFI and /boot on the ESP. Hence two volumes on one
	// disk rather than one overwriting the other.
	//
	// Half the image, not a fixed offset: FAT16 needs a few thousand clusters
	// to be legal at all, so a small fixed base (1 MiB) cannot hold the boot
	// volume and pickGeometry rejects it outright.
	kfsSector := int64(0)
	fatSize := size
	if *kfsSeed != "" {
		// The boundary comes from kfs.SplitSectors so the guest, which probes
		// with kfs.FindVolume, derives exactly the same sector. This used to be
		// inline here, which is precisely how the two sides drift apart.
		var err error
		if kfsSector, err = kfs.SplitSectors(size); err != nil {
			fail("%v", err)
		}
		fatSize = kfsSector * kfs.SectorSize
	}

	vol, err := NewVolume(dev, fatSize, *label)
	if err != nil {
		fail("%v", err)
	}

	var efiData []byte
	if *efi != "" {
		efiData, err = os.ReadFile(*efi)
		if err != nil {
			fail("%v", err)
		}
	}
	must := func(err error) {
		if err != nil {
			fail("%v", err)
		}
	}
	if *efi != "" {
		must(vol.MkDir("/EFI/BOOT"))
		must(vol.Add(FileSource{Name: "/EFI/BOOT/BOOTX64.EFI", Data: efiData,
			Mtime: modTime(*efi)}))
	}
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
	// Positional arguments are accepted as src:dst placements for
	// backward compatibility with the Makefile's disk rules, which pass
	// payloads as bare "src:/vm/app" (etc.) tokens rather than via -file.
	// Each positional is parsed exactly like a -file entry; the -file flag
	// remains the preferred form. (Without this, flag.Args() are silently
	// dropped by flag.Parse() and the payload never lands on the image --
	// the loader then hits "file not found" and halts.)
	placements := append(files, flag.Args()...)
	for _, spec := range placements {
		src, dst, err := parseMapping(spec)
		if err != nil {
			fail("%v", err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			fail("%v", err)
		}
		must(vol.Add(FileSource{Name: dst, Data: data, Mtime: modTime(src)}))
	}
	if err := vol.Flush(); err != nil {
		fail("%v", err)
	}
	// Seed AFTER the FAT volume is flushed, so the two never overlap: the
	// KFS1 volume is written past the end of the FAT16 volume.
	if kfsSector > 0 {
		if err := seedKFS(dev, kfsSector, *kfsPre, *kfsSeed); err != nil {
			fail("%v", err)
		}
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
       img -in disk.img -read /path/to/file

  Write mode (build a FAT16 image):
  -o PATH        output raw FAT16 image (required)
  -efi PATH      BOOTX64.EFI payload written to /EFI/BOOT (required)
  -app PATH      single guest payload written to /vm/app
  -file SRC:DST  individual file placement (repeatable)
  SRC:DST        positional alias for -file (repeatable); the Makefile
                 disk rules pass payloads this way
  -modules DIR   directory tree copied to /boot/modules
  -seed DIR      directory tree copied to /etc (see tools/img/templates)
  -size SIZE     total image size with K/M/G suffix (default 64M)
  -label NAME    volume label, max 11 chars (default "NO NAME")
  -vmdk PATH     also emit VMware monolithicFlat descriptor (+-flat.vmdk)
  -vdi PATH      also emit VirtualBox fixed VDI
  -kfs-seed DIR  host directory tree placed in a KFS1 volume past the FAT16 volume
  -kfs-prefix P  path inside the KFS1 volume that -kfs-seed is rooted at (default /)

  Read mode (extract a file from an image):
  -in PATH       input image (required for read mode)
  -read PATH     FAT path to read (required for read mode)
  `)
}

// runRead extracts a single file from a FAT16 image and writes it to stdout.
// Uses the independent fatReader rather than the builder's Volume so the
// read path is a genuine cross-check of the write path.
func runRead(imgPath, fatPath string) {
	raw, err := os.ReadFile(imgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "img: %v\n", err)
		os.Exit(1)
	}
	if fatPath == "" {
		fmt.Fprintln(os.Stderr, "img: -read requires a FAT path")
		os.Exit(2)
	}
	fr, err := newFatReader(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "img: %v\n", err)
		os.Exit(1)
	}
	data, err := fr.readFile(fatPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "img: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "img: %v\n", err)
		os.Exit(1)
	}
}

// parseMapping splits a "src:dst" flag value into its two halves,
// rejecting malformed values. Used by -file and tested directly.
func parseMapping(v string) (src, dst string, err error) {
	i := strings.IndexByte(v, ':')
	if i < 0 {
		return "", "", fmt.Errorf("img: -file wants src:dst, got %q", v)
	}
	src, dst = v[:i], v[i+1:]
	if src == "" {
		return "", "", fmt.Errorf("img: -file src is empty in %q", v)
	}
	if dst == "" {
		return "", "", fmt.Errorf("img: -file dst is empty in %q", v)
	}
	return src, dst, nil
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
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	if n > (1<<63-1)/mult {
		return 0, fmt.Errorf("size %d%s overflows int64", n, multSuffix(mult))
	}
	return n * mult, nil
}

func multSuffix(m int64) string {
	switch m {
	case 1:
		return ""
	case 1 << 10:
		return "K"
	case 1 << 20:
		return "M"
	case 1 << 30:
		return "G"
	}
	return ""
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
