//go:build !wasip1

package main

import (
	"testing"
)

// FuzzKFSReplay exercises the record-stream parser (AGENTS.md practice
// #4): arbitrary device images must never panic MountKFS or the
// traversal surface — mount either fails cleanly or yields a volume
// whose List/Stat/ReadFile paths are safe.
func FuzzKFSReplay(f *testing.F) {
	// seed: valid format+workload image (via RamDisk), torn prefix,
	// bit-flipped CRC region, and all-zeros.
	w := buildWorkload(&testing.T{})
	f.Add(append([]byte(nil), w.image...))
	torn := append([]byte(nil), w.image[:w.end-40]...)
	f.Add(torn)
	bad := append([]byte(nil), w.image...)
	if len(bad) > 600 {
		bad[600] ^= 0xFF
	}
	f.Add(bad)
	f.Add(make([]byte, 2048))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 512*2 {
			return // no room for superblock + log
		}
		k, err := MountKFS(&tornDev{data: data})
		if err != nil {
			return // rejected cleanly (bad sb / geometry)
		}
		ents, err := k.List("/")
		if err != nil {
			return
		}
		for _, e := range ents {
			p := "/" + e.Name
			st, err := k.Stat(p)
			if err != nil {
				continue
			}
			buf := make([]byte, 96)
			k.ReadFile(p, uint64(st.Size)+1, buf) // past-EOF is safe (0,nil)
			k.ReadFile(p, 0, buf)
			if st.IsDir() {
				subs, err := k.List(p)
				if err != nil {
					continue
				}
				for _, se := range subs {
					k.Stat(p + "/" + se.Name)
				}
			}
		}
	})
}
