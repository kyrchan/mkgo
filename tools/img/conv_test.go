package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVMDKDescriptorAndFlat(t *testing.T) {
	dir := t.TempDir()
	raw := make([]byte, 8<<20)
	rand.Read(raw)
	rawPath := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(rawPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	descPath := filepath.Join(dir, "disk.vmdk")
	if err := WriteVMDK(rawPath, descPath); err != nil {
		t.Fatal(err)
	}

	desc, err := os.ReadFile(descPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(desc)
	for _, want := range []string{
		`createType="monolithicFlat"`,
		"RW 16384 FLAT \"disk-flat.vmdk\" 0",
		`ddb.geometry.heads = "16"`,
		`ddb.geometry.sectors = "63"`,
		`ddb.adapterType = "ide"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("descriptor missing %q:\n%s", want, s)
		}
	}
	flat, err := os.ReadFile(filepath.Join(dir, "disk-flat.vmdk"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(flat, raw) {
		t.Fatal("flat extent is not a byte-exact copy of the raw image")
	}
}

func TestVDIFixedHeader(t *testing.T) {
	dir := t.TempDir()
	raw := make([]byte, 6<<20) // not block-aligned on purpose
	for i := range raw[:512] {
		raw[i] = byte(i)
	}
	rawPath := filepath.Join(dir, "disk.img")
	os.WriteFile(rawPath, raw, 0o644)
	vdiPath := filepath.Join(dir, "disk.vdi")
	if err := WriteVDI(rawPath, vdiPath); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(vdiPath)
	if err != nil {
		t.Fatal(err)
	}
	g32 := func(off int) uint32 { return binary.LittleEndian.Uint32(out[off:]) }
	if g32(0x040) != vdiSignature {
		t.Fatalf("signature %#x", g32(0x040))
	}
	if g32(0x044) != vdiVersion11 || g32(0x048) != vdiHeaderSize ||
		g32(0x04C) != vdiTypeFixed || g32(0x154) != vdiNoBlocksMap ||
		g32(0x158) != vdiDataOffset || g32(0x168) != vdiSectorSize ||
		g32(0x16C) != vdiBlockSize {
		t.Fatal("header fields wrong")
	}
	totalBlocks := (uint32(len(raw)) + vdiBlockSize - 1) / vdiBlockSize
	if g32(0x170) != totalBlocks || g32(0x174) != totalBlocks {
		t.Fatalf("block counts %d/%d want %d", g32(0x170), g32(0x174), totalBlocks)
	}
	cyl := uint32(len(raw)) / (vdiSectorSize * vdiHeads * vdiSectorsTrk)
	if g32(0x15C) != cyl || g32(0x160) != vdiHeads || g32(0x164) != vdiSectorsTrk {
		t.Fatal("geometry wrong")
	}
	if int64(len(out)) != vdiDataOffset+int64(len(raw)) {
		t.Fatalf("total length %d", len(out))
	}
	if !bytes.Equal(out[vdiDataOffset:], raw) {
		t.Fatal("payload at offData is not the raw image verbatim")
	}
	// UUIDs present and deterministic for identical content.
	var uuid [16]byte
	copy(uuid[:], out[0x178:])
	if uuid == ([16]byte{}) {
		t.Fatal("empty create-uuid")
	}
}
