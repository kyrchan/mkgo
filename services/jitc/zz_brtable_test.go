package jitc

import (
	"testing"
)

func TestBrTableMinimal(t *testing.T) {
	// block (outer, label 1)
	//   block (inner, label 0)
	//     local.get 0
	//     br_table [0 1 1] 1   // 0->inner break, 1->outer break, default=1
	//     i32.const 99          // DEAD: br_table always branches
	//     end                   // inner break lands here
	//   end                     // outer break lands here
	// unreachable
	// end
	ops := []byte{
		0x02, 0x40, // block (outer, empty)
		0x02, 0x40, // block (inner, empty)
		0x20, 0x00, // local.get 0
		0x0E, 0x03, 0x00, 0x01, 0x01, 0x01, // br_table [0 1 1] default 1
		0x41, 0x63, // i32.const 99 (dead)
		0x0B, // end (inner)
		0x0B, // end (outer)
		0x00, // unreachable
		0x0B, // end (function)
	}
	raw := module(
		typesSec(ftype([]byte{0x7F}, nil)),
		funcsSec(0),
		exportSec("f", 0, 0),
		codeSec(body(nil, ops...)),
	)
	m, img := compile(t, raw)
	for _, args := range [][]uint64{{0}, {1}, {2}, {42}} {
		diffExport(t, m, img, "f", args...)
	}
}
