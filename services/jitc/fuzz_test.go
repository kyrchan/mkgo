package jitc

import "testing"

// FuzzDecode asserts the decoder's hard contract: no panic on any
// input, and every successfully decoded module satisfies structural
// invariants (func/code count agreement, terminated bodies).
// Soak target per practice #4: 30 s per QA sweep.
func FuzzDecode(f *testing.F) {
	f.Add(minimalModule)
	f.Add(arithModule)
	f.Add([]byte("not wasm at all"))
	f.Add([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00})
	f.Add([]byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0x0A})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Decode(b)
		if err != nil {
			return
		}
		if len(m.Funcs) != len(m.Code) && len(m.Code) != 0 {
			t.Fatalf("func/code mismatch %d/%d", len(m.Funcs), len(m.Code))
		}
		for i, fb := range m.Code {
			if len(fb.Ops) == 0 || fb.Ops[len(fb.Ops)-1].Code != 0x0B {
				t.Fatalf("func %d body not terminated", i)
			}
		}
	})
}
