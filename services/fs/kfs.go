package main

import (
	"bytes"
	"errors"
	"hash/crc32"

	lib "kernel.lane/guests/lib"
)

// kfs — log-structured filesystem replacing FAT16 as fs.wasm's on-disk
// format (AGENTS.md Phase 8 / FS FORMAT DECISION). Design:
//
//	Sector 0 superblock: "KFS1" magic, geometry, log start.
//	Log region: append-only byte stream of CRC32'd records —
//	  {u32 len, u32 crc32(payload), u8 type, payload[len]}
//	  types: 1=INODE {ino,uid,kind,size}
//	         2=DIRENT {parent,op(create|delete),name,ino}
//	         3=DATA {ino,off,dlen,bytes} (off==truncSentinel ⇒ truncate)
//	         4=CHECKPOINT {nextIno} every 64 records
//	Recovery: full sequential replay from log start; first record whose
//	length/CRC fails ends the scan — everything after is a torn tail and
//	is dropped. The write cursor resumes at the recovered end, so a
//	crashed volume continues cleanly.
//	Ownership: INODE.uid native (recorded per inode).
//	FAT16 remains available as an import/export filter; the §-port
//	protocol above this layer is unchanged.

var (
	ErrKFSBadSB = errors.New("kfs: bad superblock")
)

const (
	kfsRecInode      byte = 1
	kfsRecDirent     byte = 2
	kfsRecData       byte = 3
	kfsRecCheckpoint byte = 4

	kfsHdrLen    = 9 // u32 len + u32 crc + u8 type
	kfsCPEvery   = 64
	kfsTruncSent = ^uint64(0)

	kfsRootIno = 1

	kfsKindFile byte = 0
	kfsKindDir  byte = 1
)

type kfsInode struct {
	ino  uint32
	uid  uint32
	kind byte
	size uint32
}

type KFS struct {
	dev       BlockDev
	numBlocks uint64

	inodes   map[uint32]*kfsInode
	children map[uint32][]int // parent ino -> indexes into dirents
	dirents  []kfsDirent
	data     map[uint32][]byte // live file contents (RAM image of log)
	logData  []byte            // pre-loaded KGS log region (RAM cache)

	nextIno uint32
	wOff    int64 // append cursor within the log byte stream
	recs    int   // records since mount (checkpoint pacing)
	logEnd  int64
}

type kfsDirent struct {
	parent uint32
	name   string
	ino    uint32
	dead   bool
}

const kfsSectorSize = 512

const kfsMaxPayload = 65536 // 64 KiB max record payload (replay limit)

func kfsLogStart() int64 { return 512 }

// Format lays down the superblock, root inode, and initial checkpoint.
func FormatKFS(dev BlockDev) error {
	bs, n := dev.Geometry()
	if bs != bwBlkSize || n < 16 {
		return ErrKFSBadSB
	}
	sb := make([]byte, 512)
	copy(sb[0:4], "KFS1")
	lib.Put32(sb[4:], bs)
	lib.Put64(sb[8:], n)
	lib.Put32(sb[16:], uint32(kfsLogStart()/512))
	lib.Put32(sb[20:], 1) // format version
	if err := dev.Write(0, sb); err != nil {
		return err
	}
	k := &KFS{dev: dev, numBlocks: n,
		inodes: map[uint32]*kfsInode{}, children: map[uint32][]int{},
		data: map[uint32][]byte{}, nextIno: kfsRootIno + 1,
		wOff: kfsLogStart(), logEnd: int64(n) * 512}
	if err := k.append(kfsRecInode, kfsInodePayload(
		&kfsInode{ino: kfsRootIno, uid: 0, kind: kfsKindDir, size: 0})); err != nil {
		return err
	}
	return k.appendCheckpoint()
}

// Mount replays the log into memory. A torn or corrupt tail is dropped;
// everything committed before it is restored exactly.
func MountKFS(dev BlockDev) (*KFS, error) {
	bs, n := dev.Geometry()
	sb := make([]byte, 512)
	if err := dev.Read(0, sb); err != nil {
		return nil, err
	}
	if !bytes.Equal(sb[0:4], []byte("KFS1")) || lib.Get32(sb[4:]) != bs {
		return nil, ErrKFSBadSB
	}
	if lib.Get32(sb[16:]) != uint32(kfsLogStart()/512) {
		return nil, ErrKFSBadSB
	}
	k := &KFS{dev: dev, numBlocks: n,
		inodes: map[uint32]*kfsInode{}, children: map[uint32][]int{},
		data: map[uint32][]byte{}, logData: sb,
		wOff: kfsLogStart(), logEnd: int64(n) * 512}
	if err := k.replay(); err != nil {
		return nil, err
	}
	return k, nil
}

// finalizeLogData loads the entire committed log region (log start to wOff)
// into RAM. After this, readAt and replayData read from logData instead of
// polling the block device via virtio_blk_rw — critical for throughput on
// the owned engine. Call after seeding/import finishes, when the FAT16 or
// pre-load cache is no longer needed, to avoid peak-RSS doubling.
func (k *KFS) finalizeLogData() error {
	logLen := k.wOff - kfsLogStart()
	if logLen <= 0 {
		return nil
	}
	k.logData = make([]byte, kfsLogStart()+logLen)
	// Read the superblock (sector 0) plus the log region from the device.
	for lba := uint64(0); lba < uint64(k.wOff)/kfsSectorSize+1; lba++ {
		if lba >= k.numBlocks {
			break
		}
		off := int64(lba) * kfsSectorSize
		if off+kfsSectorSize > int64(len(k.logData)) {
			break
		}
		buf := make([]byte, kfsSectorSize)
		if err := k.dev.Read(lba, buf); err != nil {
			return err
		}
		copy(k.logData[off:], buf)
	}
	return nil
}

// ---- log mechanics ----

// append splices one framed record at the cursor via read-modify-write,
// so partial trailing sectors keep whatever precedes the new bytes.
func (k *KFS) append(recType byte, payload []byte) error {
	n := kfsHdrLen + len(payload)
	if k.wOff+int64(n) > k.logEnd {
		return ErrNoSpace
	}
	raw := make([]byte, n)
	lib.Put32(raw[0:], uint32(len(payload)))
	lib.Put32(raw[4:], crc32.ChecksumIEEE(payload))
	raw[8] = recType
	copy(raw[kfsHdrLen:], payload)

	buf := raw
	secSize := int64(kfsSectorSize)
	start := k.wOff
	// Fast path: sector-aligned, sector-aligned-size write (no RMW).
	if start%secSize == 0 && int64(len(buf))%secSize == 0 {
		if err := k.dev.Write(uint64(start/secSize), buf); err != nil {
			return err
		}
		k.wOff += int64(n)
	} else {
		// Slow path: read-modify-write for partial-sector records.
		// si is the ABSOLUTE device byte (the log begins at device byte
		// kfsLogStart(), so stream offsets are already device offsets).
		sec := make([]byte, secSize)
		for done := int64(0); done < int64(n); {
			si := start + done
			off := si % secSize
			take := secSize - off
			if take > int64(n)-done {
				take = int64(n) - done
			}
			if err := k.dev.Read(uint64(si/secSize), sec[:]); err != nil {
				return err
			}
			copy(sec[off:off+take], buf[done:done+take])
			if err := k.dev.Write(uint64(si/secSize), sec[:]); err != nil {
				return err
			}
			done += take
		}
		k.wOff += int64(n)
	}
	if recType != kfsRecCheckpoint { // checkpoints never re-trigger pacing
		k.recs++
		if k.recs%kfsCPEvery == 0 {
			return k.appendCheckpoint()
		}
	}
	return nil
}

// padToSectorBoundary advances wOff to the next sector boundary so that
// subsequent append calls can use the fast-path (sector-aligned writes).
func (k *KFS) padToSectorBoundary() {
	secSize := int64(kfsSectorSize)
	if rem := k.wOff % secSize; rem != 0 {
		k.wOff += secSize - rem
	}
}

func (k *KFS) appendCheckpoint() error {
	cp := make([]byte, 4)
	lib.Put32(cp, k.nextIno)
	return k.append(kfsRecCheckpoint, cp)
}

// replay scans the whole log, applying valid records in order. The scan
// stops at the first framing violation (short header, length beyond the
// log, CRC mismatch, unknown type) — the classic torn tail.
func (k *KFS) replay() error {
	pos := k.wOff
	hdr := make([]byte, kfsHdrLen)
	for pos+kfsHdrLen <= k.logEnd {
		if err := k.readAt(pos, hdr); err != nil {
			break
		}
		plen := int(lib.Get32(hdr[0:]))
		crc := lib.Get32(hdr[4:])
		typ := hdr[8]
		if plen < 0 || plen > kfsMaxPayload || pos+kfsHdrLen+int64(plen) > k.logEnd {
			break
		}
		payload := make([]byte, plen)
		if err := k.readAt(pos+kfsHdrLen, payload); err != nil {
			break
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		if !k.apply(typ, payload) {
			break // unknown type ⇒ garbage tail
		}
		pos += int64(kfsHdrLen + plen)
	}
	k.wOff = pos
	return nil
}

func (k *KFS) readAt(off int64, dst []byte) error {
	if off < 0 || uint64(off/512) >= k.numBlocks {
		return errors.New("kfs: beyond device")
	}
	// Serve from the pre-loaded log buffer when in range; fall back to
	// device reads for data appended after the initial mount.
	if off+int64(len(dst)) <= int64(len(k.logData)) {
		copy(dst, k.logData[off:off+int64(len(dst))])
		return nil
	}
	sec := make([]byte, 512)
	done := 0
	for done < len(dst) {
		si := off + int64(done)
		if err := k.dev.Read(uint64(si/512), sec[:]); err != nil {
			return err
		}
		take := 512 - int(si%512)
		if take > len(dst)-done {
			take = len(dst) - done
		}
		copy(dst[done:], sec[si%512:int(si%512)+take])
		done += take
	}
	return nil
}

// apply applies one replayed record; false ⇒ unknown type (stop scan).
func (k *KFS) apply(typ byte, p []byte) bool {
	switch typ {
	case kfsRecInode:
		if len(p) < 13 {
			return false
		}
		ino := lib.Get32(p[0:])
		k.inodes[ino] = &kfsInode{ino: ino, uid: lib.Get32(p[4:]),
			kind: p[8], size: lib.Get32(p[9:])}
		if ino >= k.nextIno {
			k.nextIno = ino + 1
		}
		if p[8] == kfsKindDir {
			if _, ok := k.children[ino]; !ok {
				k.children[ino] = nil
			}
		}
	case kfsRecDirent:
		if len(p) < 7 {
			return false
		}
		parent := lib.Get32(p[0:])
		op := p[4]
		name, next, ok := lib.LStr(p, 5)
		if !ok || next+4 > len(p) {
			return false
		}
		ino := lib.Get32(p[next:])
		if op == 1 {
			k.removeDirent(parent, name)
			return true
		}
		k.dirents = append(k.dirents, kfsDirent{parent: parent, name: name, ino: ino})
		k.children[parent] = append(k.children[parent], len(k.dirents)-1)
	case kfsRecData:
		if len(p) < 16 {
			return false
		}
		ino := lib.Get32(p[0:])
		off := lib.Get64(p[4:])
		dlen := int(lib.Get32(p[12:]))
		if len(p) < 16+dlen {
			return false
		}
		buf := k.data[ino]
		if off == kfsTruncSent {
			k.data[ino] = buf[:0]
			return true
		}
		end := int(off) + dlen
		for len(buf) < end {
			buf = append(buf, 0)
		}
		copy(buf[off:], p[16:])
		k.data[ino] = buf
	case kfsRecCheckpoint:
		if len(p) >= 4 {
			if ni := lib.Get32(p); ni > k.nextIno {
				k.nextIno = ni
			}
		}
	default:
		return false
	}
	return true
}

func (k *KFS) removeDirent(parent uint32, name string) {
	idx := k.findDirent(parent, name)
	if idx >= 0 {
		k.dirents[idx].dead = true
	}
}

func (k *KFS) findDirent(parent uint32, name string) int {
	for _, i := range k.children[parent] {
		d := k.dirents[i]
		if !d.dead && d.name == name {
			return i
		}
	}
	return -1
}

// ---- path resolution over the dirent maps ----

// walk resolves an existing full path to its inode number.
func (k *KFS) walk(path string) (ino uint32, ok bool, err error) {
	parts, err := splitPath(path)
	if err != nil {
		return 0, false, err
	}
	cur := uint32(kfsRootIno)
	for _, part := range parts {
		di := k.findDirent(cur, part)
		if di < 0 {
			return 0, false, nil
		}
		cur = k.dirents[di].ino
	}
	return cur, true, nil
}

// walkParent resolves everything but the last component, returning the
// parent dir ino and leaf name (mirrors fat16.resolveParent usage).
func (k *KFS) walkParent(path string) (pino uint32, leaf string, err error) {
	parts, err := splitPath(path)
	if err != nil || len(parts) == 0 {
		return 0, "", ErrBadName
	}
	cur := uint32(kfsRootIno)
	for _, part := range parts[:len(parts)-1] {
		di := k.findDirent(cur, part)
		if di < 0 {
			return 0, "", ErrNoEntry
		}
		cur = k.dirents[di].ino
		if k.inodes[cur] == nil || k.inodes[cur].kind != kfsKindDir {
			return 0, "", ErrNotDir
		}
	}
	return cur, parts[len(parts)-1], nil
}

// ---- public API (mirrors *FAT so the port protocol never changes) ----

// Create makes path an empty regular file (create-or-truncate).
func (k *KFS) Create(path string) error {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return err
	}
	if di := k.findDirent(pino, leaf); di >= 0 {
		ino := k.dirents[di].ino
		if nd := k.inodes[ino]; nd != nil && nd.kind == kfsKindDir {
			return ErrIsDir
		}
		return k.truncate(ino)
	}
	ino := k.allocIno()
	if err := k.append(kfsRecInode, kfsInodePayload(
		&kfsInode{ino: ino, uid: 0, kind: kfsKindFile, size: 0})); err != nil {
		return err
	}
	if err := k.appendDirent(pino, leaf, ino); err != nil {
		return err
	}
	k.inodes[ino] = &kfsInode{ino: ino, uid: 0, kind: kfsKindFile}
	k.data[ino] = nil
	return nil
}

// Mkdir creates path (parents must exist).
func (k *KFS) Mkdir(path string) error {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return err
	}
	if k.findDirent(pino, leaf) >= 0 {
		return ErrExists
	}
	ino := k.allocIno()
	if err := k.append(kfsRecInode, kfsInodePayload(
		&kfsInode{ino: ino, uid: 0, kind: kfsKindDir, size: 0})); err != nil {
		return err
	}
	if err := k.appendDirent(pino, leaf, ino); err != nil {
		return err
	}
	k.inodes[ino] = &kfsInode{ino: ino, uid: 0, kind: kfsKindDir}
	k.children[ino] = nil
	return nil
}

// Delete unlinks path (regular file or empty directory).
func (k *KFS) Delete(path string) error {
	parts, err := splitPath(path)
	if err != nil || len(parts) == 0 {
		return ErrBadName
	}
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return ErrNoEntry
	}
	ino := k.dirents[di].ino
	nd := k.inodes[ino]
	if nd != nil && nd.kind == kfsKindDir && len(k.liveChildren(ino)) > 0 {
		return ErrNotEmpt
	}
	if err := k.appendDirentDelete(pino, leaf); err != nil {
		return err
	}
	delete(k.data, ino)
	return nil
}

func (k *KFS) liveChildren(parent uint32) []int {
	var out []int
	for _, i := range k.children[parent] {
		if !k.dirents[i].dead {
			out = append(out, i)
		}
	}
	return out
}

// Stat returns metadata for path.
func (k *KFS) Stat(path string) (StatInfo, error) {
	if path == "/" {
		return StatInfo{Attr: attrDir}, nil
	}
	parts, err := splitPath(path)
	if err != nil {
		return StatInfo{}, err
	}
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return StatInfo{}, err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return StatInfo{}, ErrNoEntry
	}
	ino := k.dirents[di].ino
	nd := k.inodes[ino]
	st := StatInfo{Name: leaf, Cluster: ino}
	if nd != nil {
		st.Size = nd.size
		if nd.kind == kfsKindDir {
			st.Attr = attrDir
		}
	} else if len(parts) > 0 {
		return StatInfo{}, ErrNoEntry
	}
	return st, nil
}

// List enumerates a directory's visible entries.
func (k *KFS) List(path string) ([]StatInfo, error) {
	var pino uint32
	if path == "/" {
		pino = kfsRootIno
	} else {
		ino, ok, err := k.walk(path)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNoEntry
		}
		nd := k.inodes[ino]
		if nd == nil || nd.kind != kfsKindDir {
			return nil, ErrNotDir
		}
		pino = ino
	}
	var out []StatInfo
	for _, i := range k.children[pino] {
		d := k.dirents[i]
		if d.dead {
			continue
		}
		st := StatInfo{Name: d.name, Cluster: d.ino}
		if nd := k.inodes[d.ino]; nd != nil {
			st.Size = nd.size
			if nd.kind == kfsKindDir {
				st.Attr = attrDir
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// ReadFile copies up to len(buf) bytes from off; (0,nil) past EOF.
func (k *KFS) ReadFile(path string, off uint64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return 0, err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return 0, ErrNoEntry
	}
	ino := k.dirents[di].ino
	nd := k.inodes[ino]
	if nd != nil && nd.kind == kfsKindDir {
		return 0, ErrIsDir
	}
	size := uint64(0)
	if nd != nil {
		size = uint64(nd.size)
	}
	if off >= size {
		return 0, nil
	}
	want := size - off
	if want > uint64(len(buf)) {
		want = uint64(len(buf))
	}
	// After finalizeLogData, replayData reads from the in-memory logData buffer
	// (no device I/O). For files written during the current mount, k.data[ino]
	// holds a partial mirror — but that path is only taken before finalizeLogData.
	data := k.data[ino]
	if uint64(len(data)) >= size {
		copy(buf[:want], data[off:off+want])
		return int(want), nil
	}
	// Lazy replay from logData buffer — reconstruct the FULL file and cache
	// it so subsequent 4KB chunks are O(1) memory copies (module verify reads
	// a 2.7 MB module in 680 chunks; scanning the log each time exceeds the
	// kernel's per-message budget).
	replayed, err := k.replayData(ino, 0, int(size))
	if err != nil {
		return 0, err
	}
	k.data[ino] = replayed
	copy(buf[:want], replayed[off:])
	return int(want), nil
}

// cacheFileData stores the full file contents in the RAM mirror so
// subsequent ReadFile calls serve from memory without scanning the log.
// Caller (seedAndWrite) populates this from the FAT16 pre-read cache.
func (k *KFS) cacheFileData(path string, data []byte) {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return
	}
	k.data[k.dirents[di].ino] = data
}

// replayData lazily reconstructs a file's data by replaying DATA records
// from the KFS log. Only the requested byte range is returned. The full
// file data is NOT cached — WriteFile no longer populates k.data[ino].
func (k *KFS) replayData(ino uint32, off int64, want int) ([]byte, error) {
	out := make([]byte, want)
	pos := kfsLogStart()
	hdr := make([]byte, kfsHdrLen)
	for pos+kfsHdrLen <= k.wOff {
		if err := k.readAt(pos, hdr); err != nil {
			return nil, err
		}
		plen := int(lib.Get32(hdr[0:]))
		crc := lib.Get32(hdr[4:])
		typ := hdr[8]
		recEnd := pos + int64(kfsHdrLen+plen)
		if plen < 0 || plen > kfsMaxPayload || recEnd > k.wOff {
			break
		}
		payload := make([]byte, plen)
		if err := k.readAt(pos+kfsHdrLen, payload); err != nil {
			return nil, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break
		}
		pos = recEnd
		if typ != kfsRecData {
			continue
		}
		if len(payload) < 16 {
			continue
		}
		dino := lib.Get32(payload[0:])
		if dino != ino {
			continue
		}
		doff := lib.Get64(payload[4:])
		if doff == kfsTruncSent {
			// Truncate record: clear the output buffer for this file's range.
			// Subsequent DATA records will re-populate it.
			for i := range out {
				out[i] = 0
			}
			continue
		}
		dlen := int(lib.Get32(payload[12:]))
		if len(payload) < 16+dlen {
			continue
		}
		segStart := int64(doff)
		segEnd := segStart + int64(dlen)
		reqStart := int64(off)
		reqEnd := reqStart + int64(want)
		if segStart >= reqEnd || segEnd <= reqStart {
			continue
		}
		ovStart := max(segStart, reqStart)
		ovEnd := min(segEnd, reqEnd)
		segOff := ovStart - segStart
		dstOff := ovStart - reqStart
		copy(out[dstOff:], payload[16+int(segOff):16+int(segOff)+int(ovEnd-ovStart)])
	}
	return out, nil
}

// WriteFile writes data at off (POSIX growth semantics).
func (k *KFS) WriteFile(path string, off uint64, data []byte) error {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return ErrNoEntry
	}
	ino := k.dirents[di].ino
	nd := k.inodes[ino]
	if nd == nil || nd.kind == kfsKindDir {
		return ErrIsDir
	}
	if err := k.appendData(ino, off, data); err != nil {
		return err
	}
	end64 := uint64(off) + uint64(len(data))
	newSize := nd.size
	if end64 > uint64(newSize) {
		newSize = uint32(end64)
	}
	if newSize != nd.size {
		if err := k.append(kfsRecInode, kfsInodePayload(
			&kfsInode{ino: ino, uid: nd.uid, kind: nd.kind, size: newSize})); err != nil {
			return err
		}
		nd.size = newSize
	}
	k.data[ino] = nil // invalidate read cache after mutation
	return nil
}

// truncate emits the sentinel DATA record clearing an existing file.
func (k *KFS) truncate(ino uint32) error {
	var off [8]byte
	lib.Put64(off[:], kfsTruncSent)
	var dl [4]byte
	lib.Put32(dl[:], 0)
	pl := make([]byte, 0, 14)
	var i4 [4]byte
	lib.Put32(i4[:], ino)
	pl = append(pl, i4[:]...)
	pl = append(pl, off[:]...)
	pl = append(pl, dl[:]...)
	if err := k.append(kfsRecData, pl); err != nil {
		return err
	}
	nd := k.inodes[ino]
	if nd != nil && nd.size != 0 {
		if err := k.append(kfsRecInode, kfsInodePayload(
			&kfsInode{ino: ino, uid: nd.uid, kind: nd.kind, size: 0})); err != nil {
			return err
		}
		nd.size = 0
	}
	k.data[ino] = k.data[ino][:0]
	return nil
}

// SetUID stamps native ownership onto an inode (server calls after auth).
func (k *KFS) SetUID(path string, uid uint32) error {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return ErrNoEntry
	}
	nd := k.inodes[k.dirents[di].ino]
	if nd == nil {
		return ErrNoEntry
	}
	if err := k.append(kfsRecInode, kfsInodePayload(
		&kfsInode{ino: nd.ino, uid: uid, kind: nd.kind, size: nd.size})); err != nil {
		return err
	}
	nd.uid = uid
	return nil
}

// UID reports an inode's recorded owner (audit/metadata surface).
func (k *KFS) UID(path string) (uint32, error) {
	pino, leaf, err := k.walkParent(path)
	if err != nil {
		return 0, err
	}
	di := k.findDirent(pino, leaf)
	if di < 0 {
		return 0, ErrNoEntry
	}
	if nd := k.inodes[k.dirents[di].ino]; nd != nil {
		return nd.uid, nil
	}
	return 0, ErrNoEntry
}

// ---- payload encoders ----

func kfsInodePayload(n *kfsInode) []byte {
	p := make([]byte, 13)
	lib.Put32(p[0:], n.ino)
	lib.Put32(p[4:], n.uid)
	p[8] = n.kind
	lib.Put32(p[9:], n.size)
	return p
}

func (k *KFS) appendDirent(parent uint32, name string, ino uint32) error {
	pl := make([]byte, 0, 5+2+len(name)+4)
	var b4 [4]byte
	lib.Put32(b4[:], parent)
	pl = append(pl, b4[:]...)
	pl = append(pl, 0) // op=create
	pl = lib.AppendLStr(pl, name)
	var t4 [4]byte
	lib.Put32(t4[:], ino)
	pl = append(pl, t4[:]...)
	if err := k.append(kfsRecDirent, pl); err != nil {
		return err
	}
	k.dirents = append(k.dirents, kfsDirent{parent: parent, name: name, ino: ino})
	k.children[parent] = append(k.children[parent], len(k.dirents)-1)
	return nil
}

func (k *KFS) appendDirentDelete(parent uint32, name string) error {
	pl := make([]byte, 0, 5+2+len(name))
	var b4 [4]byte
	lib.Put32(b4[:], parent)
	pl = append(pl, b4[:]...)
	pl = append(pl, 1) // op=delete
	pl = lib.AppendLStr(pl, name)
	if err := k.append(kfsRecDirent, pl); err != nil {
		return err
	}
	di := k.findDirent(parent, name)
	if di >= 0 {
		k.dirents[di].dead = true
	}
	return nil
}

func (k *KFS) appendData(ino uint32, off uint64, data []byte) error {
	// payload: {u32 ino, u64 off, u32 dlen, bytes[dlen]} — 16-byte header
	pl := make([]byte, 16+len(data))
	lib.Put32(pl[0:], ino)
	lib.Put64(pl[4:], off)
	lib.Put32(pl[12:], uint32(len(data)))
	copy(pl[16:], data)
	return k.append(kfsRecData, pl)
}

func (k *KFS) allocIno() uint32 {
	ino := k.nextIno
	k.nextIno++
	return ino
}
