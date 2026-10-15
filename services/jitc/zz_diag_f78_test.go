package jitc

import (
	"os"
	"testing"
)

func TestDiagF78(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nfuncs=%d", len(m.Code))
	a, err := CompileOne(m, 78)
	if err != nil {
		t.Fatalf("CompileOne: %v", err)
	}
	t.Logf("code len=%d", len(a.Code))
	if len(a.Code) == 0 {
		t.Fatal("empty code")
	}
	t.Logf("first 8 bytes: % x", a.Code[:8])
}
