package main

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// 8.3 short-name generation and LFN (long file name) directory entries,
// following the Microsoft EFI FAT file system specification. References
// kept inline because the checksum / sequencing rules are easy to get
// subtly wrong.

const (
	attrLFN = 0x0F // RO|HID|SYS|VOL combination marking a long-name entry
)

type dirEntryKey [11]byte

func valid83(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'()-@^_`{}~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			c -= 'a' - 'A'
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte("!#$%&'()-@^_`{}~", c) >= 0:
		default:
			c = '_'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// shortName derives the on-disk 8.3 short name for name: the 11 padded
// bytes, whether information was lost (requiring a "~n" tail plus an LFN
// slot), or an error for unusable names.
func shortName(name string) ([11]byte, bool, error) {
	var out [11]byte
	if name == "" || len(name) > 255 {
		return out, false, fmt.Errorf("img: bad name %q", name)
	}
	base, ext, hasExt := splitLastDot(name)
	base = strings.TrimSuffix(base, ".") // trailing dot is not storable

	exact := false
	if !hasExt {
		exact = base == strings.ToUpper(base) && valid83(base) && len(base) <= 8
	} else if base != "" {
		uB, uE := strings.ToUpper(base), strings.ToUpper(ext)
		exact = uB == base && uE == ext &&
			valid83(uB) && valid83(uE) && len(uB) <= 8 && len(uE) <= 3
	}
	if exact {
		copy(out[0:], pad(base, 8))
		copy(out[8:], pad(ext, 3))
		return out, false, nil
	}

	sb := sanitize(strings.TrimPrefix(strings.TrimSuffix(base, "."), "."))
	se := ""
	if hasExt {
		se = sanitize(ext)
	}
	lossy := sb != base || se != ext ||
		len(sb) > 8 || len(se) > 3 ||
		base == "" // dotfile: short form can never round-trip

	maxBase := 8
	if lossy {
		maxBase = 6 // reserve room for "~1"
	}
	if len(sb) > maxBase {
		sb = sb[:maxBase]
	}
	if len(se) > 3 {
		se = se[:3]
	}
	copy(out[0:], pad(sb, 8))
	copy(out[8:], pad(se, 3))
	return out, lossy, nil
}

func splitLastDot(name string) (base, ext string, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return name, "", false
	}
	return name[:i], name[i+1:], true
}

func pad(s string, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	copy(b, s)
	return b
}

// resolveCollision rewrites cand with a unique ~N tail against taken.
func resolveCollision(cand [11]byte, taken map[dirEntryKey]bool) [11]byte {
	ext := strings.TrimRight(string(cand[8:]), " ")
	base := strings.TrimRight(string(cand[:8]), " ")
	for n := 1; n <= 999999; n++ {
		tail := fmt.Sprintf("~%d", n)
		b := base
		if len(b)+len(tail) > 8 {
			b = b[:8-len(tail)]
		}
		var key dirEntryKey
		copy(key[0:], pad(b+tail, 8))
		copy(key[8:], pad(ext, 3))
		if !taken[key] {
			taken[key] = true
			return key
		}
	}
	panic("img: >999999 short-name collisions in one directory")
}

// lfnChecksum computes the LFN name-checksum over the 11 short-name bytes.
func lfnChecksum(short []byte) byte {
	var sum byte
	for _, c := range short {
		sum = ((sum >> 1) | (sum << 7)) + c
	}
	return sum
}

// lfnEntries builds the long-file-name entries for name in logical order
// seq=1..N; the caller must write them in REVERSE order directly before the
// short entry. Each entry carries up to 13 UTF-16 code units, NUL-terminated
// then 0xFFFF-padded.
func lfnEntries(name string, checksum byte) [][]byte {
	units := utf16.Encode([]rune(name))
	nEnt := (len(units) + 13 - 1) / 13
	if nEnt < 1 {
		nEnt = 1
	}
	out := make([][]byte, 0, nEnt)
	for seq := 1; seq <= nEnt; seq++ {
		e := make([]byte, 32)
		first := byte(seq)
		if seq == nEnt {
			first |= 0x40 // end-of-name flag on the last physical entry
		}
		e[0] = first
		e[11] = attrLFN
		e[12] = 0
		e[13] = checksum
		fillLFNUnits(e, units[(seq-1)*13:], seq == nEnt)
		out = append(out, e)
	}
	return out
}

func fillLFNUnits(e []byte, rem []uint16, last bool) {
	slots := [][2]int{{1, 5}, {14, 6}, {28, 2}} // (byte offset, u16 count)
	pos := 0
	terminated := false
	for _, sl := range slots {
		off, cnt := sl[0], sl[1]
		for j := 0; j < cnt; j++ {
			var u uint16 = 0xFFFF
			switch {
			case pos < len(rem):
				u = rem[pos]
				pos++
			case last && !terminated:
				u = 0x0000 // single NUL terminator
				terminated = true
			}
			e[off+j*2] = byte(u)
			e[off+j*2+1] = byte(u >> 8)
		}
	}
}
