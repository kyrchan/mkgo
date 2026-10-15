package jitc

import (
	"os"
	"testing"
)

func TestWriteF78Bin(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := CompileOne(m, 78)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/f78.bin", a.Code, 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes", len(a.Code))
}
