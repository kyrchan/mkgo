package jitc

import (
	"os"
	"testing"
)

func TestDumpF78bytes(t *testing.T) {
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
	c := a.Code
	for i := 25; i < 115 && i < len(c); i += 16 {
		end := i + 16
		if end > len(c) {
			end = len(c)
		}
		t.Logf("%4d: % x", i, c[i:end])
	}
}
