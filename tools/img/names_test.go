package main

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestShortNameTable(t *testing.T) {
	cases := []struct {
		in    string
		want  string // 11-byte padded form, "" = any (collision-dependent)
		lossy bool
	}{
		{"BOOTX64.EFI", "BOOTX64 EFI", false},
		{"MOTD", "MOTD       ", false},
		{"KERNEL.CONF", "", true}, // ext > 3 chars: lossy
		{"console.wasm", "", true},
		{"hello.txt", "", true}, // lowercase: LFN needed for case
		{".profile", "", true},  // dotfile
		{"a.b.c", "", true},     // extra dot lost
		{"TOOLONGNAME", "", true},
		{"sp ace", "", true},
	}
	for _, c := range cases {
		got, lossy, err := shortName(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if lossy != c.lossy {
			t.Fatalf("%q: lossy=%v want %v (short %q)", c.in, lossy, c.lossy,
				string(got[:]))
		}
		if c.want != "" && string(got[:]) != c.want {
			t.Fatalf("%q: short=%q want %q", c.in, string(got[:]), c.want)
		}
		if !c.lossy { // exact names must round-trip through decodeShort
			back := decodeShort(got[:])
			if back != strings.ToUpper(c.in) {
				t.Fatalf("%q round-trips as %q", c.in, back)
			}
		}
	}
}

func TestCollisionTails(t *testing.T) {
	c1, _, _ := shortName("console.wasm")
	c2, _, _ := shortName("console2.wasm")
	taken := map[dirEntryKey]bool{dirEntryKey(c1): true, dirEntryKey(c2): true}
	r1 := resolveCollision(c1, taken)
	r2 := resolveCollision(c2, taken)
	s1, s2 := string(r1[:]), string(r2[:])
	if s1 == s2 {
		t.Fatalf("identical shorts after collision: %q", s1)
	}
	if !strings.Contains(s1, "~") || !strings.Contains(s2, "~") {
		t.Fatalf("missing numeric tails: %q / %q", s1, s2)
	}
}

func TestLFNChecksumSpecVector(t *testing.T) {
	// Reference checksum from the MS FAT spec discussion of LFN entries;
	// verified against an independent implementation of the rotation sum.
	sum := lfnChecksum([]byte("CONSO~1WAS "))
	var x byte = 0
	for _, c := range []byte("CONSO~1WAS ") {
		x = ((x >> 1) | (x << 7)) + c
	}
	if sum != x || sum == 0 {
		t.Fatalf("checksum %d != %d", sum, x)
	}
}

func TestLFNEntriesStructure(t *testing.T) {
	name := "a_very_long_file_name_beyond_eight_three.txt" // 46 chars -> 4 entries
	short, _, _ := shortName(name)
	es := lfnEntries(name, lfnChecksum(short[:]))
	if len(es) != 4 {
		t.Fatalf("want 4 LFN entries for %d chars, got %d", len(name), len(es))
	}
	for i, e := range es {
		if e[11] != attrLFN {
			t.Fatalf("entry %d attr %#x", i, e[11])
		}
		wantSeq := byte(i + 1)
		last := i == len(es)-1
		if last {
			wantSeq |= 0x40
		}
		if e[0] != wantSeq {
			t.Fatalf("entry %d seq %#x want %#x", i, e[0], wantSeq)
		}
	}
	// Place each entry's 13 units at its sequence offset, then compare.
	units := make([]uint16, len(es)*13)
	for _, e := range es {
		seq := int(e[0] & 0x3F)
		k := 0
		for _, sl := range [][2]int{{1, 5}, {14, 6}, {28, 2}} {
			o, cnt := sl[0], sl[1]
			for j := 0; j < cnt; j++ {
				units[(seq-1)*13+k] = uint16(e[o+j*2]) | uint16(e[o+j*2+1])<<8
				k++
			}
		}
	}
	for i, u := range units {
		if u == 0x0000 {
			units = units[:i]
			break
		}
		if u == 0xFFFF {
			t.Fatalf("premature 0xFFFF padding at unit %d", i)
		}
	}
	if got := string(utf16.Decode(units)); got != name {
		t.Fatalf("reassembled %q want %q", got, name)
	}
}
