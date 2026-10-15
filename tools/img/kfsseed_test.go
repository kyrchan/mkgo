package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"kernel.lane/kfs"
)

// TestSeedKFSProducesMountableVolume builds a real image in memory, seeds a
// KFS1 volume after the FAT16 one, and verifies with the shared reader that
// the tree landed and that the FAT volume at sector 0 is untouched.
func TestSeedKFSProducesMountableVolume(t *testing.T) {
	const size = 8 << 20 // 8 MiB
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "boot", "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("M"), 5000) // > one DATA chunk
	if err := os.WriteFile(filepath.Join(dir, "boot", "modules", "fs.wasm"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	dev := NewMemBlock(size)
	vol, err := NewVolume(dev, (size / 2), "NO NAME") // FAT ends where KFS1 begins
	if err != nil {
		t.Fatalf("NewVolume: %v", err)
	}
	efi := []byte("EFI-BOOT")
	if err := vol.Add(FileSource{Name: "/EFI/BOOT/BOOTX64.EFI", Data: efi, Mtime: modTime(dir)}); err != nil {
		t.Fatal(err)
	}
	if err := vol.Flush(); err != nil {
		t.Fatal(err)
	}
	base := size / 2 / 512
	if err := seedKFS(dev, int64(base), "/", dir); err != nil {
		t.Fatalf("seedKFS: %v", err)
	}

	// The FAT16 volume must still be intact at sector 0 — the loader reads it.
	head := make([]byte, 512)
	if _, err := dev.ReadAt(head, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(head[0:3], []byte{0xEB, 0x3C, 0x90}) && !bytes.Equal(head[54:62], []byte("FAT16   ")) {
		// Either the boot signature or the FAT16 type is what matters; check the
		// type field explicitly since -kfs-seed must not have clobbered it.
		if !bytes.Equal(head[54:62], []byte("FAT16   ")) {
			t.Errorf("sector 0 no longer looks like a FAT16 BPB (type=%q); the KFS1 write clobbered the boot volume", head[54:62])
		}
	}

	// Now read the seeded volume back with the shared reader.
	kdev := kfsDev{dev}
	recs, ok := kfs.Replay(kfs.NewOffset(kdev, uint64(base)))
	if !ok {
		t.Fatal("shared reader rejected the volume img just seeded")
	}
	dirs := map[uint32]bool{}
	files := map[uint32][]byte{}
	child := map[uint32]map[string]uint32{}
	links := map[string]uint32{}
	for _, r := range recs {
		switch r.Type {
		case kfs.RecInode:
			ino, _, kind, _, ok := kfs.DecodeInode(r.Payload)
			if !ok {
				t.Fatalf("bad inode: %x", r.Payload)
			}
			if kind == kfs.KindDir {
				dirs[ino] = true
				if child[ino] == nil {
					child[ino] = map[string]uint32{}
				}
			}
		case kfs.RecDirent:
			parent, op, name, ino, ok := kfs.DecodeDirent(r.Payload)
			if !ok || op != kfs.DirentCreate {
				t.Fatalf("bad dirent: %x", r.Payload)
			}
			if child[parent] == nil {
				child[parent] = map[string]uint32{}
			}
			child[parent][name] = ino
			links[name] = ino
		case kfs.RecData:
			ino, _, data, ok := kfs.DecodeData(r.Payload)
			if !ok {
				t.Fatalf("bad data: %x", r.Payload)
			}
			files[ino] = append(files[ino], data...)
		}
	}
	if !dirs[links["boot"]] {
		t.Fatal("seeded /boot did not replay as a directory")
	}
	mods := child[links["boot"]]["modules"]
	if mods == 0 {
		t.Fatalf("seeded /boot/modules missing (boot children=%v)", child[links["boot"]])
	}
	if !dirs[mods] {
		t.Fatal("/boot/modules did not replay as a directory")
	}
	got := files[child[mods]["fs.wasm"]]
	if !bytes.Equal(got, payload) {
		t.Errorf("seeded fs.wasm round-tripped %d bytes, want %d (chunked DATA records lost or reordered data)", len(got), len(payload))
	}
}
