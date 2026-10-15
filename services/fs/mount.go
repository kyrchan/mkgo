package main

import (
	"kernel.lane/kfs"
)

// mountAuto locates and mounts the KFS1 data volume.
//
// An image may carry BOTH a FAT16 boot volume at sector 0 -- which the loader
// needs for BOOTX64.EFI and /boot on the ESP -- and a KFS1 data volume after
// it. kfs.FindVolume derives the boundary with kfs.SplitSectors, the same rule
// tools/img uses to place it, so the builder and the guest cannot disagree
// about where the volume starts. (That disagreement would be silent: the guest
// would probe a sector nobody wrote and serve nothing.)
//
// This lives outside main.go deliberately: main.go is //go:build wasip1, so
// anything in it is untestable from the host suite. The mount decision is
// exactly the kind of thing that must be testable.
func mountAuto(dev BlockDev) (*KFS, error) {
	vol, err := kfs.FindVolume(dev)
	if err != nil {
		return nil, err
	}
	return MountKFS(vol)
}
