package jitc

import (
	"os"
	"testing"
)

func TestF78Debug(t *testing.T) {
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
	// find all e9/e8/0f xx jumps and trap sites, print target bytes
	for i := 0; i < len(c); i++ {
		b := c[i]
		if b == 0xE8 || b == 0xE9 {
			rel := int32(c[i+1]) | int32(c[i+2])<<8 | int32(c[i+3])<<16 | int32(c[i+4])<<24
			tgt := int32(i+5) + rel
			t.Logf("JUMP %5d -> %d", i, tgt)
			i += 4
		} else if b == 0x0F && (c[i+1] == 0x85 || c[i+1] == 0x82 || c[i+1] == 0x87) {
			rel := int32(c[i+2]) | int32(c[i+3])<<8 | int32(c[i+4])<<16 | int32(c[i+5])<<24
			tgt := int32(i+6) + rel
			t.Logf("JCC  %5d -> %d", i, tgt)
			i += 5
		} else if b == 0xC7 && c[i+1] == 0x02 {
			code := uint32(c[i+2]) | uint32(c[i+3])<<8 | uint32(c[i+4])<<16 | uint32(c[i+5])<<24
			t.Logf("TRAP %5d code=%d", i, code)
			i += 5
		}
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
