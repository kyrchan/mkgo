package main

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Golden files lock the converter outputs so accidental format drift breaks
// the build instead of Phase 12's hypervisor matrix. Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./...
//
// Inputs are fully deterministic: the VMDK CID is an FNV-1a hash of the raw
// content and the VDI UUIDs are MD5 digests, so identical inputs always
// produce byte-identical outputs.

const goldenRawSize = 4 << 20 // 4 MiB

// goldenRaw builds a pseudo-random-but-fixed payload (no crypto needed).
func goldenRaw() []byte {
	raw := make([]byte, goldenRawSize)
	x := uint32(0x12345678)
	for i := range raw {
		x = x*1664525 + 1013904223 // LCG: deterministic across runs
		raw[i] = byte(x >> 24)
	}
	return raw
}

func TestGoldenVMDKDescriptor(t *testing.T) {
	raw := goldenRaw()
	desc := vmdkDescriptor(int64(len(raw)), fnv32(raw), "disk-flat.vmdk")
	goldenPath := filepath.Join("testdata", "vmdk_descriptor.golden")

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, []byte(desc), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden missing (run UPDATE_GOLDEN=1 go test): %v", err)
	}
	if desc != string(want) {
		t.Fatalf("VMDK descriptor drifted from golden:\n--- got ---\n%s\n--- want ---\n%s",
			desc, want)
	}
}

func TestGoldenVDIHeader(t *testing.T) {
	raw := goldenRaw()
	uuid := md5.Sum(raw) // mirrors WriteVDI's deterministic UUID derivation
	hdr := vdiHeader(int64(len(raw)), uuid)

	goldenPath := filepath.Join("testdata", "vdi_header.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, hdr, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden missing (run UPDATE_GOLDEN=1 go test): %v", err)
	}
	if !bytes.Equal(hdr, want) {
		// Report the first differing field for actionable failures.
		for off := 0; off < len(hdr) && off < len(want); off++ {
			if hdr[off] != want[off] {
				t.Fatalf("VDI header differs from golden at offset %#x: got %#x want %#x",
					off, hdr[off], want[off])
			}
		}
		t.Fatal("VDI header length differs from golden")
	}
	// Structural re-checks independent of the golden bytes.
	if binary.LittleEndian.Uint32(hdr[0x040:]) != vdiSignature ||
		binary.LittleEndian.Uint32(hdr[0x04C:]) != vdiTypeFixed ||
		binary.LittleEndian.Uint32(hdr[0x158:]) != vdiDataOffset {
		t.Fatal("header structural fields wrong")
	}
}

// fnv32 mirrors vmdk.go's content-derived CID (kept in sync intentionally).
func fnv32(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// The full end-to-end conversion through the public API must also match the
// goldens (descriptor file text + header region of the .vdi).
func TestGoldenEndToEndConversions(t *testing.T) {
	dir := t.TempDir()
	raw := goldenRaw()
	rawPath := filepath.Join(dir, "disk.img")
	vmdkPath := filepath.Join(dir, "disk.vmdk")
	vdiPath := filepath.Join(dir, "disk.vdi")
	if err := os.WriteFile(rawPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteVMDK(rawPath, vmdkPath); err != nil {
		t.Fatal(err)
	}
	if err := WriteVDI(rawPath, vdiPath); err != nil {
		t.Fatal(err)
	}
	desc, _ := os.ReadFile(vmdkPath)
	goldenDesc, err := os.ReadFile(filepath.Join("testdata", "vmdk_descriptor.golden"))
	if err == nil && string(desc) != string(goldenDesc) {
		t.Fatal("end-to-end VMDK descriptor differs from golden")
	}
	out, _ := os.ReadFile(vdiPath)
	goldenHdr, err := os.ReadFile(filepath.Join("testdata", "vdi_header.golden"))
	if err == nil && !bytes.Equal(out[:len(goldenHdr)], goldenHdr) {
		t.Fatal("end-to-end VDI header differs from golden")
	}
	if int64(len(out)) != vdiDataOffset+int64(len(raw)) {
		t.Fatalf("vdi size %d", len(out))
	}
	if !bytes.Equal(out[vdiDataOffset:], raw) {
		t.Fatal("payload not verbatim")
	}
}
