package jitc

import (
	"os"
	"testing"
)

func TestBlockMapF78(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	fb := m.Code[55]
	for i, o := range fb.Ops {
		t.Logf("%3d code=%02x sub=%d label=%d", i, o.Code, o.Sub, o.Label)
	}
	elsePC, endPC, err := blockMap(fb.Ops)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("elsePC=%v", elsePC)
	t.Logf("endPC=%v", endPC)
	for i, o := range fb.Ops {
		if o.Code == 0x0E {
			t.Logf("br_table at %d: table=%v", i, o.Table)
			break
		}
	}
}
