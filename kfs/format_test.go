package kfs

import (
	"bytes"
	"hash/crc32"
	"testing"
)

// TestFormatReplayRoundTrip is the proof that a volume written by this
// package's Formatter is readable by this package's Replay with the tree
// intact — the same guarantee tools/img seeding relies on for the guest.
func TestFormatReplayRoundTrip(t *testing.T) {
	dev := newMem(512, 2048) // 1 MiB
	if err := Format(dev); err != nil {
		t.Fatalf("Format: %v", err)
	}
	f, err := NewFormatter(dev)
	if err != nil {
		t.Fatalf("NewFormatter: %v", err)
	}
	// A tiny tree: /a.txt (bytes) and /sub (dir) containing /sub/b.txt.
	inoA, err := f.AppendInode(0, KindFile, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(RootIno, "a.txt", inoA); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendData(inoA, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	inoSub, err := f.AppendInode(0, KindDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(RootIno, "sub", inoSub); err != nil {
		t.Fatal(err)
	}
	inoB, err := f.AppendInode(0, KindFile, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(inoSub, "b.txt", inoB); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendData(inoB, 0, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	f.AppendCheckpoint()

	recs, ok := Replay(dev)
	if !ok {
		t.Fatal("Replay rejected a volume we just wrote")
	}
	// Rebuild enough state to prove the tree round-trips.
	files := map[uint32][]byte{}
	dirs := map[uint32]bool{RootIno: true}
	links := map[string]uint32{}
	child := map[uint32]map[string]uint32{}
	nextIno := uint32(RootIno + 1)
	for _, r := range recs {
		switch r.Type {
		case RecInode:
			ino, _, kind, _, ok := DecodeInode(r.Payload)
			if !ok {
				t.Fatalf("bad inode payload: %x", r.Payload)
			}
			if kind == KindDir {
				dirs[ino] = true
				child[ino] = map[string]uint32{}
			}
			if ino >= nextIno {
				nextIno = ino + 1
			}
		case RecDirent:
			parent, op, name, ino, ok := DecodeDirent(r.Payload)
			if !ok {
				t.Fatalf("bad dirent payload: %x", r.Payload)
			}
			if op == DirentCreate {
				// Do not assume the parent's INODE record preceded this
				// DIRENT; replay must not depend on record order.
				if child[parent] == nil {
					child[parent] = map[string]uint32{}
					dirs[parent] = true
				}
				child[parent][name] = ino
				links[name] = ino
			}
		case RecData:
			ino, _, data, ok := DecodeData(r.Payload)
			if !ok {
				t.Fatalf("bad data payload: %x", r.Payload)
			}
			files[ino] = append(files[ino], data...)
		case RecCheckpoint:
			if _, ok := DecodeCheckpoint(r.Payload); !ok {
				t.Fatalf("bad checkpoint: %x", r.Payload)
			}
		}
	}
	if !bytes.Equal(files[links["a.txt"]], []byte("hello")) {
		t.Errorf("a.txt = %q, want %q", files[links["a.txt"]], "hello")
	}
	sub := links["sub"]
	if !dirs[sub] {
		t.Errorf("sub is not a directory")
	}
	bIno := child[sub]["b.txt"]
	if !bytes.Equal(files[bIno], []byte("abc")) {
		t.Errorf("sub/b.txt = %q, want %q", files[bIno], "abc")
	}
	if nextIno <= inoB {
		t.Errorf("nextIno %d did not advance past last ino %d", nextIno, inoB)
	}
}

// TestRecordCRC catches a torn tail: mutating any payload byte must make the
// record fail to decode rather than decode to something plausible.
func TestRecordCRC(t *testing.T) {
	payload := []byte("abc")
	raw := EncodeRecord(RecData, payload)
	hdr, body := raw[:HdrLen], raw[HdrLen:]
	if _, ok := DecodeRecord(hdr, body); !ok {
		t.Fatal("freshly encoded record failed to decode")
	}
	if _, ok := DecodeRecord(hdr, []byte("abd")); ok {
		t.Error("corrupted payload decoded cleanly (CRC not checked)")
	}
	// Also corrupt the stored CRC itself.
	bad := append([]byte{}, raw...)
	bad[4] ^= 0xff
	if _, ok := DecodeRecord(bad[:HdrLen], bad[HdrLen:]); ok {
		t.Error("corrupted CRC field decoded cleanly")
	}
	if crc32.ChecksumIEEE(payload) == crc32.ChecksumIEEE([]byte("abd")) {
		t.Skip("collision; CRC sanity assumption broken")
	}
}

// TestSuperblockValidation: a volume whose log-start is not one sector in is
// rejected, so we never replay a layout we do not understand.
func TestSuperblockValidation(t *testing.T) {
	good := EncodeSuperblock(Superblock{BlockSize: 512, NumBlocks: 100, LogSector: LogStartSector, Version: FormatVersion})
	if _, ok := ParseSuperblock(good); !ok {
		t.Fatal("valid superblock rejected")
	}
	bad := append([]byte{}, good...)
	Put32(bad[16:], 99)
	if _, ok := ParseSuperblock(bad); ok {
		t.Error("superblock with bogus log-start sector accepted")
	}
	if _, ok := ParseSuperblock([]byte("NOTKFS1.............................")); ok {
		t.Error("non-KFS1 superblock accepted")
	}
}

// TestOffsetShiftsVolume proves the "option A" wrapper: a KFS1 volume written
// past a base sector is found by a reader that only ever sees sector 0 as the
// wrapped device's start.
func TestOffsetShiftsVolume(t *testing.T) {
	const base = 64 // sectors
	raw := newMem(512, 2048)
	// Something else (e.g. FAT16) occupies the low sectors.
	raw.Write(0, bytes.Repeat([]byte{0xEE}, 512))

	vol := NewOffset(raw, base)
	if err := Format(vol); err != nil {
		t.Fatalf("Format through offset: %v", err)
	}
	// Sector 0 of the underlying device must still be the foreign data.
	sector0 := make([]byte, 512)
	if err := raw.Read(0, sector0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sector0[:4], []byte{0xEE, 0xEE, 0xEE, 0xEE}) {
		t.Error("offset write clobbered sector 0 of the underlying device")
	}
	// A reader wrapping the same base must replay it.
	recs, ok := Replay(NewOffset(raw, base))
	if !ok || len(recs) < 2 {
		t.Fatalf("replay through offset: ok=%v recs=%d", ok, len(recs))
	}
}

// TestSplitSectorsAgreement is the guard for the convention that replaced
// hardcoded offsets: the builder and the guest must derive the SAME boundary
// from the image size, or the guest probes a sector the builder never wrote.
func TestSplitSectorsAgreement(t *testing.T) {
	for _, size := range []int64{4 << 20, 8 << 20, 64 << 20, 512 << 20} {
		base, err := SplitSectors(size)
		if err != nil {
			t.Fatalf("SplitSectors(%d): %v", size, err)
		}
		if base <= 0 || base*SectorSize > size/2 {
			t.Errorf("SplitSectors(%d) = %d, outside (0, %d]", size, base, size/2/SectorSize)
		}
		// The boundary must be sector aligned by construction.
		if base*SectorSize != (size/2)-(size/2)%SectorSize {
			t.Errorf("SplitSectors(%d) = %d is not the aligned half", size, base)
		}
	}
	if _, err := SplitSectors(512); err == nil {
		t.Error("SplitSectors accepted an image too small to split")
	}
}

// TestFindVolumeLocatesSplitVolume: given an image with foreign bytes at
// sector 0 and a KFS1 volume at the split boundary, FindVolume must hand back
// a Device that starts at the volume -- and must not mistake sector 0.
func TestFindVolumeLocatesSplitVolume(t *testing.T) {
	const size = 4 << 20
	base, err := SplitSectors(size)
	if err != nil {
		t.Fatal(err)
	}
	raw := newMem(512, uint64(size/512))
	// Something else owns the low half (the FAT16 boot volume).
	for i := 0; i < int(base); i++ {
		s := make([]byte, 512)
		if err := raw.Write(uint64(i), s); err != nil {
			t.Fatal(err)
		}
	}
	marker := make([]byte, 512)
	for i := range marker {
		marker[i] = 0xA5
	}
	if err := raw.Write(0, marker); err != nil {
		t.Fatal(err)
	}
	if err := Format(NewOffset(raw, uint64(base))); err != nil {
		t.Fatalf("format at split base: %v", err)
	}
	vol, err := FindVolume(raw)
	if err != nil {
		t.Fatalf("FindVolume could not find the volume at sector %d: %v", base, err)
	}
	if _, ok := vol.(*Offset); !ok {
		t.Errorf("FindVolume returned %T; want *Offset for a split image", vol)
	}
	if recs, ok := Replay(vol); !ok || len(recs) < 2 {
		t.Errorf("replay of the located volume: ok=%v recs=%d", ok, len(recs))
	}
	// A bare data image (KFS1 at sector 0) must be found unwrapped.
	bare := newMem(512, uint64(size/512))
	if err := Format(bare); err != nil {
		t.Fatal(err)
	}
	got, err := FindVolume(bare)
	if err != nil {
		t.Fatalf("FindVolume on a bare KFS1 image: %v", err)
	}
	if _, wrapped := got.(*Offset); wrapped {
		t.Error("FindVolume wrapped a bare KFS1 image that already starts at sector 0")
	}
}
