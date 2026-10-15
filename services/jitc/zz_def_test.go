package jitc

import (
	"os"
	"testing"
)

func TestF78Definitive(t *testing.T) {
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
	t.Logf("len=%d", len(c))
	t.Logf("bytes[0x5e:0x72] = % x", c[0x5e:0x72])
	t.Logf("bytes[0x6c:0x72] = % x", c[0x6c:0x72])
	t.Logf("bytes[0x52f:0x538] = % x", c[0x52f:0x538])
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
		if args[0] != 0 && res.Trap == 0 {
			t.Errorf("expected trap for args %v, got value=%x", args, res.Value)
		}
	}
}
