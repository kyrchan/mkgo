package jitc

import (
	"os"
	"testing"
)

func TestNativeF78(t *testing.T) {
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
	img := &Image{Funcs: map[uint32]int{78: 0}, Region: a.Code, Polls: a.Polls}
	r, err := NewRunner(m, img, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, args := range [][]uint64{{0}, {1}, {2}, {0xFFFFFFFF}} {
		res, err := r.CallFunc(m, 78, args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("args=%v value=%x trap=%d", args, res.Value, res.Trap)
	}
}
