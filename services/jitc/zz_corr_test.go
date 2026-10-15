package jitc

import (
	"os"
	"testing"
)

func TestF78Correlate(t *testing.T) {
	raw, err := os.ReadFile("../fs/fs.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	fb := m.Code[55]
	for i := 0; i < 12 && i < len(fb.Ops); i++ {
		o := fb.Ops[i]
		t.Logf("OP %2d code=%02x local=%d table=%v", i, o.Code, o.Local, o.Table)
	}
	a, err := CompileOne(m, 78)
	if err != nil {
		t.Fatal(err)
	}
	c := a.Code
	for i := 0x30; i < 0x80 && i < len(c); i += 16 {
		end := i + 16
		if end > len(c) {
			end = len(c)
		}
		t.Logf("B %4d: % x", i, c[i:end])
	}
	img := &Image{Funcs: map[uint32]int{78: 0}, Region: c, Polls: a.Polls}
	r, err := NewRunner(m, img, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, args := range [][]uint64{{0}, {1}, {2}} {
		res, err := r.CallFunc(m, 78, args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("NATIVE args=%v value=%x trap=%d", args, res.Value, res.Trap)
	}
}
