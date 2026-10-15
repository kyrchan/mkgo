package kfs

import "errors"

// memDevice is an in-memory Device for tests.
type memDevice struct {
	bs  uint32
	n   uint64
	buf []byte
}

func newMem(bs uint32, blocks uint64) *memDevice {
	return &memDevice{bs: bs, n: blocks, buf: make([]byte, int(blocks)*int(bs))}
}

func (m *memDevice) Read(lba uint64, p []byte) error {
	if lba >= m.n {
		return errors.New("read past end")
	}
	copy(p, m.buf[int(lba)*int(m.bs):])
	return nil
}

func (m *memDevice) Write(lba uint64, p []byte) error {
	if lba >= m.n {
		return errors.New("write past end")
	}
	copy(m.buf[int(lba)*int(m.bs):], p)
	return nil
}

func (m *memDevice) Geometry() (uint32, uint64) { return m.bs, m.n }

// Formatter is an append-only KFS1 log writer. It is the host-side half of
// the format: services/fs has its own replay/mutation layer on top of the
// same encoding, but the framing, superblock and record pacing live here so
// the two cannot drift.
type Formatter struct {
	dev     Device
	logEnd  int64
	wOff    int64
	recs    int
	nextIno uint32
}

// NewFormatter prepares a device for appending without erasing it. The device
// must already hold a valid superblock (see Format).
func NewFormatter(dev Device) (*Formatter, error) {
	bs, n := dev.Geometry()
	if bs == 0 || n == 0 {
		return nil, ErrBadSB
	}
	// Trust the superblock for the log start, but bound it by geometry.
	return &Formatter{
		dev:     dev,
		logEnd:  int64(n) * int64(bs),
		wOff:    LogStart,
		nextIno: RootIno + 1,
	}, nil
}

// Format lays down a fresh empty volume: superblock, root inode, checkpoint.
// This is what tools/img calls; it is NOT called at boot (fs.wasm refuses to
// format implicitly).
func Format(dev Device) error {
	bs, n := dev.Geometry()
	if n < 16 {
		return ErrBadSB
	}
	sb := EncodeSuperblock(Superblock{
		BlockSize: bs,
		NumBlocks: n,
		LogSector: LogStartSector,
		Version:   FormatVersion,
	})
	if err := dev.Write(0, sb); err != nil {
		return err
	}
	f, err := NewFormatter(dev)
	if err != nil {
		return err
	}
	if err := f.Append(RecInode, InodePayload(RootIno, 0, KindDir, 0)); err != nil {
		return err
	}
	return f.AppendCheckpoint()
}

// Append writes one framed record, pacing a CHECKPOINT every CPEvery records.
func (f *Formatter) Append(typ byte, payload []byte) error {
	raw := EncodeRecord(typ, payload)
	if f.wOff+int64(len(raw)) > f.logEnd {
		return ErrNoSpace
	}
	if err := f.writeAt(f.wOff, raw); err != nil {
		return err
	}
	f.wOff += int64(len(raw))
	if typ != RecCheckpoint {
		f.recs++
		if f.recs%CPEvery == 0 {
			return f.AppendCheckpoint()
		}
	}
	return nil
}

// AppendCheckpoint records the next-inode watermark.
func (f *Formatter) AppendCheckpoint() error {
	return f.Append(RecCheckpoint, CheckpointPayload(f.nextIno))
}

// SetNextIno lets a seeding caller declare the next inode to hand out, so the
// first checkpoint is meaningful.
func (f *Formatter) SetNextIno(v uint32) { f.nextIno = v }

// AppendInode records a new inode and returns the ino.
func (f *Formatter) AppendInode(uid uint32, kind byte, size uint32) (uint32, error) {
	ino := f.nextIno
	if err := f.Append(RecInode, InodePayload(ino, uid, kind, size)); err != nil {
		return 0, err
	}
	f.nextIno++
	return ino, nil
}

// AppendDirent links name under parent.
func (f *Formatter) AppendDirent(parent uint32, name string, ino uint32) error {
	return f.Append(RecDirent, DirentPayload(parent, DirentCreate, name, ino))
}

// AppendData writes a data chunk at off. off==TruncSentinel truncates.
func (f *Formatter) AppendData(ino uint32, off uint64, data []byte) error {
	if off == TruncSentinel {
		return f.Append(RecData, DataPayload(ino, off, nil))
	}
	return f.Append(RecData, DataPayload(ino, off, data))
}

// writeAt writes bytes at an absolute device byte offset, using a
// read-modify-write for any partial trailing sector.
func (f *Formatter) writeAt(off int64, buf []byte) error {
	secSize := int64(SectorSize)
	if off%secSize == 0 && int64(len(buf))%secSize == 0 {
		return f.dev.Write(uint64(off/secSize), buf)
	}
	sec := make([]byte, secSize)
	for done := int64(0); done < int64(len(buf)); {
		si := off + done
		within := si % secSize
		take := secSize - within
		if take > int64(len(buf))-done {
			take = int64(len(buf)) - done
		}
		if err := f.dev.Read(uint64(si/secSize), sec); err != nil {
			return err
		}
		copy(sec[within:within+take], buf[done:done+take])
		if err := f.dev.Write(uint64(si/secSize), sec); err != nil {
			return err
		}
		done += take
	}
	return nil
}

// Replay is the reference reader: it scans the log and returns the records it
// could validate, stopping at the first torn or corrupt record. It exists so
// the format can be tested against itself here; services/fs's MountKFS is the
// production reader and must agree with it.
func Replay(dev Device) ([]Record, bool) {
	bs, n := dev.Geometry()
	sec := make([]byte, bs)
	if err := dev.Read(0, sec); err != nil {
		return nil, false
	}
	sb, ok := ParseSuperblock(sec)
	if !ok {
		return nil, false
	}
	logEnd := int64(n) * int64(bs)
	pos := int64(sb.LogSector) * int64(bs)
	var recs []Record
	hdr := make([]byte, HdrLen)
	for pos+HdrLen <= logEnd {
		if err := dev.Read(uint64(pos/int64(bs)), hdr); err != nil {
			break
		}
		// A header may straddle a sector; read it byte-accurately.
		hdr = readBytes(dev, pos, HdrLen, bs)
		if hdr == nil {
			break
		}
		plen := int(Get32(hdr[0:]))
		if plen < 0 || plen > MaxPayload || pos+HdrLen+int64(plen) > logEnd {
			break
		}
		payload := readBytes(dev, pos+HdrLen, plen, bs)
		if payload == nil {
			break
		}
		r, ok := DecodeRecord(hdr, payload)
		if !ok {
			break // torn tail
		}
		recs = append(recs, r)
		pos += int64(HdrLen + plen)
	}
	return recs, true
}

// readBytes reads n bytes starting at an absolute device byte offset,
// spanning sector boundaries as needed.
func readBytes(dev Device, off int64, n int, bs uint32) []byte {
	out := make([]byte, 0, n)
	sec := make([]byte, bs)
	for len(out) < n {
		si := off + int64(len(out))
		if err := dev.Read(uint64(si/int64(bs)), sec); err != nil {
			return nil
		}
		within := si % int64(bs)
		take := int64(bs) - within
		if take > int64(n-len(out)) {
			take = int64(n - len(out))
		}
		out = append(out, sec[within:within+take]...)
	}
	return out
}
