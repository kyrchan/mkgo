package main

import (
	"bytes"
	"testing"

	kfs "kernel.lane/kfs"
)

// ramKFS is an in-memory block device. services/fs's BlockDev and the shared
// package's kfs.Device have identical method sets, so this one type satisfies
// both and the guest and the host-side writer can be pointed at the same
// bytes.
type ramKFS struct {
	bs  uint32
	buf []byte
}

func newRamKFS(blocks uint64) *ramKFS {
	return &ramKFS{bs: 512, buf: make([]byte, int(blocks)*512)}
}

func (d *ramKFS) Read(lba uint64, p []byte) error {
	if lba >= uint64(len(d.buf)/int(d.bs)) {
		return ErrNoSpace
	}
	copy(p, d.buf[int(lba)*int(d.bs):])
	return nil
}

func (d *ramKFS) Write(lba uint64, p []byte) error {
	if lba >= uint64(len(d.buf)/int(d.bs)) {
		return ErrNoSpace
	}
	copy(d.buf[int(lba)*int(d.bs):], p)
	return nil
}

func (d *ramKFS) Geometry() (uint32, uint64) {
	return d.bs, uint64(len(d.buf)) / uint64(d.bs)
}

// TestSharedWriterVolumeMountsInGuest is the guarantee the host-side seeder
// rests on: a volume written by the shared kfs module is mounted by the
// UNMODIFIED guest MountKFS and serves the seeded tree.
//
// Without this test, a one-byte divergence between the two encoders would not
// fail loudly. The guest would replay a truncated log, end up with an empty
// in-memory tree, and serve nothing -- while the image-building gate reported
// success. That is the "certifies a ghost" failure class, and it is precisely
// what putting the format in one tested module is meant to prevent.
func TestSharedWriterVolumeMountsInGuest(t *testing.T) {
	dev := newRamKFS(2048) // 1 MiB

	if err := kfs.Format(dev); err != nil {
		t.Fatalf("shared kfs.Format: %v", err)
	}
	f, err := kfs.NewFormatter(dev)
	if err != nil {
		t.Fatalf("shared NewFormatter: %v", err)
	}

	// Seed the tree the way the p18 image will need: a /boot/modules
	// directory holding a module file.
	boot, err := f.AppendInode(0, kfs.KindDir, 0)
	if err != nil {
		t.Fatalf("seed /boot inode: %v", err)
	}
	if err := f.AppendDirent(kfs.RootIno, "boot", boot); err != nil {
		t.Fatalf("seed /boot dirent: %v", err)
	}
	mods, err := f.AppendInode(0, kfs.KindDir, 0)
	if err != nil {
		t.Fatalf("seed /boot/modules inode: %v", err)
	}
	if err := f.AppendDirent(boot, "modules", mods); err != nil {
		t.Fatalf("seed /boot/modules dirent: %v", err)
	}
	payload := []byte("\x00asm\x01\x00\x00\x00 seeded module bytes")
	mod, err := f.AppendInode(0, kfs.KindFile, uint32(len(payload)))
	if err != nil {
		t.Fatalf("seed module inode: %v", err)
	}
	if err := f.AppendData(mod, 0, payload); err != nil {
		t.Fatalf("seed module data: %v", err)
	}
	if err := f.AppendDirent(mods, "fs.wasm", mod); err != nil {
		t.Fatalf("seed module dirent: %v", err)
	}
	if err := f.AppendCheckpoint(); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	// Now the guest, entirely unmodified, mounts what the host wrote.
	k, err := MountKFS(dev)
	if err != nil {
		t.Fatalf("guest MountKFS on a shared-written volume: %v", err)
	}
	for _, dir := range []string{"/boot", "/boot/modules"} {
		st, err := k.Stat(dir)
		if err != nil {
			t.Fatalf("guest Stat(%s) on seeded volume: %v", dir, err)
		}
		if !st.IsDir() {
			t.Errorf("guest Stat(%s): not a directory (%+v)", dir, st)
		}
	}
	list, err := k.List("/boot/modules")
	if err != nil {
		t.Fatalf("guest List(/boot/modules): %v", err)
	}
	if len(list) != 1 || list[0].Name != "fs.wasm" {
		t.Fatalf("guest List(/boot/modules) = %+v, want exactly fs.wasm", list)
	}
	if list[0].Size != uint32(len(payload)) {
		t.Errorf("seeded size = %d, want %d", list[0].Size, len(payload))
	}
	buf := make([]byte, len(payload))
	n, err := k.ReadFile("/boot/modules/fs.wasm", 0, buf)
	if err != nil {
		t.Fatalf("guest ReadFile of seeded module: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Errorf("seeded content round-tripped as %q, want %q", buf[:n], payload)
	}
}

// TestGuestWrittenVolumeParsesWithSharedReader is the converse direction: a
// volume the GUEST wrote must be readable by the shared reader, which is what
// tooling (and any future host-side inspector) will use.
func TestGuestWrittenVolumeParsesWithSharedReader(t *testing.T) {
	dev := newRamKFS(2048)
	if err := FormatKFS(dev); err != nil {
		t.Fatalf("guest FormatKFS: %v", err)
	}
	k, err := MountKFS(dev)
	if err != nil {
		t.Fatalf("guest MountKFS: %v", err)
	}
	if err := k.Mkdir("/etc"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := k.Create("/etc/motd"); err != nil {
		t.Fatalf("create: %v", err)
	}
	want := []byte("Welcome\n")
	if err := k.WriteFile("/etc/motd", 0, want); err != nil {
		t.Fatalf("write: %v", err)
	}

	recs, ok := kfs.Replay(dev)
	if !ok {
		t.Fatal("shared reader rejected a volume the guest wrote")
	}
	var sawInode, sawDirent, sawData bool
	for _, r := range recs {
		switch r.Type {
		case kfs.RecInode:
			ino, _, kind, _, ok := kfs.DecodeInode(r.Payload)
			if !ok {
				t.Fatalf("shared reader could not decode a guest INODE: %x", r.Payload)
			}
			if ino != kfs.RootIno && kind == kfs.KindDir {
				sawInode = true
			}
		case kfs.RecDirent:
			_, _, name, _, ok := kfs.DecodeDirent(r.Payload)
			if !ok {
				t.Fatalf("shared reader could not decode a guest DIRENT: %x", r.Payload)
			}
			if name == "motd" {
				sawDirent = true
			}
		case kfs.RecData:
			_, _, data, ok := kfs.DecodeData(r.Payload)
			if !ok {
				t.Fatalf("shared reader could not decode guest DATA: %x", r.Payload)
			}
			if bytes.Equal(data, want) {
				sawData = true
			}
		}
	}
	if !sawInode {
		t.Error("no decoded directory inode from the guest-written volume")
	}
	if !sawDirent {
		t.Error(`no dirent named "motd" in the guest-written volume`)
	}
	if !sawData {
		t.Errorf("guest-written DATA record did not decode to %q", want)
	}
}

// TestGuestMountsSplitVolume is the end-to-end proof for option (A) at the
// guest level, without booting anything: an image whose sector 0 belongs to
// another filesystem, with a KFS1 data volume at the split boundary, must be
// located and mounted by exactly the path fs.wasm takes at boot.
//
// Before the boundary was derived from one shared rule, the guest had no way to
// know where the builder had put the volume -- and the failure would have been
// a refusal to serve, or worse, an empty tree reported as success.
func TestGuestMountsSplitVolume(t *testing.T) {
	const size = 4 << 20
	base, err := kfs.SplitSectors(size)
	if err != nil {
		t.Fatal(err)
	}
	dev := newRamKFS(uint64(size / 512))

	// The low half is the boot volume's business, not ours; fill it with
	// non-zero bytes so a probe at sector 0 cannot accidentally match.
	junk := bytes.Repeat([]byte{0xA5}, 512)
	for lba := uint64(0); lba < uint64(base); lba++ {
		if err := dev.Write(lba, junk); err != nil {
			t.Fatal(err)
		}
	}

	// Seed the data volume the way tools/img does.
	vol := kfs.NewOffset(dev, uint64(base))
	if err := kfs.Format(vol); err != nil {
		t.Fatal(err)
	}
	f, err := kfs.NewFormatter(vol)
	if err != nil {
		t.Fatal(err)
	}
	mods, err := f.AppendInode(0, kfs.KindDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(kfs.RootIno, "boot", mods); err != nil {
		t.Fatal(err)
	}
	inner, err := f.AppendInode(0, kfs.KindDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(mods, "modules", inner); err != nil {
		t.Fatal(err)
	}
	want := []byte("module bytes in a split volume")
	ino, err := f.AppendInode(0, kfs.KindFile, uint32(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.AppendData(ino, 0, want); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendDirent(inner, "fs.wasm", ino); err != nil {
		t.Fatal(err)
	}

	// The guest's real boot path.
	k, err := mountAuto(dev)
	if err != nil {
		t.Fatalf("guest mountAuto on a split image (volume at sector %d): %v", base, err)
	}
	if _, err := k.Stat("/boot"); err != nil {
		t.Fatalf("Stat(/boot) on the split volume: %v", err)
	}
	list, err := k.List("/boot")
	if err != nil {
		t.Fatalf("List(/boot): %v", err)
	}
	if len(list) != 1 || list[0].Name != "modules" {
		t.Fatalf("List(/boot) = %+v, want modules", list)
	}
	buf := make([]byte, len(want))
	n, err := k.ReadFile("/boot/modules/fs.wasm", 0, buf)
	if err != nil {
		t.Fatalf("ReadFile on the split volume: %v", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Errorf("split-volume content = %q, want %q", buf[:n], want)
	}
}
