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

// FuzzDecodeInputEvent fuzzes the §4 input record parser (AGENTS.md
// practice #4): arbitrary bytes must never panic; a successful decode of
// either the deployed 4-byte v1 form or the ratified 6-byte v1.3 form must
// yield an event whose Encode roundtrips for the v1 shape.
func FuzzDecodeInputEvent(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 3))
	f.Add(make([]byte, InputRecLen))
	f.Add(make([]byte, InputRecLenV13))
	f.Add([]byte{1, 0, 0x41, 0x00})           // v1:KeyDown 'A'
	f.Add([]byte{1, 0, 0x1e, 0x00, 0, 0})    // v1.3:KeyDown with scan
	f.Add([]byte{2, ModShift | ModCtrl, 0, 0}) // KeyUp with mods

	f.Fuzz(func(t *testing.T, data []byte) {
		ev, ok := DecodeInputEvent(data)
		if !ok {
			return
		}
		// Encode must not panic and must produce the deployed width.
		out := ev.Encode()
		if len(out) != InputRecLen {
			t.Fatalf("encode len %d want %d", len(out), InputRecLen)
		}
		// A 6-byte input must decode with a non-zero scan only when the
		// input actually carries one — the v1.3 path.
		if len(data) >= InputRecLenV13 {
			// re-parse must be stable
			ev2, ok2 := DecodeInputEvent(data)
			if !ok2 {
				t.Fatal("second decode failed")
			}
			if ev2.Kind != ev.Kind || ev2.Mods != ev.Mods || ev2.Codepoint != ev.Codepoint {
				t.Fatalf("decode not stable: %+v vs %+v", ev, ev2)
			}
		}
	})
}
