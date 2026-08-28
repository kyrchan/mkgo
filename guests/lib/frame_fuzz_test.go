package kern

import (
	"testing"
)

// FuzzParseCanonicalHeader fuzzes the ratified §1 header parser and the
// LStr payload decoder (AGENTS.md practice #4): arbitrary datagram bytes
// must parse without panic; successful decodes must respect bounds.
func FuzzParseCanonicalHeader(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, CanonicalHeaderLen))
	good := make([]byte, CanonicalHeaderLen+4)
	Put16(good, 1)
	Put16(good[2:], 2)
	Put32(good[4:], 0)
	copy(good[8:14], "login\x00")
	f.Add(good)
	f.Add(append(good, 3, 0, 'a', 'b', 'c'))

	f.Fuzz(func(t *testing.T, data []byte) {
		hdr, ok := ParseHeader(data)
		if !ok {
			return
		}
		if len(hdr.RNam) > MaxName+16 { // cstr16 caps at field width
			t.Fatalf("rname overflow: %q", hdr.RNam)
		}
		if s, next, ok2 := LStr(data, 0); ok2 {
			if next < 2 || next > len(data) || len(s) != next-2 {
				t.Fatalf("LStr bounds: next=%d len=%d data=%d", next, len(s), len(data))
			}
		}
	})
}
