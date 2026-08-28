package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateVMX writes a minimal VMware .vmx descriptor for EFI boot of a
// monolithicFlat VMDK with serial output to a file. Field set mirrors the
// Phase-12 recipe in AGENTS.md (firmware=efi, serial0 to file).
func GenerateVMX(name, vmdkPath, serialLogPath string, memMB int) string {
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	lines := []string{
		`.encoding = "UTF-8"`,
		fmt.Sprintf("config.version = \"8\""),
		fmt.Sprintf("virtualHW.version = \"16\""),
		fmt.Sprintf("displayName = \"%s\"", name),
		fmt.Sprintf("guestOS = \"other-64\""),
		fmt.Sprintf("firmware = \"efi\""),
		fmt.Sprintf("memsize = \"%d\"", memMB),
		fmt.Sprintf("numvcpus = \"1\""),
		`cpuid.coresPerSocket = "1"`,
		// Disk: IDE so firmware needs no driver.
		`ide0:0.present = "TRUE"`,
		fmt.Sprintf("ide0:0.fileName = \"%s\"", abs(vmdkPath)),
		`ide0:0.deviceType = "disk"`,
		// Serial to file: the gate source.
		`serial0.present = "TRUE"`,
		`serial0.fileType = "file"`,
		fmt.Sprintf("serial0.fileName = \"%s\"", abs(serialLogPath)),
		`serial0.tryNoRxLoss = "FALSE"`,
		// No network/display needed for headless gates.
		`ethernet0.present = "FALSE"`,
		`svga.autodetect = "FALSE"`,
		`mks.enable3d = "FALSE"`,
		`tools.syncTime = "FALSE"`,
		`usb.present = "FALSE"`,
		`sound.present = "FALSE"`,
		`floppy0.present = "FALSE"`,
		`cdrom0.present = "FALSE"`,
	}
	return strings.Join(lines, "\n") + "\n"
}

// WriteVMDKForVMware produces the monolithicFlat pair VMware needs from a
// raw image: <out>.vmdk descriptor + <stem>-flat.vmdk byte-exact copy.
// Mirrors tools/img/vmdk.go (kept standalone so hvtest has no cross-module
// dependency); format per VMware Virtual Disk Format 1.1.
func WriteVMDKForVMware(rawPath, outPath string) error {
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		return err
	}
	const sect = 512
	if len(raw)%sect != 0 {
		return fmt.Errorf("%s: size %d not sector aligned", rawPath, len(raw))
	}
	cid := fnv1a(raw)
	flatName := strings.TrimSuffix(filepath.Base(outPath), ".vmdk") + "-flat.vmdk"
	cyl := int64(len(raw)) / (sect * 16 * 63)
	if cyl < 1 {
		cyl = 1
	}
	if cyl > 16383 {
		cyl = 16383
	}
	desc := fmt.Sprintf(`# Disk DescriptorFile
version=1
CID=%08x
parentCID=ffffffff
createType="monolithicFlat"

# Extent description
RW %d FLAT "%s" 0

# The Disk Data Base
#DDB

ddb.virtualHWVersion = "4"
ddb.geometry.cylinders = "%d"
ddb.geometry.heads = "16"
ddb.geometry.sectors = "63"
ddb.adapterType = "ide"
`, cid, len(raw)/sect, flatName, cyl)
	if err := os.WriteFile(outPath, []byte(desc), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(filepath.Dir(outPath), flatName), raw, 0o644)
}

func fnv1a(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}
