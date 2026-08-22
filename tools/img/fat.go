package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// FAT16 volume builder reproducing the mtools layout used by the Makefile:
// superfloppy (no MBR), 512-byte sectors, 1 reserved sector, 2 FATs,
// 512 root entries, media 0xF8, CHS geometry 16 heads / 63 sectors-per-track.
// For a 64 MiB image this yields exactly mformat's geometry (2-sector
// clusters, 255-sector FATs, 65264 clusters) — verified against minfo.

const (
	sectorSize   = 512
	reservedSecs = 1
	numFATs      = 2
	rootEntries  = 512
	rootSecs     = rootEntries * 32 / sectorSize // 16

	mediaDescriptor = 0xF8
	driveNumber     = 0x80
	extBootSig      = 0x29

	fatEOC    = 0xFFFF
	fatFree   = 0x0000
	firstClus = 2

	attrDir     = 0x10
	attrArchive = 0x20
)

type fsnode struct {
	name     string
	attr     byte
	mtime    time.Time
	data     []byte
	children []*fsnode
	start    uint32 // first cluster, assigned during Flush
}

// FileSource is one file to place into the volume. Name uses '/'
// separators relative to the volume root; parents are auto-created.
type FileSource struct {
	Name  string
	Data  []byte
	Mtime time.Time
}

// Volume builds one FAT16 filesystem image through a BlockDevice.
type Volume struct {
	dev       BlockDevice
	totalSecs int
	spc       int // sectors per cluster
	fatSecs   int
	clusters  int

	dataStart int64 // byte offset of cluster 2
	rootOff   int64 // byte offset of fixed root directory region

	fat      []byte // RAM copy of one FAT, replicated at flush
	rootBuf  bytes.Buffer
	nextFree uint32 // allocation hint
	volid    uint32
	label    string

	root    *fsnode
	fileCnt int
	byteCnt int64
	hasher  hash.Hash32
	flushed bool
}

// NewVolume picks geometry immediately so callers fail fast on bad sizes.
func NewVolume(dev BlockDevice, size int64, label string) (*Volume, error) {
	if size <= 0 || size%sectorSize != 0 {
		return nil, fmt.Errorf("img: size %d must be a positive multiple of %d", size, sectorSize)
	}
	total := int(size / sectorSize)
	spc, fatSecs, clusters, err := pickGeometry(total)
	if err != nil {
		return nil, fmt.Errorf("img: %d sectors: %w", total, err)
	}
	if label == "" {
		label = "NO NAME"
	}
	if len(label) > 11 {
		return nil, fmt.Errorf("img: label %q longer than 11 bytes", label)
	}
	return &Volume{
		dev:       dev,
		totalSecs: total,
		spc:       spc,
		fatSecs:   fatSecs,
		clusters:  clusters,
		dataStart: int64(reservedSecs+numFATs*fatSecs+rootSecs) * sectorSize,
		rootOff:   int64(reservedSecs+numFATs*fatSecs) * sectorSize,
		fat:       make([]byte, fatSecs*sectorSize),
		nextFree:  firstClus,
		label:     strings.ToUpper(label),
		root:      &fsnode{attr: attrDir},
		hasher:    fnv.New32a(),
	}, nil
}

// pickGeometry scans cluster sizes until the data area yields a cluster
// count inside the FAT16 window [4085, 65524]. The FAT size is solved to a
// fixed point first because it shrinks the data area. For 131072 sectors
// (64 MiB) this returns spc=2, fatSecs=255 — identical to mformat.
func pickGeometry(totalSecs int) (spc, fatSecs, clusters int, err error) {
	for _, s := range []int{1, 2, 4, 8, 16, 32, 64, 128} {
		f := 1
		var c int
		for i := 0; i < 12; i++ { // fixed-point iteration
			data := totalSecs - reservedSecs - numFATs*f - rootSecs
			if data <= 0 {
				break
			}
			c = data / s
			want := ((c+2)*2 + sectorSize - 1) / sectorSize
			if want == f {
				break
			}
			f = want
		}
		data := totalSecs - reservedSecs - numFATs*f - rootSecs
		c = data / s
		if c >= 4085 && c <= 65524 &&
			reservedSecs+numFATs*f+rootSecs+c*s <= totalSecs {
			return s, f, c, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("no FAT16 geometry fits")
}

func (v *Volume) clusterBytes() int64 { return int64(v.spc) * sectorSize }

func (v *Volume) clusterOff(cl uint32) int64 {
	return v.dataStart + int64(cl-firstClus)*v.clusterBytes()
}

func (v *Volume) fatGet(n uint32) uint16 {
	return binary.LittleEndian.Uint16(v.fat[n*2:])
}

func (v *Volume) fatSet(n uint32, val uint16) {
	binary.LittleEndian.PutUint16(v.fat[n*2:], val)
}

// allocCluster claims one free cluster (caller links/marks it).
func (v *Volume) allocCluster() (uint32, error) {
	n := v.nextFree
	for i := uint32(0); i < uint32(v.clusters); i++ {
		if n > uint32(v.clusters) {
			n = firstClus
		}
		if v.fatGet(n) == fatFree {
			v.nextFree = n + 1
			return n, nil
		}
		n++
	}
	return 0, fmt.Errorf("image full: all %d clusters in use", v.clusters)
}

// allocChain links count clusters starting at a fresh head and returns it;
// payloads are written by chainWrite afterwards.
func (v *Volume) allocChain(count int) (uint32, error) {
	start, err := v.allocCluster()
	if err != nil {
		return 0, err
	}
	prev := start
	for k := 1; k < count; k++ {
		cur, err := v.allocCluster()
		if err != nil {
			return 0, err
		}
		v.fatSet(prev, uint16(cur))
		prev = cur
	}
	v.fatSet(prev, fatEOC)
	return start, nil
}

// chainWrite scatters buf across the chain headed at start.
func (v *Volume) chainWrite(start uint32, buf []byte) error {
	cb := v.clusterBytes()
	for off, cl := 0, start; ; cl++ {
		chunk := buf[off:]
		if int64(len(chunk)) > cb {
			chunk = chunk[:cb]
		}
		if _, err := v.dev.WriteAt(chunk, v.clusterOff(cl)); err != nil {
			return err
		}
		off += len(chunk)
		if off >= len(buf) {
			return nil
		}
	}
}

// MkDir creates every path component explicitly (empty dirs survive).
func (v *Volume) MkDir(p string) error {
	p = strings.Trim(p, "/")
	if _, err := v.nodeFor(p, true); err != nil {
		return err
	}
	v.hasher.Write([]byte(p))
	return nil
}

// Add places one file, creating parent directories as needed.
func (v *Volume) Add(src FileSource) error {
	name := strings.Trim(src.Name, "/")
	if name == "" {
		return fmt.Errorf("img: empty file name")
	}
	dir, leaf := path.Split(name)
	parent, err := v.nodeFor(dir, true)
	if err != nil {
		return err
	}
	for _, c := range parent.children {
		if c.name == leaf {
			return fmt.Errorf("img: duplicate entry %q", src.Name)
		}
	}
	if uint64(len(src.Data)) > 0xFFFFFFFF {
		return fmt.Errorf("img: %s exceeds FAT16 max file size", src.Name)
	}
	if src.Mtime.IsZero() {
		src.Mtime = time.Unix(0, 0).UTC()
	}
	parent.children = append(parent.children, &fsnode{
		name:  leaf,
		attr:  attrArchive,
		mtime: src.Mtime,
		data:  src.Data,
	})
	v.hasher.Write([]byte(name))
	binary.Write(v.hasher, binary.LittleEndian, uint64(len(src.Data)))
	v.fileCnt++
	v.byteCnt += int64(len(src.Data))
	return nil
}

// AddTree copies every regular file under dirsrc into dstDir ("/etc"...).
func (v *Volume) AddTree(dirsrc, dstDir string) error {
	return fs.WalkDir(os.DirFS(dirsrc), ".", func(rp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || rp == "." {
			return nil
		}
		if !d.Type().IsRegular() {
			fmt.Fprintf(os.Stderr, "img: skipping non-regular %s/%s\n", dirsrc, rp)
			return nil
		}
		full := path.Join(dirsrc, rp)
		st, err := os.Stat(full)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		dst := path.Join(dstDir, rp)
		if err := v.MkDir(path.Dir(dst)); err != nil {
			return err
		}
		return v.Add(FileSource{Name: dst, Data: data, Mtime: st.ModTime()})
	})
}

func (v *Volume) nodeFor(p string, mk bool) (*fsnode, error) {
	cur := v.root
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		var next *fsnode
		for _, c := range cur.children {
			if c.name == part {
				next = c
				break
			}
		}
		if next == nil {
			if !mk {
				return cur, fmt.Errorf("img: no such directory %q", p)
			}
			next = &fsnode{name: part, attr: attrDir, mtime: time.Unix(0, 0).UTC()}
			cur.children = append(cur.children, next)
		}
		cur = next
	}
	return cur, nil
}

// Flush serializes the whole volume onto the block device exactly once.
func (v *Volume) Flush() error {
	if v.flushed {
		return fmt.Errorf("img: volume already flushed")
	}
	v.volid = v.hasher.Sum32()

	if err := v.emitDir(v.root, nil, true); err != nil {
		return err
	}

	bs := v.bootSector()
	if _, err := v.dev.WriteAt(bs, 0); err != nil {
		return err
	}

	// FAT[0] carries the media descriptor (0xFFF8 for fixed media);
	// FAT[1] is end-of-chain.
	v.binaryFAT(0, 0xFF00|mediaDescriptor)
	v.binaryFAT(1, fatEOC)
	for i := 0; i < numFATs; i++ {
		off := int64(reservedSecs+i*v.fatSecs) * sectorSize
		if _, err := v.dev.WriteAt(v.fat, off); err != nil {
			return err
		}
	}
	if _, err := v.dev.WriteAt(v.rootBuf.Bytes(), v.rootOff); err != nil {
		return err
	}
	v.flushed = true
	return nil
}

func (v *Volume) binaryFAT(n uint32, val uint16) {
	binary.LittleEndian.PutUint16(v.fat[n*2:], val)
}

func (v *Volume) bootSector() []byte {
	bs := make([]byte, sectorSize)
	bs[0], bs[1], bs[2] = 0xEB, 0x3C, 0x90
	copy(bs[3:], "MSDOS5.0")
	binary.LittleEndian.PutUint16(bs[11:], sectorSize)
	bs[13] = byte(v.spc)
	binary.LittleEndian.PutUint16(bs[14:], reservedSecs)
	bs[16] = numFATs
	binary.LittleEndian.PutUint16(bs[17:], rootEntries)
	binary.LittleEndian.PutUint16(bs[19:], 0) // totsec16: use totsec32
	bs[21] = mediaDescriptor
	binary.LittleEndian.PutUint16(bs[22:], uint16(v.fatSecs))
	binary.LittleEndian.PutUint16(bs[24:], 63) // sectors per track
	binary.LittleEndian.PutUint16(bs[26:], 16) // heads (mtools parity)
	binary.LittleEndian.PutUint32(bs[28:], 0)  // hidden sectors
	binary.LittleEndian.PutUint32(bs[32:], uint32(v.totalSecs))
	bs[36] = driveNumber
	bs[38] = extBootSig
	binary.LittleEndian.PutUint32(bs[39:], v.volid)
	copy(bs[43:], pad(v.label, 11))
	copy(bs[54:], "FAT16   ")
	bs[510], bs[511] = 0x55, 0xAA
	return bs
}

// emitDir walks top-down. A subdirectory's first cluster is allocated
// BEFORE its children are emitted, so their ".." entries can carry the
// parent start; contents are materialized once all child starts exist.
func (v *Volume) emitDir(n, parent *fsnode, isRoot bool) error {
	var buf bytes.Buffer
	if isRoot {
		v.rootBuf.Reset()
	} else {
		start, err := v.allocChain(1)
		if err != nil {
			return fmt.Errorf("dir %q: %w", n.name, err)
		}
		n.start = start
		parentStart := uint32(0)
		if parent != nil && parent.start != 0 && parent.attr&attrDir != 0 {
			parentStart = parent.start
		}
		writeDotEntry(&buf, n, ".          ", n.start)
		writeDotEntry(&buf, n, "..         ", parentStart)
	}
	for _, c := range n.children {
		if c.attr&attrDir != 0 {
			if err := v.emitDir(c, n, false); err != nil {
				return err
			}
		}
	}
	for _, c := range n.children {
		if c.attr&attrDir != 0 {
			continue
		}
		if len(c.data) == 0 {
			c.start = 0 // FAT convention: empty files claim no cluster
			continue
		}
		count := (len(c.data) + int(v.clusterBytes()) - 1) / int(v.clusterBytes())
		start, err := v.allocChain(count)
		if err != nil {
			return fmt.Errorf("file %q: %w", c.name, err)
		}
		c.start = start
		if err := v.chainWrite(c.start, c.data); err != nil {
			return err
		}
	}
	// Short names of all siblings feed the collision resolver.
	taken := map[dirEntryKey]bool{}
	for _, c := range n.children {
		short, _, err := shortName(c.name)
		if err != nil {
			return fmt.Errorf("%q: %w", c.name, err)
		}
		taken[dirEntryKey(short)] = true
	}
	for _, c := range n.children {
		if err := v.appendEntry(&buf, c, taken); err != nil {
			return err
		}
	}
	if isRoot {
		if buf.Len() > rootEntries*32 {
			return fmt.Errorf("img: root directory overflow (%d entries > %d)",
				buf.Len()/32, rootEntries)
		}
		buf.WriteTo(&v.rootBuf)
		return nil
	}
	cb := int(v.clusterBytes())
	want := (buf.Len() + cb - 1) / cb
	if want > 1 {
		if err := v.growChain(n.start, want); err != nil {
			return fmt.Errorf("dir %q: %w", n.name, err)
		}
	}
	return v.chainWrite(n.start, buf.Bytes())
}

func writeDotEntry(buf *bytes.Buffer, n *fsnode, dot string, start uint32) {
	e := make([]byte, 32)
	copy(e[0:], pad(strings.TrimRight(dot, " "), 11))
	e[11] = attrDir
	date, tim := dosDateTime(n.mtime)
	binary.LittleEndian.PutUint16(e[22:], tim)
	binary.LittleEndian.PutUint16(e[24:], date)
	binary.LittleEndian.PutUint16(e[26:], uint16(start))
	buf.Write(e)
}

// appendEntry emits LFN entries (reverse order) then the 8.3 short entry.
func (v *Volume) appendEntry(buf *bytes.Buffer, c *fsnode, taken map[dirEntryKey]bool) error {
	short, lossy, err := shortName(c.name)
	if err != nil {
		return fmt.Errorf("%q: %w", c.name, err)
	}
	if lossy {
		short = resolveCollision(short, taken)
	}
	date, tim := dosDateTime(c.mtime)
	e := make([]byte, 32)
	copy(e[0:], short[:])
	e[11] = c.attr
	binary.LittleEndian.PutUint16(e[22:], tim)
	binary.LittleEndian.PutUint16(e[24:], date)
	binary.LittleEndian.PutUint16(e[26:], uint16(c.start&0xFFFF))
	binary.LittleEndian.PutUint32(e[28:], uint32(len(c.data)))
	if lossy {
		sum := lfnChecksum(short[:])
		for _, le := range reverse(lfnEntries(c.name, sum)) {
			buf.Write(le)
		}
	}
	buf.Write(e)
	return nil
}

// growChain extends the chain headed at start until it has want clusters.
func (v *Volume) growChain(start uint32, want int) error {
	count := 1
	prev := start
	for v.fatGet(prev) < 0xFFF8 {
		prev = uint32(v.fatGet(prev))
		count++
	}
	for count < want {
		cur, err := v.allocCluster()
		if err != nil {
			return err
		}
		v.fatSet(prev, uint16(cur))
		v.fatSet(cur, fatEOC)
		prev = cur
		count++
	}
	return nil
}

func reverse(in [][]byte) [][]byte {
	out := make([][]byte, len(in))
	for i, e := range in {
		out[len(in)-1-i] = e
	}
	return out
}

func dosDateTime(t time.Time) (date, tim uint16) {
	y, mo, d := t.Date()
	if y < 1980 {
		y, mo, d = 1980, 1, 1
	}
	if y > 2107 {
		y, mo, d = 2107, 12, 31
	}
	hh, mm, ss := t.Clock()
	date = uint16(y-1980)<<9 | uint16(mo)<<5 | uint16(d)
	tim = uint16(hh)<<11 | uint16(mm)<<5 | uint16(ss/2)
	return date, tim
}
