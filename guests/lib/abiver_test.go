package kern

import "testing"

// stampModule builds the smallest module add_abiver.py could have written:
// magic, version, one custom section carrying abi_ver.
func stampModule(ver byte) []byte {
	name := []byte("abi_ver")
	body := append(lebBytes(uint64(len(name))), name...)
	body = append(body, ver)
	out := []byte("\x00asm\x01\x00\x00\x00")
	out = append(out, 0x00) // custom section id
	out = append(out, lebBytes(uint64(len(body)))...)
	return append(out, body...)
}

func lebBytes(n uint64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7F)
		n >>= 7
		if n != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func TestReadAbiVerRoundTrip(t *testing.T) {
	for _, v := range []byte{0, 1, 2, 127} {
		got, ok, err := ReadAbiVer(stampModule(v))
		if err != nil || !ok || got != int(v) {
			t.Fatalf("ver %d: got %d ok=%v err=%v", v, got, ok, err)
		}
	}
}

func TestReadAbiVerMissing(t *testing.T) {
	// A valid module with no abi_ver section: ok=false, NOT an error. That
	// distinction matters -- "this module is old" and "this file is not a
	// module" are different answers.
	m := []byte("\x00asm\x01\x00\x00\x00")
	v, ok, err := ReadAbiVer(m)
	if err != nil || ok || v != 0 {
		t.Fatalf("got %d ok=%v err=%v, want ok=false and no error", v, ok, err)
	}
}

func TestReadAbiVerIgnoresOtherCustomSections(t *testing.T) {
	name := []byte("producers")
	body := append(lebBytes(uint64(len(name))), name...)
	body = append(body, 0xFF, 0xFF)
	m := []byte("\x00asm\x01\x00\x00\x00")
	m = append(m, 0x00)
	m = append(m, lebBytes(uint64(len(body)))...)
	m = append(m, body...)
	m = append(m, stampModule(2)[8:]...) // the real stamp after it
	got, ok, err := ReadAbiVer(m)
	if err != nil || !ok || got != 2 {
		t.Fatalf("got %d ok=%v err=%v, want the abi_ver after another custom section", got, ok, err)
	}
}

func TestReadAbiVerRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"not wasm":             []byte("hello"),
		"truncated header":     []byte("\x00asm"),
		"section past end":     append([]byte("\x00asm\x01\x00\x00\x00"), 0x00, 0x7F),
		"overlong LEB":         append([]byte("\x00asm\x01\x00\x00\x00"), 0x00, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80),
		"custom name past end": append([]byte("\x00asm\x01\x00\x00\x00"), 0x00, 0x02, 0x7F, 'x'),
	}
	for name, in := range cases {
		if _, ok, err := ReadAbiVer(in); err == nil && ok {
			t.Fatalf("%s: accepted %q, want an error", name, in)
		}
	}
}

func TestAbiVerStampIsTwo(t *testing.T) {
	// The stamp is the number the kernel enforces; if this changes, the
	// kernel's own check and every shipped module change with it.
	if AbiVerStamp != 2 {
		t.Fatalf("AbiVerStamp=%d, want 2", AbiVerStamp)
	}
}
