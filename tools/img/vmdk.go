package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
)

// VMware monolithicFlat writer for the Phase-12 hypervisor matrix.
// A flat VMDK = small text descriptor (.vmdk) referencing a byte-exact
// copy of the raw image in a sibling "-flat.vmdk" file. Layout follows the
// documented hosted-disk descriptor format (see VMware Virtual Disk Format
// 1.1 spec, "monolithicFlat"); geometry uses the same 16/63 convention as
// the FAT16 builder so BIOS/EFI geometry assumptions stay consistent.

const (
	vmdkHeads         = 16
	vmdkSectorsPerTrk = 63
	vmdkMaxCylinders  = 16383
	vmdkSectorSize    = 512
)

// vmdkDescriptor renders the descriptor text for a raw disk of rawSize bytes.
func vmdkDescriptor(rawSize int64, cid uint32, flatName string) string {
	cyl := rawSize / (vmdkSectorSize * vmdkHeads * vmdkSectorsPerTrk)
	if cyl < 1 {
		cyl = 1
	}
	if cyl > vmdkMaxCylinders {
		cyl = vmdkMaxCylinders
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Disk DescriptorFile\n")
	fmt.Fprintf(&b, "version=1\n")
	fmt.Fprintf(&b, "CID=%08x\n", cid)
	fmt.Fprintf(&b, "parentCID=ffffffff\n")
	fmt.Fprintf(&b, "createType=\"monolithicFlat\"\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "# Extent description\n")
	fmt.Fprintf(&b, "RW %d FLAT \"%s\" 0\n", rawSize/vmdkSectorSize, flatName)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "# The Disk Data Base\n")
	fmt.Fprintf(&b, "#DDB\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "ddb.virtualHWVersion = \"4\"\n")
	fmt.Fprintf(&b, "ddb.geometry.cylinders = \"%d\"\n", cyl)
	fmt.Fprintf(&b, "ddb.geometry.heads = \"%d\"\n", vmdkHeads)
	fmt.Fprintf(&b, "ddb.geometry.sectors = \"%d\"\n", vmdkSectorsPerTrk)
	fmt.Fprintf(&b, "ddb.adapterType = \"ide\"\n")
	return b.String()
}

// WriteVMDK converts a raw image to a monolithicFlat pair:
// descPath holds the descriptor; data goes to <stem>-flat.vmdk beside it.
func WriteVMDK(rawPath, descPath string) error {
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		return err
	}
	if len(raw)%vmdkSectorSize != 0 {
		return fmt.Errorf("img: %s size %d not sector aligned", rawPath, len(raw))
	}
	h := fnv.New32a()
	h.Write(raw)
	flat := strings.TrimSuffix(filepath.Base(descPath), ".vmdk") + "-flat.vmdk"
	desc := vmdkDescriptor(int64(len(raw)), h.Sum32(), flat)
	if err := os.WriteFile(descPath, []byte(desc), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(descPath), flat), raw, 0o644)
}
