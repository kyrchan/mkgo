package jitc

import (
	"os"
	"testing"
)

func TestTraceF78(t *testing.T) {
	os.Setenv("JITC_TRACE", "1")
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := NewInterp(m, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]uint64{{0}, {1}, {2}} {
		t.Logf("=== args=%v ===", args)
		res, trap, err := ip.execFunc(78, args)
		t.Logf("res=%v trap=%d err=%v", res, trap, err)
	}
}
