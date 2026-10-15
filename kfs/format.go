// Package kfs is the single source of truth for the KFS1 on-disk format.
//
// It exists because two independent Go modules need byte-identical KFS1:
// services/fs (the guest, which mounts and replays the log) and tools/img
// (the host image builder, which must SEED a volume). Before this module the
// format lived only inside services/fs, which is package main and cannot be
// imported -- so a host-side writer would have had to reimplement the record
// framing by hand. A one-byte divergence would not fail loudly: the guest
// would mount the volume, replay a truncated log, and serve an EMPTY
// filesystem while the gate still went green. That is the exact
// "certifies a ghost" failure this package exists to prevent.
//
// Format (services/fs/kfs.go is now a consumer of this file):
//
//	Sector 0 of the volume: superblock — "KFS1", block size, block count,
//	  log-start sector, format version.
//	Log region: append-only byte stream of CRC32'd records starting one
//	  sector after the superblock. Each record is
//	    {u32 payloadLen, u32 crc32(payload), u8 type, payload[payloadLen]}
//	  with types 1=INODE, 2=DIRENT, 3=DATA, 4=CHECKPOINT.
//	Recovery: sequential replay; the first record whose framing or CRC fails
//	  ends the scan and the tail is dropped as torn.
package kfs

import (
	"errors"
	"hash/crc32"
)

// Sector geometry and record framing constants.
const (
	SectorSize = 512
	// LogStart is the byte offset of the log relative to the volume's first
	// sector. One sector holds the superblock.
	LogStart = 512
	// LogStartSector is LogStart expressed in sectors, as stored in the
	// superblock.
	LogStartSector = LogStart / SectorSize
	HdrLen         = 9 // u32 len + u32 crc + u8 type
	MaxPayload     = 65536
	CPEvery        = 64

	RootIno = 1

	KindFile byte = 0
	KindDir  byte = 1

	RecInode      byte = 1
	RecDirent     byte = 2
	RecData       byte = 3
	RecCheckpoint byte = 4

	// FormatVersion is the superblock version this package writes.
	FormatVersion = 1
)

// ErrBadSB means the bytes at the volume start are not a KFS1 superblock.
var ErrBadSB = errors.New("kfs: bad superblock")

// ErrNoSpace means the log region is full.
var ErrNoSpace = errors.New("kfs: no space")

// TruncSentinel is the DATA record offset meaning "truncate the file".
const TruncSentinel = ^uint64(0)

// Device is the 512-byte-sector block surface KFS1 is defined over.
// services/fs's BlockDev satisfies it as-is; tools/img adapts its own
// byte-oriented BlockDevice with a few lines.
type Device interface {
	Read(lba uint64, buf []byte) error
	Write(lba uint64, buf []byte) error
	Geometry() (blkSize uint32, numBlocks uint64)
}

// Offset presents a suffix of a device as a whole device, so a KFS1 volume
// can live AFTER another filesystem on the same physical disk. Every lba is
// shifted by base sectors; the reported block count shrinks accordingly.
//
// This is the whole of "option A": the boot volume keeps sector 0, and the
// KFS1 volume starts at a sector the image builder chooses.
type Offset struct {
	Inner Device
	Base  uint64 // sectors
}

// NewOffset wraps inner so that lba 0 of the result is sector Base of inner.
func NewOffset(inner Device, baseSectors uint64) *Offset {
	return &Offset{Inner: inner, Base: baseSectors}
}

func (o *Offset) Read(lba uint64, buf []byte) error { return o.Inner.Read(o.Base+lba, buf) }

func (o *Offset) Write(lba uint64, buf []byte) error {
	return o.Inner.Write(o.Base+lba, buf)
}

func (o *Offset) Geometry() (uint32, uint64) {
	bs, n := o.Inner.Geometry()
	if o.Base >= n {
		return bs, 0
	}
	return bs, n - o.Base
}

// Put32/Put64/Get32/Get64 are LITTLE-endian accessors, byte-for-byte
// identical to guests/lib's Put32/Get32/Put64. KFS1 is little-endian on
// disk; this was verified against the guest, not assumed -- an earlier
// big-endian version of this file produced a volume the guest rejected with
// "bad superblock", which is the whole failure mode this package exists to
// prevent.
func Put32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func Put64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

func Get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func Get64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// AppendLStr appends a u16 length-prefixed string.
func AppendLStr(dst []byte, s string) []byte {
	var l [2]byte
	l[0] = byte(len(s))
	l[1] = byte(len(s) >> 8)
	dst = append(dst, l[:]...)
	return append(dst, s...)
}

// LStr reads a u16 length-prefixed string, returning the next offset.
func LStr(b []byte, off int) (string, int, bool) {
	if off+2 > len(b) {
		return "", 0, false
	}
	n := int(b[off]) | int(b[off+1])<<8
	off += 2
	if off+n > len(b) {
		return "", 0, false
	}
	return string(b[off : off+n]), off + n, true
}

// Superblock is the decoded sector-0 header.
type Superblock struct {
	BlockSize uint32
	NumBlocks uint64
	LogSector uint32
	Version   uint32
}

// EncodeSuperblock renders the 512-byte superblock sector.
func EncodeSuperblock(sb Superblock) []byte {
	b := make([]byte, SectorSize)
	copy(b[0:4], "KFS1")
	Put32(b[4:], sb.BlockSize)
	Put64(b[8:], sb.NumBlocks)
	Put32(b[16:], sb.LogSector)
	Put32(b[20:], sb.Version)
	return b
}

// ParseSuperblock validates and decodes a superblock sector.
func ParseSuperblock(b []byte) (Superblock, bool) {
	var sb Superblock
	if len(b) < 24 || string(b[0:4]) != "KFS1" {
		return sb, false
	}
	sb.BlockSize = Get32(b[4:])
	sb.NumBlocks = Get64(b[8:])
	sb.LogSector = Get32(b[16:])
	sb.Version = Get32(b[20:])
	// LogSector is the only field replay depends on; a volume whose log does
	// not start one sector in is not something we know how to read.
	if sb.LogSector != LogStartSector {
		return sb, false
	}
	return sb, true
}

// Record is one framed log record.
type Record struct {
	Type    byte
	Payload []byte
}

// EncodeRecord frames a payload: {u32 len, u32 crc32(payload), u8 type, ...}.
func EncodeRecord(typ byte, payload []byte) []byte {
	raw := make([]byte, HdrLen+len(payload))
	Put32(raw[0:], uint32(len(payload)))
	Put32(raw[4:], crc32.ChecksumIEEE(payload))
	raw[8] = typ
	copy(raw[HdrLen:], payload)
	return raw
}

// DecodeRecord validates framing and CRC, returning the payload. A framing
// or CRC failure is reported as ok=false — that is the torn tail, not an error
// to propagate.
func DecodeRecord(hdr, payload []byte) (Record, bool) {
	var r Record
	if len(hdr) < HdrLen {
		return r, false
	}
	plen := int(Get32(hdr[0:]))
	crc := Get32(hdr[4:])
	if plen < 0 || plen > MaxPayload || len(payload) < plen {
		return r, false
	}
	if crc32.ChecksumIEEE(payload[:plen]) != crc {
		return r, false
	}
	r.Type = hdr[8]
	r.Payload = payload[:plen]
	return r, true
}

// InodePayload encodes an INODE record: {u32 ino, u32 uid, u8 kind, u32 size}.
func InodePayload(ino, uid uint32, kind byte, size uint32) []byte {
	p := make([]byte, 13)
	Put32(p[0:], ino)
	Put32(p[4:], uid)
	p[8] = kind
	Put32(p[9:], size)
	return p
}

// DecodeInode decodes an INODE payload.
func DecodeInode(p []byte) (ino, uid uint32, kind byte, size uint32, ok bool) {
	if len(p) < 13 {
		return 0, 0, 0, 0, false
	}
	return Get32(p[0:]), Get32(p[4:]), p[8], Get32(p[9:]), true
}

// DirentOp is the DIRENT operation.
const (
	DirentCreate byte = 0
	DirentDelete byte = 1
)

// DirentPayload encodes a DIRENT: {u32 parent, u8 op, lstr name, u32 ino}.
// The ino is omitted for a delete.
func DirentPayload(parent uint32, op byte, name string, ino uint32) []byte {
	p := make([]byte, 0, 5+2+len(name)+4)
	var b4 [4]byte
	Put32(b4[:], parent)
	p = append(p, b4[:]...)
	p = append(p, op)
	p = AppendLStr(p, name)
	if op == DirentCreate {
		var t4 [4]byte
		Put32(t4[:], ino)
		p = append(p, t4[:]...)
	}
	return p
}

// DecodeDirent decodes a DIRENT payload.
func DecodeDirent(p []byte) (parent uint32, op byte, name string, ino uint32, ok bool) {
	if len(p) < 7 {
		return 0, 0, "", 0, false
	}
	parent = Get32(p[0:])
	op = p[4]
	name, next, ok2 := LStr(p, 5)
	if !ok2 {
		return 0, 0, "", 0, false
	}
	if op == DirentCreate {
		if next+4 > len(p) {
			return 0, 0, "", 0, false
		}
		ino = Get32(p[next:])
	}
	return parent, op, name, ino, true
}

// DataPayload encodes a DATA record: {u32 ino, u64 off, u32 dlen, bytes}.
func DataPayload(ino uint32, off uint64, data []byte) []byte {
	p := make([]byte, 16+len(data))
	Put32(p[0:], ino)
	Put64(p[4:], off)
	Put32(p[12:], uint32(len(data)))
	copy(p[16:], data)
	return p
}

// DecodeData decodes a DATA payload.
func DecodeData(p []byte) (ino uint32, off uint64, data []byte, ok bool) {
	if len(p) < 16 {
		return 0, 0, nil, false
	}
	dlen := int(Get32(p[12:]))
	if len(p) < 16+dlen {
		return 0, 0, nil, false
	}
	return Get32(p[0:]), Get64(p[4:]), p[16 : 16+dlen], true
}

// CheckpointPayload encodes a CHECKPOINT: u32 nextIno.
func CheckpointPayload(nextIno uint32) []byte {
	p := make([]byte, 4)
	Put32(p, nextIno)
	return p
}

// DecodeCheckpoint decodes a CHECKPOINT payload.
func DecodeCheckpoint(p []byte) (uint32, bool) {
	if len(p) < 4 {
		return 0, false
	}
	return Get32(p), true
}

// SplitSectors returns the sector at which the data (KFS1) volume should begin
// on an image that also carries a boot (FAT16) volume: the two volumes each
// get half the image.
//
// The image builder and the guest must agree on this boundary, and they
// previously did not -- tools/img derived it inline while fs.wasm had no idea
// where to look. A disagreement there is silent: the guest probes the wrong
// sector, finds no superblock, and refuses to serve, or worse, finds stale
// bytes that look like a volume. So the rule lives here and is derived from
// the image size on both sides -- nothing is hardcoded twice.
//
// If the volumes are ever given independent sizes, this should become a field
// in the superblock instead of a convention; noted here so the convention is
// not mistaken for something sturdier than it is.
func SplitSectors(imageBytes int64) (int64, error) {
	if imageBytes < 2*SectorSize {
		return 0, errors.New("kfs: image too small to hold a boot and a data volume")
	}
	half := imageBytes / 2
	half -= half % SectorSize // whole sectors only
	if half <= 0 {
		return 0, errors.New("kfs: image too small to split")
	}
	return half / SectorSize, nil
}

// FindVolume locates a KFS1 volume on dev: at sector 0 (a bare data image),
// or at the SplitSectors boundary when the image also carries a boot volume.
//
// It returns a Device that reads as the volume, whichever was found.
func FindVolume(dev Device) (Device, error) {
	if _, ok := ParseSuperblock(readSector(dev, 0)); ok {
		return dev, nil
	}
	bs, n := dev.Geometry()
	base, err := SplitSectors(int64(n) * int64(bs))
	if err != nil {
		return nil, err
	}
	if _, ok := ParseSuperblock(readSector(dev, uint64(base))); ok {
		return NewOffset(dev, uint64(base)), nil
	}
	return nil, ErrBadSB
}

func readSector(dev Device, lba uint64) []byte {
	bs, _ := dev.Geometry()
	if bs == 0 {
		return nil
	}
	buf := make([]byte, bs)
	if err := dev.Read(lba, buf); err != nil {
		return nil
	}
	return buf
}
