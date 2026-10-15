package jitc

import (
	"os"
	"testing"
)

func TestDisF78(t *testing.T) {
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
	for i := 0; i < len(c); i++ {
		b := c[i]
		switch {
		case b == 0xE8 || b == 0xE9:
			rel := int32(c[i+1]) | int32(c[i+2])<<8 | int32(c[i+3])<<16 | int32(c[i+4])<<24
			t.Logf("%5d: %02x -> %d", i, b, int32(i+5)+rel)
			i += 4
		case b == 0xEB:
			rel := int8(c[i+1])
			t.Logf("%5d: eb -> %d", i, int32(i+2)+int32(rel))
			i++
		case b == 0x0F && (c[i+1] == 0x85 || c[i+1] == 0x82 || c[i+1] == 0x87 || c[i+1] == 0x83 || c[i+1] == 0x84 || c[i+1] == 0x86 || c[i+1] == 0x8F || c[i+1] == 0x8D || c[i+1] == 0x8C):
			rel := int32(c[i+2]) | int32(c[i+3])<<8 | int32(c[i+4])<<16 | int32(c[i+5])<<24
			t.Logf("%5d: 0f %02x -> %d", i, c[i+1], int32(i+6)+rel)
			i += 5
		case b == 0xC7 && c[i+1] == 0x02:
			code := uint32(c[i+2]) | uint32(c[i+3])<<8 | uint32(c[i+4])<<16 | uint32(c[i+5])<<24
			t.Logf("%5d: trap code=%d", i, code)
			i += 5
		}
	}
}
