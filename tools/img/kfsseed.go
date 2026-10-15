package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kernel.lane/kfs"
)

// kfsDev adapts tools/img's byte-oriented BlockDevice to the shared
// package's sector-oriented kfs.Device.
type kfsDev struct{ d BlockDevice }

func (k kfsDev) Read(lba uint64, p []byte) error {
	_, err := k.d.ReadAt(p, int64(lba)*kfs.SectorSize)
	return err
}

func (k kfsDev) Write(lba uint64, p []byte) error {
	_, err := k.d.WriteAt(p, int64(lba)*kfs.SectorSize)
	return err
}

func (k kfsDev) Geometry() (uint32, uint64) {
	return kfs.SectorSize, uint64(k.d.Size()) / kfs.SectorSize
}

// seedKFS lays down a KFS1 volume starting at baseSector and fills it from a
// host directory tree.
//
// This exists because fs.wasm mounts KFS1 and no longer formats implicitly, so
// a gate image that boots fs.wasm must ALREADY carry a valid volume. The FAT16
// volume keeps sector 0 (the loader needs it for BOOTX64.EFI and /boot on the
// ESP); the KFS1 volume starts after it. The two encoders are proven
// byte-identical by the interop tests in services/fs -- a mismatch here would
// otherwise produce a volume the guest mounts into an empty tree while the
// image gate reports success.
func seedKFS(dev BlockDevice, baseSector int64, prefix, srcDir string) error {
	vol := kfs.NewOffset(kfsDev{dev}, uint64(baseSector))
	if err := kfs.Format(vol); err != nil {
		return fmt.Errorf("img: kfs format at sector %d: %w", baseSector, err)
	}
	f, err := kfs.NewFormatter(vol)
	if err != nil {
		return fmt.Errorf("img: kfs formatter: %w", err)
	}

	root := uint32(kfs.RootIno)

	// parentOf resolves a volume path to the inode of that path, creating any
	// missing intermediate directories. The accumulated prefix (including the
	// component being resolved) is the lookup key -- looking up the PARENT and
	// continuing resolves the parent instead, which silently files nested
	// entries under the wrong directory.
	mkdirs := map[string]uint32{"/": root}
	parentOf := func(path string) (uint32, string, error) {
		path = "/" + strings.Trim(path, "/")
		if path == "/" {
			return root, "", nil
		}
		cur := root
		acc := ""
		for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
			parent := cur
			acc = pathJoin(acc, part)
			if ino, ok := mkdirs[acc]; ok {
				cur = ino
				continue
			}
			ino, err := f.AppendInode(0, kfs.KindDir, 0)
			if err != nil {
				return 0, "", err
			}
			if err := f.AppendDirent(parent, part, ino); err != nil {
				return 0, "", err
			}
			mkdirs[acc] = ino
			cur = ino
		}
		return cur, "", nil
	}

	// parentIno resolves a volume path to its inode, creating intermediates.
	parentIno := func(path string) (uint32, error) {
		ino, _, err := parentOf(path)
		return ino, err
	}

	// Walk deterministically: sorted names, dirs before their contents.
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := os.ReadDir(filepath.Join(srcDir, rel))
		if err != nil {
			return err
		}
		names := make([]string, 0, len(entries))
		dirs := map[string]bool{}
		for _, e := range entries {
			names = append(names, e.Name())
			dirs[e.Name()] = e.IsDir()
		}
		sort.Strings(names)
		for _, name := range names {
			child := filepath.Join(rel, name)
			// The volume path must mirror the HOST tree under the prefix, or
			// every nested entry would collapse onto the prefix root.
			volPath := pathJoin(prefix, child)
			parent, err := parentIno(dirOf(volPath))
			if err != nil {
				return err
			}
			if dirs[name] {
				ino, err := f.AppendInode(0, kfs.KindDir, 0)
				if err != nil {
					return err
				}
				if err := f.AppendDirent(parent, base(volPath), ino); err != nil {
					return err
				}
				mkdirs[volPath] = ino
				if err := walk(child); err != nil {
					return err
				}
				continue
			}
			data, err := os.ReadFile(filepath.Join(srcDir, child))
			if err != nil {
				return err
			}
			ino, err := f.AppendInode(0, kfs.KindFile, uint32(len(data)))
			if err != nil {
				return err
			}
			// DATA payloads are bounded; chunk so a large module cannot
			// exceed the replay limit and truncate the log.
			const chunk = 32 * 1024
			for off := 0; off < len(data) || (len(data) == 0 && off == 0); off += chunk {
				end := off + chunk
				if end > len(data) {
					end = len(data)
				}
				if err := f.AppendData(ino, uint64(off), data[off:end]); err != nil {
					return err
				}
			}
			if err := f.AppendDirent(parent, base(volPath), ino); err != nil {
				return err
			}
		}
		return nil
	}

	if _, _, err := parentOf(prefix); err != nil {
		return err
	}

	if err := walk(""); err != nil {
		return err
	}
	return f.AppendCheckpoint()
}

// pathUpTo returns "/a/b" for i==2 and the prefix before parts[i].

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func pathJoin(prefix, name string) string {
	prefix = "/" + strings.Trim(prefix, "/")
	if prefix == "/" {
		return "/" + name
	}
	return prefix + "/" + name
}
