package jitc

import (
	"os"
	"testing"
)

func TestCountEnds(t *testing.T) {
	raw, err := os.ReadFile("../net/net.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	nimport := 0
	for _, im := range m.Imports {
		if im.Kind == 0 {
			nimport++
		}
	}
	bi := 882 - nimport
	ends := 0
	if bi >= 0 && bi < len(m.Code) {
		fb := m.Code[bi]
		for _, op := range fb.Ops {
			if op.Code == 0x0B {
				ends++
			}
		}
	}
	t.Logf("Total ENDs in func 882: %d", ends)
}
