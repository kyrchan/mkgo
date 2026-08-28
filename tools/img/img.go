// tools/img -- Go disk image builder replacing the mtools shell pipeline.
// Builds FAT16 filesystem images with kernel + service modules + config.
//
// Usage:
//   img <out.img> <size_mb> <src:dst> [<src:dst> ...]
//
// Source paths are host files; dst paths are FAT16 absolute paths using
// forward slashes. Parent directories are created automatically. Long
// filenames get LFN directory entries (as mtools does) so UEFI's FAT
// driver matches them to their 8.3 aliases.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	sectorSize  = 512
	clusterSize = 2 * sectorSize // 1 KB (matches mtools)
	reservedSecs = 1
	numFATs     = 2
	rootEntries = 512
	mediaByte   = 0xF8
)

func fatSectors(dataClusters int) int {
	entriesPerSec := sectorSize / 2
	return (dataClusters + 2 + entriesPerSec - 1) / entriesPerSec
}

type image struct {
	size    int
	data    []byte
	fatOf   int
	fatSecs int
	dataOf  int
	clu2    int
	used83  map[string]bool
}

func newImage(sizeMB int) *image {
	size := sizeMB * 1024 * 1024
	data := make([]byte, size)

	totalSecs := size / sectorSize
	rootSecs := (rootEntries*32 + sectorSize - 1) / sectorSize
	// converge FAT size
	fatSecs := 100
	var dataSecs, dataClusters int
	for iter := 0; iter < 3; iter++ {
		dataSecs = totalSecs - reservedSecs - numFATs*fatSecs - rootSecs
		dataClusters = dataSecs / (clusterSize / sectorSize)
		fatSecs = fatSectors(dataClusters)
	}
	if dataSecs < 8 {
		panic("image too small")
	}

	img := &image{size: size, data: data, used83: make(map[string]bool)}

	bpb := data
	copy(bpb[0:3], []byte{0xEB, 0x3C, 0x90})
	copy(bpb[3:11], "MSDOS5.0")
	binary.LittleEndian.PutUint16(bpb[11:], sectorSize)
	bpb[13] = clusterSize / sectorSize
	binary.LittleEndian.PutUint16(bpb[14:], reservedSecs)
	bpb[16] = numFATs
	binary.LittleEndian.PutUint16(bpb[17:], rootEntries)
	if totalSecs <= 0xFFFF {
		binary.LittleEndian.PutUint16(bpb[19:], uint16(totalSecs))
	} else {
		binary.LittleEndian.PutUint16(bpb[19:], 0)
		binary.LittleEndian.PutUint32(bpb[32:], uint32(totalSecs))
	}
	bpb[21] = mediaByte
	binary.LittleEndian.PutUint16(bpb[22:], uint16(fatSecs))
	binary.LittleEndian.PutUint16(bpb[24:], 63)
	binary.LittleEndian.PutUint16(bpb[26:], 255)
	binary.LittleEndian.PutUint32(bpb[28:], 0)
	copy(bpb[54:62], "FAT16   ")

	img.fatOf = reservedSecs * sectorSize
	img.dataOf = img.fatOf + numFATs*fatSecs*sectorSize + rootSecs*sectorSize
	img.clu2 = 2
	img.fatSecs = fatSecs

	for f := 0; f < numFATs; f++ {
		off := img.fatOf + f*fatSecs*sectorSize
		binary.LittleEndian.PutUint16(data[off:], 0xFF00|mediaByte)
		binary.LittleEndian.PutUint16(data[off+2:], 0xFFFF)
	}
	return img
}

func (im *image) clusterOffset(c int) int {
	return im.dataOf + (c-im.clu2)*clusterSize
}

func (im *image) fatEntry(c int) uint16 {
	return binary.LittleEndian.Uint16(im.data[im.fatOf+c*2:])
}

func (im *image) setFatEntry(c int, v uint16) {
	binary.LittleEndian.PutUint16(im.data[im.fatOf+c*2:], v)
	binary.LittleEndian.PutUint16(im.data[im.fatOf+im.fatSecs*sectorSize+c*2:], v)
}

func (im *image) allocCluster() int {
	for c := im.clu2; c < im.clu2+im.fatSecs*sectorSize/2; c++ {
		if im.fatEntry(c) == 0 {
			im.setFatEntry(c, 0xFFFF)
			return c
		}
	}
	panic("FAT full")
}

func (im *image) dirEntries(cluster int) []entry {
	var data []byte
	if cluster == 0 {
		off := im.fatOf + numFATs*im.fatSecs*sectorSize
		data = im.data[off : off+rootEntries*32]
	} else {
		for c := cluster; c >= 2 && c < 0xFFF8; c = int(im.fatEntry(c)) {
			off := im.clusterOffset(c)
			data = append(data, im.data[off:off+clusterSize]...)
		}
	}
	var ents []entry
	for i := 0; i+32 <= len(data); i += 32 {
		rec := data[i : i+32]
		if rec[0] == 0x00 {
			break
		}
		if rec[0] == 0xE5 {
			continue
		}
		attr := rec[11]
		if attr&0x08 != 0 {
			continue
		}
		if attr&0x0F == 0x0F {
			continue // skip LFN entries in listings
		}
		name := trimFat(string(rec[0:8]))
		ext := trimFat(string(rec[8:11]))
		full := name
		if ext != "" {
			full += "." + ext
		}
		ents = append(ents, entry{
			name:    strings.ToUpper(full),
			attr:    attr,
			cluster: int(binary.LittleEndian.Uint16(rec[26:28])),
			size:    int(binary.LittleEndian.Uint32(rec[28:32])),
			dir:     attr&0x10 != 0,
		})
	}
	return ents
}

func trimFat(s string) string {
	s = strings.TrimRight(s, " ")
	s = strings.TrimRight(s, "\x00")
	return s
}

func (im *image) dirFind(cluster int, name string) *entry {
	name = strings.ToUpper(name)
	for _, e := range im.dirEntries(cluster) {
		if e.name == name {
			return &entry{name: e.name, attr: e.attr, cluster: e.cluster, size: e.size, dir: e.dir}
		}
	}
	return nil
}

// to83Canonical returns the canonical 8.3 form of name (uppercase, no
// tilde). Used to decide whether an LFN entry is needed.
func to83Canonical(long string) string {
	base := long
	ext := ""
	if i := strings.LastIndex(long, "."); i >= 0 {
		base = long[:i]
		ext = long[i+1:]
	}
	clean := func(s string, max int) string {
		var b strings.Builder
		for _, r := range s {
			if b.Len() >= max {
				break
			}
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(unicode.ToUpper(r))
			}
		}
		return b.String()
	}
	b := clean(base, 8)
	if ext != "" {
		b += "." + clean(ext, 3)
	}
	return b
}

func (im *image) make83(long string) string {
	base := long
	ext := ""
	if i := strings.LastIndex(long, "."); i >= 0 {
		base = long[:i]
		ext = long[i+1:]
	}
	clean := func(s string, max int) string {
		var b strings.Builder
		for _, r := range s {
			if b.Len() >= max {
				break
			}
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(unicode.ToUpper(r))
			}
		}
		return b.String()
	}
	basename := clean(base, 6)
	extension := clean(ext, 3)
	for n := 1; n <= 9999; n++ {
		var cand string
		if n <= 9 {
			cand = basename + "~" + strconv.Itoa(n)
		} else {
			cand = basename[:5] + "~" + strconv.Itoa(n)
		}
		if extension != "" {
			cand += "." + extension
		}
		if !im.used83[cand] {
			im.used83[cand] = true
			return cand
		}
	}
	panic("too many name collisions")
}

// findFreeSlots finds n contiguous free directory slots at or after
// offset *start; returns -1 if none found.
func (im *image) findFreeSlots(cluster, n int) (int, []byte) {
	var recs []byte
	if cluster == 0 {
		off := im.fatOf + numFATs*im.fatSecs*sectorSize
		recs = im.data[off : off+rootEntries*32]
	} else {
		c := cluster
		for {
			nxt := int(im.fatEntry(c))
			if nxt >= 0xFFF8 {
				break
			}
			c = nxt
		}
		off := im.clusterOffset(c)
		recs = im.data[off : off+clusterSize]
	}
	for i := 0; i+32*n <= len(recs); i += 32 {
		ok := true
		for j := 0; j < n; j++ {
			if recs[i+j*32] != 0x00 && recs[i+j*32] != 0xE5 {
				ok = false
				break
			}
		}
		if ok {
			return i, recs
		}
	}
	return -1, recs
}

func (im *image) addDirEntry(cluster int, name string, attr uint8, firstClu int, size int) error {
	upper := strings.ToUpper(name)
	// Determine if LFN is needed: only when the long name differs from
	// a valid 8.3 representation. Names like "BOOTX64.EFI" need no LFN.
	needsLFN := upper != to83Canonical(name)
	var short string
	if needsLFN {
		short = im.make83(name) // ~1 form, unique per image
	} else {
		short = upper
		im.used83[short] = true
	}
	pad := func(s string, n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = 0x20
		}
		copy(b, s)
		return b
	}

	// Count LFN entries needed.
	numLFN := 0
	if needsLFN {
		numLFN = (len(name) + 12) / 13
	}

	slot, recs := im.findFreeSlots(cluster, numLFN+1)
	if slot < 0 {
		if cluster != 0 {
			newClu := im.allocCluster()
			im.setFatEntry(cluster, uint16(newClu))
			off := im.clusterOffset(newClu)
			for i := range im.data[off : off+clusterSize] {
				im.data[off+i] = 0
			}
			slot = 0
			recs = im.data[off : off+clusterSize]
		} else {
			return fmt.Errorf("root directory full")
		}
	}

	// Write LFN entries (in reverse order: last sequence first).
	if numLFN > 0 {
		lfnBase := short
		lfnExt := ""
		if i := strings.LastIndex(short, "."); i >= 0 {
			lfnBase = short[:i]
			lfnExt = short[i+1:]
		}
		chk := checksum83(lfnBase, lfnExt)
		for idx := numLFN - 1; idx >= 0; idx-- {
			rec := make([]byte, 32)
			seq := idx + 1
			if idx == numLFN-1 {
				seq |= 0x40
			}
			rec[0] = byte(seq)
			rec[11] = 0x0F
			rec[13] = chk
			// Fill 13 UTF-16 code units.
			chars := make([]rune, 13)
			for k := 0; k < 13; k++ {
				pos := idx*13 + k
				if pos < len(name) {
					chars[k] = rune(name[pos])
				} else if pos == len(name) {
					chars[k] = 0x0000
				} else {
					chars[k] = 0xFFFF
				}
			}
			// Layout: 5 chars, 6 chars, 2 chars.
			putW := func(off int, r rune) {
				if r == 0xFFFF {
					rec[off], rec[off+1] = 0xFF, 0xFF
				} else {
					rec[off] = byte(r)
					rec[off+1] = byte(r >> 8)
				}
			}
			for k := 0; k < 5; k++ {
				putW(1+k*2, chars[k])
			}
			for k := 0; k < 6; k++ {
				putW(14+k*2, chars[5+k])
			}
			for k := 0; k < 2; k++ {
				putW(28+k*2, chars[11+k])
			}
			copy(recs[slot+(numLFN-1-idx)*32:slot+(numLFN-1-idx)*32+32], rec)
		}
	}

	// Write 8.3 short entry after LFN entries.
	shortSlot := slot + numLFN*32
	base := short
	ext := ""
	if i := strings.LastIndex(short, "."); i >= 0 {
		base = short[:i]
		ext = short[i+1:]
	}
	rec := make([]byte, 32)
	copy(rec[0:8], pad(base, 8))
	copy(rec[8:11], pad(ext, 3))
	rec[11] = attr
	binary.LittleEndian.PutUint16(rec[26:28], uint16(firstClu))
	binary.LittleEndian.PutUint32(rec[28:32], uint32(size))
	copy(recs[shortSlot:shortSlot+32], rec)
	return nil
}

func checksum83(name, ext string) byte {
	var sum byte
	padded := name
	for len(padded) < 8 {
		padded += " "
	}
	if len(padded) > 8 {
		padded = padded[:8]
	}
	full := padded + ext
	for len(full) < 11 {
		full += " "
	}
	for i := 0; i < 11; i++ {
		sum = ((sum & 1) << 7) + (sum >> 1) + full[i]
	}
	return sum
}

func (im *image) mkdir(path string) (int, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	cluster := 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		if ex := im.dirFind(cluster, p); ex != nil {
			cluster = ex.cluster
			continue
		}
		newClu := im.allocCluster()
		off := im.clusterOffset(newClu)
		for i := range im.data[off : off+clusterSize] {
			im.data[off+i] = 0
		}
		if err := im.addDirEntry(cluster, p, 0x10, newClu, 0); err != nil {
			return 0, err
		}
		// "." and ".." entries.
		dot := make([]byte, 32)
		copy(dot[0:8], ".       ")
		copy(dot[8:11], "   ")
		dot[11] = 0x10
		binary.LittleEndian.PutUint16(dot[26:28], uint16(newClu))
		copy(im.data[off:off+32], dot)
		ddot := make([]byte, 32)
		copy(ddot[0:8], "..      ")
		copy(ddot[8:11], "   ")
		ddot[11] = 0x10
		binary.LittleEndian.PutUint16(ddot[26:28], uint16(cluster))
		copy(im.data[off+32:off+64], ddot)
		cluster = newClu
	}
	return cluster, nil
}

func (im *image) writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if dir == "/" || dir == "." {
		dir = ""
	}
	parentClu := 0
	if dir != "" {
		var err error
		parentClu, err = im.mkdir(dir)
		if err != nil {
			return err
		}
	}
	size := len(data)
	numClus := (size + clusterSize - 1) / clusterSize
	if numClus == 0 {
		numClus = 1
	}
	chain := make([]int, 0, numClus)
	for i := 0; i < numClus; i++ {
		chain = append(chain, im.allocCluster())
	}
	for i := 0; i < len(chain)-1; i++ {
		im.setFatEntry(chain[i], uint16(chain[i+1]))
	}
	im.setFatEntry(chain[len(chain)-1], 0xFFFF)
	for i, c := range chain {
		off := im.clusterOffset(c)
		start := i * clusterSize
		end := start + clusterSize
		if end > size {
			end = size
		}
		copy(im.data[off:off+clusterSize], data[start:end])
	}
	return im.addDirEntry(parentClu, base, 0x20, chain[0], size)
}

func (im *image) flush(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(im.data)
	return err
}

type entry struct {
	name    string
	attr    uint8
	cluster int
	size    int
	dir     bool
}

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: img <out.img> <size_mb> <src:dst> [src:dst ...]")
		os.Exit(2)
	}
	sizeMB := 0
	fmt.Sscanf(os.Args[2], "%d", &sizeMB)
	if sizeMB <= 0 {
		sizeMB = 64
	}
	img := newImage(sizeMB)

	for _, a := range os.Args[3:] {
		i := strings.IndexByte(a, ':')
		if i < 0 {
			fmt.Fprintf(os.Stderr, "bad arg %q (want src:dst)\n", a)
			os.Exit(2)
		}
		src, dst := a[:i], a[i+1:]
		data, err := os.ReadFile(src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		if err := img.writeFile(dst, data); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}

	if err := img.flush(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "flush:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "img: %s (%d MB, %d bytes)\n", os.Args[1], sizeMB, img.size)
}
