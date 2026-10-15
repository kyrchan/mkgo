package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	lib "kernel.lane/guests/lib"
)

var debugFat16 = false

// FAT16 over the §3 block window (abi/ABI.md §3): 512-byte sectors,
// 8.3 short names only, fixed root directory, whole-directory
// load/modify/save model (small dirs; simple and correct beats clever).
// Scope per AGENTS.md Phase 5: /etc, /home/<user>, /boot/modules.
// Long filenames are out of v1 scope.

const (
	secPerClus  = 2 // 1 KiB clusters → ~7.9k on 8 MiB: solidly FAT16
	numFATs     = 2
	rootEntries = 512
	rootSecs    = rootEntries * 32 / 512
	reserved    = 1
	clusBytes   = secPerClus * 512

	attrReadOnly = 0x01
	attrHidden   = 0x02
	attrSystem   = 0x04
	attrVolume   = 0x08
	attrDir      = 0x10
	attrArchive  = 0x20

	eocFAT16 uint16 = 0xFFFF
	freeClus uint16 = 0x0000
)

var (
	ErrNoEntry = errors.New("fat16: no such file or directory")
	ErrExists  = errors.New("fat16: already exists")
	ErrNotDir  = errors.New("fat16: not a directory")
	ErrIsDir   = errors.New("fat16: is a directory")
	ErrNoSpace = errors.New("fat16: no space left")
	ErrBadName = errors.New("fat16: invalid 8.3 name")
	ErrNotEmpt = errors.New("fat16: directory not empty")
	ErrRange   = errors.New("fat16: offset beyond EOF")
)

// BlockDev is the storage surface; *BlockWindow implements it.
type BlockDev interface {
	Read(lba uint64, buf []byte) error
	Write(lba uint64, buf []byte) error
	Geometry() (blkSize uint32, numBlocks uint64)
}

var _ BlockDev = (*BlockWindow)(nil)

// StatInfo is one directory record.
type StatInfo struct {
	Name    string // long name if LFN present, else dotted 8.3 ("HELLO.TXT")
	Attr    byte
	Size    uint32
	Cluster uint32
}

func (s StatInfo) IsDir() bool { return s.Attr&attrDir != 0 }

type FAT struct {
	dev      BlockDev
	fatSz    uint32
	rootLba  uint32
	dataLba  uint32
	clusters uint32
	fat      []byte // cached first FAT (fatSz*512 bytes)
	// rootData caches the FAT16 root directory sectors so they survive
	// KFS format/seeding writes that overlap the FAT table area (the
	// KFS log starts at sector 1, same region as the FAT; the root
	// dir at rootLba is not cached elsewhere and would be clobbered
	// once the log grows past it during large-file seeding).
	rootData []byte
	// fileCache caches resolved file entries (cluster chain + size) by path
	// so repeated READs on the same file skip the directory walk.
	fileCache map[string]fileEntry
	// sectorCache caches data-area sectors so file reads still work
	// after FormatKFS overwrites the FAT16 data area with the KGS log.
	// Populated on first access during Mount-time directory traversal.
	sectorCache map[uint64][]byte
}

type fileEntry struct {
	chain []uint32
	size  uint32
	data  []byte // cached file content (populated by fatPreReadData)
}

// ---- geometry / format / mount ----

func fatSizeFor(totalSectors uint32) (fatSz, clusters uint32) {
	fatSz = 1
	for i := 0; i < 64; i++ {
		c := (totalSectors - reserved - numFATs*fatSz - rootSecs) / secPerClus
		want := (c + 2 + 255) / 256 // ceil((c+2)*2/512)
		if want <= fatSz {
			return fatSz, c
		}
		fatSz = want
	}
	return fatSz, (totalSectors - reserved - numFATs*fatSz - rootSecs) / secPerClus
}

// Format lays down a fresh empty FAT16 filesystem on dev.
func Format(dev BlockDev, label string) error {
	bs, nb := dev.Geometry()
	if bs != 512 || nb < 640 {
		return errors.New("fat16: unsupported geometry")
	}
	total := uint32(nb)
	fatSz, clusters := fatSizeFor(total)
	if clusters < 0xFF5 || clusters > 0xFFF4 {
		return errors.New("fat16: cluster count outside FAT16 range")
	}

	var boot [512]byte
	copy(boot[0:], []byte{0xEB, 0x3C, 0x90})
	copy(boot[3:11], "KERNELN ")
	lib.Put16(boot[11:], 512)
	boot[13] = secPerClus
	lib.Put16(boot[14:], reserved)
	boot[16] = numFATs
	lib.Put16(boot[17:], rootEntries)
	if total <= 0xFFFF {
		lib.Put16(boot[19:], uint16(total))
	} else {
		lib.Put32(boot[32:], total)
	}
	boot[21] = 0xF8
	lib.Put16(boot[22:], uint16(fatSz))
	lib.Put16(boot[24:], 32) // sectors/track (cosmetic)
	lib.Put16(boot[26:], 2)  // heads (cosmetic)
	boot[36] = 0x80
	boot[38] = 0x29 // extended boot signature
	lib.Put32(boot[39:], 0x4B45524E)
	lbl := [11]byte{'N', 'O', ' ', 'N', 'A', 'M', 'E', ' ', ' ', ' ', ' '}
	if label != "" {
		l, err := validate83(label)
		if err != nil {
			return err
		}
		lbl = l
	}
	copy(boot[43:54], lbl[:])
	copy(boot[54:62], "FAT16   ")
	boot[510], boot[511] = 0x55, 0xAA
	if err := dev.Write(0, boot[:]); err != nil {
		return err
	}

	z := make([]byte, 512)
	end := uint32(reserved+numFATs*int(fatSz)) + rootSecs
	for lba := uint32(reserved); lba < end && lba < total; lba++ {
		if err := dev.Write(uint64(lba), z); err != nil {
			return err
		}
	}
	// FAT[0] = media descriptor | 0xFF00, FAT[1] = EOC (DOS/mtools
	// convention; mtools refuses volumes without them).
	var fatHead [4]byte
	lib.Put16(fatHead[0:], 0xFFF8|uint16(boot[21]))
	lib.Put16(fatHead[2:], eocFAT16)
	for n := uint32(0); n < numFATs; n++ {
		var sec [512]byte
		if err := dev.Read(uint64(reserved)+uint64(n)*uint64(fatSz), sec[:]); err != nil {
			return err
		}
		copy(sec[0:4], fatHead[:])
		if err := dev.Write(uint64(reserved)+uint64(n)*uint64(fatSz), sec[:]); err != nil {
			return err
		}
	}
	return nil
}

// Mount validates the BPB and loads the first FAT.
func Mount(dev BlockDev) (*FAT, error) {
	var boot [512]byte
	if err := dev.Read(0, boot[:]); err != nil {
		return nil, err
	}
	if boot[510] != 0x55 || boot[511] != 0xAA {
		return nil, errors.New("fat16: bad boot signature")
	}
	if string(boot[54:62]) != "FAT16   " {
		return nil, errors.New("fat16: not a FAT16 volume")
	}
	if lib.Get16(boot[11:]) != 512 || boot[13] != secPerClus ||
		lib.Get16(boot[17:]) != rootEntries {
		return nil, errors.New("fat16: unexpected BPB values")
	}
	fatSz := lib.Get16(boot[22:])
	total := uint32(lib.Get16(boot[19:]))
	if total == 0 {
		total = lib.Get32(boot[32:])
	}
	f := &FAT{
		dev:         dev,
		fatSz:       uint32(fatSz),
		rootLba:     reserved + uint32(fatSz)*numFATs,
		fileCache:   make(map[string]fileEntry),
		sectorCache: make(map[uint64][]byte),
	}
	f.dataLba = f.rootLba + rootSecs
	f.clusters = (total - f.dataLba) / secPerClus
	f.fat = make([]byte, int(f.fatSz)*512)
	if err := dev.Read(reserved, f.fat); err != nil {
		return nil, err
	}
	f.rootData = make([]byte, rootSecs*512)
	if err := dev.Read(uint64(f.rootLba), f.rootData); err != nil {
		return nil, err
	}
	return f, nil
}

// ---- FAT chain primitives ----

func (f *FAT) fatGet(c uint32) uint16 { return lib.Get16(f.fat[c*2:]) }
func (f *FAT) fatSet(c uint32, v uint16) {
	lib.Put16(f.fat[c*2:], v)
}

// flushFat writes both FAT copies back to the device.
func (f *FAT) flushFat() error {
	dup := make([]byte, len(f.fat))
	copy(dup, f.fat)
	if err := f.dev.Write(uint64(reserved), f.fat); err != nil {
		return err
	}
	return f.dev.Write(uint64(reserved)+uint64(f.fatSz), dup)
}

func (f *FAT) allocOne() (uint32, error) {
	for c := uint32(2); c < f.clusters+2; c++ {
		if f.fatGet(c) == freeClus {
			f.fatSet(c, eocFAT16)
			if err := f.zeroCluster(c); err != nil {
				return 0, err
			}
			return c, nil
		}
	}
	return 0, ErrNoSpace
}

// zeroCluster clears one data cluster. Every runtime allocation starts
// zeroed so recycled storage never leaks stale bytes through sparse
// write gaps (cross-file disclosure class; FAT stores no owners).
func (f *FAT) zeroCluster(c uint32) error {
	z := make([]byte, secPerClus*512)
	return f.dev.Write(f.clusLBA(c), z)
}

func (f *FAT) clusLBA(c uint32) uint64 {
	return uint64(f.dataLba + (c-2)*secPerClus)
}

// chainOf lists the cluster ids reachable from start.
func (f *FAT) chainOf(start uint32) []uint32 {
	var out []uint32
	c := start
	guard := int(f.clusters) + 4
	for c >= 2 && c < f.clusters+2 && guard > 0 {
		out = append(out, c)
		next := f.fatGet(c)
		if next < 2 || next >= 0xFFF8 {
			break
		}
		c = uint32(next)
		guard--
	}
	return out
}

// extendChain appends n clusters at the tail of an existing chain
// (chain must be non-empty).
func (f *FAT) extendChain(chain []uint32, n uint32) ([]uint32, error) {
	out := chain
	for i := uint32(0); i < n; i++ {
		c, err := f.allocOne()
		if err != nil {
			return nil, err
		}
		f.fatSet(out[len(out)-1], uint16(c))
		out = append(out, c)
	}
	return out, nil
}

func (f *FAT) freeChain(start uint32) {
	for _, c := range f.chainOf(start) {
		f.fatSet(c, freeClus)
	}
}

// ---- names ----

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
}

func valid83Char(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case strings.ContainsRune("!#$%&'()-@^_`{}~", r):
		return true
	}
	return false
}

// validate83 converts "readme.txt" to its 11-byte short-name form.
// Long extensions are truncated to 3 chars (standard 8.3 behaviour) so
// Go wasm modules ("hello.wasm") remain reachable even though the runtime
// does not implement LFN (v1 scope: 8.3 only).
func validate83(name string) ([11]byte, error) {
	var out [11]byte
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." || len(name) > 12 {
		return out, ErrBadName
	}
	base, ext := name, ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		base, ext = name[:i], name[i+1:]
	}
	if base == "" {
		// An empty base is not a legal 8.3 short name. Mapping ".hidden" onto
		// the extension field yields the illegal ".HID", and mapping it onto
		// the base field instead would make it collide with "hidden" on
		// export -- silent data loss is worse than a refusal.
		return out, ErrBadName
	}
	if len(base) > 8 || strings.Contains(base, ".") {
		return out, ErrBadName
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	if reservedNames[base] {
		return out, ErrBadName
	}
	for _, r := range base + ext {
		if !valid83Char(r) {
			return out, ErrBadName
		}
	}
	copy(out[0:], base)
	for i := len(base); i < 8; i++ {
		out[i] = ' '
	}
	copy(out[8:], ext)
	for i := 8 + len(ext); i < 11; i++ {
		out[i] = ' '
	}
	return out, nil
}

func from83(raw []byte) string {
	base := strings.TrimRight(string(raw[0:8]), " ")
	ext := strings.TrimRight(string(raw[8:11]), " ")
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// ---- directories: load whole area, mutate in memory, save wholesale ----

type dirData struct {
	isRoot bool
	chain  []uint32 // subdir clusters (empty for root)
	lbas   []uint32 // concrete sector list backing data
	data   []byte   // len(lbas)*512
}

func decodeLFNEntry(data []byte, off int) string {
	r := func(b byte, b2 byte) rune {
		return rune(uint16(b) | uint16(b2)<<8)
	}
	positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
	var s string
	for _, p := range positions {
		idx := off + p
		c := r(data[idx], data[idx+1])
		if c == 0 {
			if len(s) > 0 {
				return s
			}
		}
		s += string(c)
	}
	return s
}

func collectLFN(data []byte, off int) (string, int) {
	var parts []string
	for off+32 <= len(data) && isLFN(data, off) {
		seq := data[off]
		if seq == 0 {
			break
		}
		parts = append([]string{decodeLFNEntry(data, off)}, parts...)
		off += 32
	}
	return strings.Join(parts, ""), off
}

func (d *dirData) find(name11 [11]byte) (int, bool) {
	off := 0
	for off+32 <= len(d.data) {
		if d.data[off] == 0x00 {
			break
		}
		if isLFN(d.data, off) {
			_, skipped := collectLFN(d.data, off)
			off = skipped
			continue
		}
		if d.data[off] == 0xE5 || (d.data[off+11]&attrVolume) != 0 {
			off += 32
			continue
		}
		if bytes.Equal(d.data[off:off+11], name11[:]) {
			return off, true
		}
		off += 32
	}
	return -1, false
}

func (d *dirData) findByLfn(target string) (int, bool) {
	off := 0
	for off+32 <= len(d.data) {
		if d.data[off] == 0x00 {
			break
		}
		lfn := ""
		if isLFN(d.data, off) {
			var skipped int
			lfn, skipped = collectLFN(d.data, off)
			off = skipped
			if off+32 > len(d.data) {
				break
			}
		}
		if d.data[off] == 0xE5 || (d.data[off+11]&attrVolume) != 0 {
			off += 32
			continue
		}
		shortName := from83(d.data[off : off+32])
		candidate := lfn
		if candidate == "" {
			candidate = shortName
		}
		if strings.EqualFold(candidate, target) {
			return off, true
		}
		off += 32
	}
	return -1, false
}

// freeSlot claims the first deleted-or-terminator slot index.
func (d *dirData) freeSlot() (int, bool) {
	for off := 0; off+32 <= len(d.data); off += 32 {
		first := d.data[off]
		if first == 0xE5 {
			return off, true
		}
		if first == 0x00 {
			if off+64 <= len(d.data) {
				return off, true // keep the following zero as terminator
			}
			break // no room for a new terminator in this dir
		}
	}
	return -1, false
}

func (d *dirData) list() []StatInfo {
	var out []StatInfo
	off := 0
	for off+32 <= len(d.data) {
		if d.data[off] == 0x00 {
			break
		}
		if isLFN(d.data, off) {
			lfn, skipped := collectLFN(d.data, off)
			off = skipped
			if off+32 > len(d.data) {
				break
			}
			name := lfn
			if name == "" {
				name = from83(d.data[off : off+32])
			}
			raw := d.data[off : off+32]
			si := StatInfo{
				Name:    name,
				Attr:    raw[11],
				Size:    lib.Get32(raw[28:]),
				Cluster: uint32(lib.Get16(raw[26:])),
			}
			if si.Name == "." || si.Name == ".." {
				off += 32
				continue
			}
			out = append(out, si)
			off += 32
			continue
		}
		if d.data[off] == 0xE5 || (d.data[off+11]&attrVolume) != 0 {
			off += 32
			continue
		}
		raw := d.data[off : off+32]
		si := StatInfo{
			Name:    from83(raw),
			Attr:    raw[11],
			Size:    lib.Get32(raw[28:]),
			Cluster: uint32(lib.Get16(raw[26:])),
		}
		if si.Name == "." || si.Name == ".." {
			off += 32
			continue
		}
		out = append(out, si)
		off += 32
	}
	return out
}

// loadDir reads a directory's full data area.
func (f *FAT) loadDir(isRoot bool, chain []uint32) (*dirData, error) {
	d := &dirData{isRoot: isRoot, chain: chain}
	if isRoot {
		d.lbas = make([]uint32, rootSecs)
		for i := range d.lbas {
			d.lbas[i] = f.rootLba + uint32(i)
		}
		// Use the cached root directory data when available. The KFS log
		// starts at sector 1 and grows upward; once it passes rootLba the
		// on-device root entries are overwritten, so we must serve from the
		// snapshot captured at Mount time.
		data := f.rootData
		if len(data) == 0 {
			data = make([]byte, rootSecs*512)
			for i, lba := range d.lbas {
				if err := f.dev.Read(uint64(lba), data[i*512:(i+1)*512]); err != nil {
					return nil, err
				}
			}
		}
		d.data = make([]byte, len(data))
		copy(d.data, data)
	} else {
		for _, c := range chain {
			lba := f.clusLBA(c)
			for s := uint32(0); s < secPerClus; s++ {
				d.lbas = append(d.lbas, uint32(lba)+s)
			}
		}
		d.data = make([]byte, len(d.lbas)*512)
		for i, lba := range d.lbas {
			if cached, ok := f.sectorCache[uint64(lba)]; ok {
				copy(d.data[i*512:(i+1)*512], cached)
			} else {
				if err := f.dev.Read(uint64(lba), d.data[i*512:(i+1)*512]); err != nil {
					return nil, err
				}
				cached := make([]byte, 512)
				copy(cached, d.data[i*512:(i+1)*512])
				f.sectorCache[uint64(lba)] = cached
			}
		}
	}
	if debugFat16 && !isRoot && len(chain) > 0 {
		fmt.Fprintf(os.Stderr, "[fat-debug] loadDir chain=%v lbas=%v data_len=%d first32=%x\n", chain, d.lbas, len(d.data), d.data[:32])
	}
	return d, nil
}

// saveDir writes the whole directory area back, keeping the sector caches
// in sync so subsequent loadDir calls observe the write. The rootData cache
// exists primarily so the FAT16 root survives a KGS format/seeding pass that
// overwrites on-disk sectors; in the FAT-only path (no format) it must still
// reflect live writes.
func (f *FAT) saveDir(d *dirData) error {
	if debugFat16 {
		fmt.Fprintf(os.Stderr, "[fat-debug] saveDir lbas=%v data_len=%d first32=%x\n", d.lbas, len(d.data), d.data[:32])
	}
	for i, lba := range d.lbas {
		offset := i * 512
		chunk := d.data[offset : offset+512]
		if err := f.dev.Write(uint64(lba), chunk); err != nil {
			return err
		}
		if d.isRoot {
			idx := (int(lba) - int(f.rootLba)) * 512
			if idx >= 0 && idx+512 <= len(f.rootData) {
				copy(f.rootData[idx:idx+512], chunk)
			}
		} else if f.sectorCache != nil {
			cached := make([]byte, 512)
			copy(cached, chunk)
			f.sectorCache[uint64(lba)] = cached
		}
	}
	return nil
}

// growDir extends a subdirectory by one zeroed cluster (root cannot).
func (f *FAT) growDir(d *dirData) error {
	if d.isRoot {
		return ErrNoSpace
	}
	c, err := f.extendChain(d.chain, 1)
	if err != nil {
		return err
	}
	d.chain = c
	tail := f.clusLBA(c[len(c)-1])
	for s := uint32(0); s < secPerClus; s++ {
		d.lbas = append(d.lbas, uint32(tail)+s)
	}
	d.data = append(d.data, make([]byte, clusBytes)...)
	return nil
}

// putEntry claims a slot and writes name with attr/clus/size.
func (d *dirData) putEntry(name [11]byte, attr byte, clus uint32, size uint32) {
	off, ok := d.freeSlot()
	if !ok {
		panic("putEntry without capacity") // callers grow first
	}
	raw := d.data[off : off+32]
	for i := range raw {
		raw[i] = 0
	}
	copy(raw[0:11], name[:])
	raw[11] = attr
	lib.Put16(raw[26:], uint16(clus))
	lib.Put32(raw[28:], size)
}

func entryRaw(d *dirData, off int) []byte { return d.data[off : off+32] }

// statOf renders one raw entry.
func statOf(raw []byte, data []byte, off int) StatInfo {
	name := from83(raw)
	for scan := off - 32; scan >= 0 && scan+32 <= len(data) && isLFN(data, scan); scan -= 32 {
		if data[scan] == 0 {
			break
		}
		name = decodeLFNEntry(data, scan) + name
	}
	return StatInfo{
		Name:    name,
		Attr:    raw[11],
		Size:    lib.Get32(raw[28:]),
		Cluster: uint32(lib.Get16(raw[26:])),
	}
}

func isLFN(data []byte, off int) bool {
	return (data[off+11] & 0x0F) == 0x0F
}

// ---- path resolution ----

// splitPath cleans an absolute path into components ("/a/b.txt").
func splitPath(path string) ([]string, error) {
	if path == "" || path[0] != '/' {
		return nil, ErrBadName
	}
	parts := strings.Split(path[1:], "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return nil, ErrBadName // v1: no traversal above root
		}
		out = append(out, p)
	}
	return out, nil
}

// resolveDir walks to the directory containing the final component,
// returning its dirData plus the target's validated short name.
func (f *FAT) resolveParent(path string) (*dirData, string, [11]byte, error) {
	parts, err := splitPath(path)
	if err != nil {
		return nil, "", [11]byte{}, err
	}
	name := parts[len(parts)-1]
	n11, err := validate83(name)
	if err != nil {
		n11 = [11]byte{}
	}
	dir, err := f.loadDir(true, nil)
	if err != nil {
		return nil, "", [11]byte{}, err
	}
	for _, part := range parts[:len(parts)-1] {
		p11, err := validate83(part)
		if err != nil {
			return nil, "", [11]byte{}, err
		}
		off, ok := dir.find(p11)
		if !ok {
			off, ok = dir.findByLfn(part)
		}
		if !ok {
			return nil, "", [11]byte{}, ErrNoEntry
		}
		raw := entryRaw(dir, off)
		if raw[11]&attrDir == 0 {
			return nil, "", [11]byte{}, ErrNotDir
		}
		sub, err := f.loadDir(false, f.chainOf(uint32(lib.Get16(raw[26:]))))
		if err != nil {
			return nil, "", [11]byte{}, err
		}
		dir = sub
	}
	return dir, name, n11, nil
}

// walk returns the entry offset of path within its parent dir, or
// found=false.
func (f *FAT) walk(path string) (*dirData, int, bool, error) {
	parent, name, n11, err := f.resolveParent(path)
	if err != nil {
		return nil, 0, false, err
	}
	off, ok := parent.find(n11)
	if !ok {
		off, ok = parent.findByLfn(name)
	}
	return parent, off, ok, nil
}

// ---- public filesystem ops ----

// Mkdir creates path (parents must exist).
func (f *FAT) Mkdir(path string) error {
	delete(f.fileCache, path)
	parent, _, n11, err := f.resolveParent(path)
	if err != nil {
		return err
	}
	if _, exists := parent.find(n11); exists {
		return ErrExists
	}
	c, err := f.allocOne() // returns a zeroed cluster
	if err != nil {
		return err
	}
	// initialise "." / ".."
	dot := make([]byte, 32)
	copy(dot[0:11], ".          ")
	dot[11] = attrDir
	lib.Put16(dot[26:], uint16(c))
	dotdot := make([]byte, 32)
	copy(dotdot[0:11], "..         ")
	dotdot[11] = attrDir
	// parent cluster id (root → 0 per convention)
	pclus := uint32(0)
	if !parent.isRoot && len(parent.chain) > 0 {
		pclus = parent.chain[0]
	}
	lib.Put16(dotdot[26:], uint16(pclus))
	zlba := f.clusLBA(c) // cluster already zeroed by allocOne
	if err := writePartial(f.dev, zlba, dot, dotdot); err != nil {
		return err
	}
	if _, ok := parent.freeSlot(); !ok {
		if err := f.growDir(parent); err != nil {
			f.freeChain(c)
			return err
		}
	}
	parent.putEntry(n11, attrDir, c, 0)
	if err := f.saveDir(parent); err != nil {
		return err
	}
	return f.flushFat()
}

// Create makes path an empty regular file (create-or-truncate).
func (f *FAT) Create(path string) error {
	delete(f.fileCache, path)
	parent, _, n11, err := f.resolveParent(path)
	if err != nil {
		if debugFat16 {
			fmt.Fprintf(os.Stderr, "[fat-debug] Create(%q) resolveParent err=%v\n", path, err)
		}
		return err
	}
	if off, exists := parent.find(n11); exists {
		raw := entryRaw(parent, off)
		if raw[11]&attrDir != 0 {
			return ErrIsDir
		}
		// truncate in place
		f.freeChain(uint32(lib.Get16(raw[26:])))
		lib.Put16(raw[26:], 0)
		lib.Put32(raw[28:], 0)
		if err := f.saveDir(parent); err != nil {
			return err
		}
		return f.flushFat()
	}
	if _, ok := parent.freeSlot(); !ok {
		if err := f.growDir(parent); err != nil {
			return err
		}
	}
	parent.putEntry(n11, attrArchive, 0, 0)
	if debugFat16 {
		fmt.Fprintf(os.Stderr, "[fat-debug] Create(%q) putEntry at parent lbas=%v data_len=%d\n", path, parent.lbas, len(parent.data))
	}
	if err := f.saveDir(parent); err != nil {
		if debugFat16 {
			fmt.Fprintf(os.Stderr, "[fat-debug] Create(%q) saveDir err=%v\n", path, err)
		}
		return err
	}
	if err := f.flushFat(); err != nil {
		if debugFat16 {
			fmt.Fprintf(os.Stderr, "[fat-debug] Create(%q) flushFat err=%v\n", path, err)
		}
		return err
	}
	if debugFat16 {
		fmt.Fprintf(os.Stderr, "[fat-debug] Create(%q) success\n", path)
	}
	return nil
}

// WriteFile writes data at off into an existing regular file, growing
// it as needed. Size becomes max(oldSize, off+len(data)).
func (f *FAT) WriteFile(path string, off uint64, data []byte) error {
	delete(f.fileCache, path)
	parent, offIn, ok, err := f.walk(path)
	if debugFat16 {
		fmt.Fprintf(os.Stderr, "[fat-debug] WriteFile(%q) walk parent_lbas=%v ok=%v err=%v\n", path, parent.lbas, ok, err)
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoEntry
	}
	raw := entryRaw(parent, offIn)
	if raw[11]&attrDir != 0 {
		return ErrIsDir
	}
	start := uint32(lib.Get16(raw[26:]))
	size := lib.Get32(raw[28:])
	need := off + uint64(len(data))
	finalSize := size // POSIX pwrite: size grows to max(size, off+len)
	if need > uint64(finalSize) {
		finalSize = uint32(need)
	}
	reqClusters := ceilDiv(uint64(finalSize), clusBytes)

	chain := f.chainOf(start)
	zeroFile := start < 2
	switch {
	case reqClusters == 0:
		f.freeChain(start)
	case zeroFile:
		chain = make([]uint32, 0, reqClusters)
		for i := uint32(0); i < reqClusters; i++ {
			c, err := f.allocOne()
			if err != nil {
				return err
			}
			if len(chain) > 0 {
				f.fatSet(chain[len(chain)-1], uint16(c))
			}
			chain = append(chain, c)
		}
		start = chain[0]
	case uint32(len(chain)) < reqClusters:
		var err error
		chain, err = f.extendChain(chain, reqClusters-uint32(len(chain)))
		if err != nil {
			return err
		}
	case uint32(len(chain)) > reqClusters:
		for _, c := range chain[reqClusters:] {
			f.fatSet(c, freeClus)
		}
		f.fatSet(chain[reqClusters-1], eocFAT16)
		chain = chain[:reqClusters]
	}

	// scatter data across the chain
	pos := uint64(0)
	for pos < uint64(len(data)) {
		idx := (off + pos) / clusBytes
		inOff := uint32((off + pos) % clusBytes)
		chunk := uint64(clusBytes) - uint64(inOff)
		if chunk > uint64(len(data))-pos {
			chunk = uint64(len(data)) - pos
		}
		if inOff == 0 && chunk == clusBytes {
			if err := f.dev.Write(f.clusLBA(chain[idx]), data[pos:pos+chunk]); err != nil {
				return err
			}
		} else if err := readModifyWrite(f.dev, f.clusLBA(chain[idx]), inOff, chunk, data[pos:pos+chunk]); err != nil {
			return err
		}
		pos += chunk
	}

	lib.Put16(raw[26:], uint16(start))
	lib.Put32(raw[28:], finalSize)
	raw[11] |= attrArchive
	if err := f.saveDir(parent); err != nil {
		return err
	}
	return f.flushFat()
}

// ReadFile reads up to len(buf) bytes at off; returns bytes copied.
func (f *FAT) ReadFile(path string, off uint64, buf []byte) (int, error) {
	var chain []uint32
	var size uint32

	entry, cached := f.fileCache[path]
	if cached {
		chain = entry.chain
		size = entry.size
	} else {
		parent, offIn, ok, err := f.walk(path)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, ErrNoEntry
		}
		raw := entryRaw(parent, offIn)
		if raw[11]&attrDir != 0 {
			return 0, ErrIsDir
		}
		size = lib.Get32(raw[28:])
		chain = f.chainOf(uint32(lib.Get16(raw[26:])))
		f.fileCache[path] = fileEntry{chain: chain, size: size}
	}

	// Fast path: file data is fully cached in memory.
	if cached && entry.data != nil && int(entry.size) == len(entry.data) {
		if off >= uint64(size) {
			return 0, nil
		}
		want := uint64(size) - off
		if want > uint64(len(buf)) {
			want = uint64(len(buf))
		}
		copy(buf, entry.data[off:off+want])
		return int(want), nil
	}

	if off >= uint64(size) {
		return 0, nil
	}
	want := uint64(size) - off
	if want > uint64(len(buf)) {
		want = uint64(len(buf))
	}
	read := uint64(0)
	remaining := want
	for remaining > 0 {
		idx := (off + read) / clusBytes
		if idx >= uint64(len(chain)) {
			break // sparse/corrupt tail: stop cleanly
		}
		inOff := uint32((off + read) % clusBytes)
		take := uint32(clusBytes) - inOff
		if take > uint32(remaining) {
			take = uint32(remaining)
		}
		srcLBA := f.clusLBA(chain[idx]) + uint64(inOff)/512
		sOff := inOff % 512
		var sec [512]byte
		for left := take; left > 0; {
			if cachedData, ok := f.sectorCache[srcLBA]; ok {
				copy(sec[:], cachedData)
			} else {
				if err := f.dev.Read(srcLBA, sec[:]); err != nil {
					return int(read), err
				}
			}
			n := 512 - sOff
			if n > left {
				n = left
			}
			copy(buf[read:read+uint64(n)], sec[sOff:sOff+n])
			read += uint64(n)
			remaining -= uint64(n)
			left -= n
			srcLBA++
			sOff = 0
		}
	}
	return int(read), nil
}

// Delete unlinks path (regular file or empty directory).
func (f *FAT) Delete(path string) error {
	delete(f.fileCache, path)
	parent, offIn, ok, err := f.walk(path)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoEntry
	}
	raw := entryRaw(parent, offIn)
	if raw[11]&attrDir != 0 {
		sub, err := f.loadDir(false, f.chainOf(uint32(lib.Get16(raw[26:]))))
		if err != nil {
			return err
		}
		n := 0
		for _, si := range sub.list() { // list() already skips . ..
			_ = si
			n++
		}
		if n > 0 {
			return ErrNotEmpt
		}
		f.freeChain(uint32(lib.Get16(raw[26:])))
	} else {
		f.freeChain(uint32(lib.Get16(raw[26:])))
	}
	raw[0] = 0xE5
	if err := f.saveDir(parent); err != nil {
		return err
	}
	return f.flushFat()
}

// Stat returns metadata for path.
func (f *FAT) Stat(path string) (StatInfo, error) {
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}
	if path == "/" {
		return StatInfo{Name: "", Attr: attrDir}, nil
	}
	parent, offIn, ok, err := f.walk(path)
	if err != nil {
		return StatInfo{}, err
	}
	if !ok {
		return StatInfo{}, ErrNoEntry
	}
	return statOf(entryRaw(parent, offIn), parent.data, offIn), nil
}

// List enumerates a directory's visible entries.
func (f *FAT) List(path string) ([]StatInfo, error) {
	if path == "/" {
		d, err := f.loadDir(true, nil)
		if err != nil {
			return nil, err
		}
		return d.list(), nil
	}
	parent, offIn, ok, err := f.walk(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoEntry
	}
	raw := entryRaw(parent, offIn)
	if raw[11]&attrDir == 0 {
		return nil, ErrNotDir
	}
	sub, err := f.loadDir(false, f.chainOf(uint32(lib.Get16(raw[26:]))))
	if err != nil {
		return nil, err
	}
	return sub.list(), nil
}

// ---- small device helpers ----

// writePartial writes two consecutive 32-byte entries over a zeroed
// cluster's first sector (mkdir init).
func writePartial(dev BlockDev, lba uint64, a, b []byte) error {
	var sec [512]byte
	copy(sec[0:32], a)
	copy(sec[32:64], b)
	return dev.Write(lba, sec[:])
}

// readModifyWrite patches arbitrary byte ranges inside one cluster.
func readModifyWrite(dev BlockDev, clusLBA uint64, inOff uint32, chunk uint64, src []byte) error {
	var sec [512]byte
	lba := clusLBA + uint64(inOff)/512
	boff := inOff % 512
	done := uint32(0)
	for done < uint32(chunk) {
		take := 512 - boff
		if take > uint32(chunk)-done {
			take = uint32(chunk) - done
		}
		if err := dev.Read(lba, sec[:]); err != nil {
			return err
		}
		copy(sec[boff:boff+take], src[done:done+take])
		if err := dev.Write(lba, sec[:]); err != nil {
			return err
		}
		done += take
		lba++
		boff = 0
	}
	return nil
}

func ceilDiv(v uint64, by uint64) uint32 {
	return uint32((v + by - 1) / by)
}
