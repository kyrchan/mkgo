package main

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// Independent minimal FAT16 reader used ONLY by tests to verify what the
// builder wrote: parses the BPB back, follows FAT chains, reconstructs
// long names from LFN slots. Deliberately shares no state with Volume.

type rdEntry struct {
	name  string
	short [11]byte
	attr  byte
	start uint32
	size  uint32
}

type fatReader struct {
	raw      []byte
	spc      int
	reserved int
	nfats    int
	rootEnts int
	fatSecs  int
	totSecs  int

	fatOff   int64
	rootOff  int64
	dataOff  int64
	clusterN int
}

func newFatReader(raw []byte) (*fatReader, error) {
	if len(raw) < 512 || raw[510] != 0x55 || raw[511] != 0xAA {
		return nil, errBad("missing 55AA boot signature")
	}
	g16 := func(off int) int { return int(binary.LittleEndian.Uint16(raw[off:])) }
	fr := &fatReader{
		raw:      raw,
		spc:      int(raw[13]), // sectors per cluster is a single byte
		reserved: g16(14),
		nfats:    int(raw[16]),
		rootEnts: g16(17),
		fatSecs:  g16(22),
		totSecs:  g16(32) | g16(34)<<16,
	}
	if fr.spc == 0 || fr.nfats != 2 || fr.rootEnts%16 != 0 || fr.fatSecs == 0 {
		return nil, errBad("implausible BPB")
	}
	if strings.TrimSpace(string(raw[54:62])) != "FAT16" {
		return nil, errBad("fstype not FAT16")
	}
	fr.clusterN = (fr.totSecs - fr.reserved - fr.nfats*fr.fatSecs -
		fr.rootEnts*32/512) / fr.spc
	fr.fatOff = int64(fr.reserved) * 512
	fr.rootOff = fr.fatOff + int64(fr.nfats)*int64(fr.fatSecs)*512
	fr.dataOff = fr.rootOff + int64(fr.rootEnts)*32
	return fr, nil
}

type errBad string

func (e errBad) Error() string { return string(e) }

func (fr *fatReader) fat(n uint32) uint16 {
	return binary.LittleEndian.Uint16(fr.raw[int(fr.fatOff)+int(n)*2:])
}

func (fr *fatReader) chain(start uint32) []byte {
	cb := fr.spc * 512
	var out []byte
	for cl := start; cl >= 2 && cl < 0xFFF8 && len(out)/cb < fr.clusterN+1; cl = uint32(fr.fat(cl)) {
		out = append(out, fr.raw[int(fr.dataOff)+int(cl-2)*cb:int(fr.dataOff)+int(cl-1)*cb]...)
	}
	return out
}

// listDir reads a directory (start==0 means the fixed root region).
func (fr *fatReader) listDir(start uint32) ([]rdEntry, error) {
	var region []byte
	if start == 0 {
		region = fr.raw[int(fr.rootOff) : int(fr.rootOff)+fr.rootEnts*32]
	} else {
		region = fr.chain(start)
	}
	var out []rdEntry
	lfn := map[byte][]uint16{}
	var lfnSum byte // checksum carried by the LFN slots themselves
	for off := 0; off+32 <= len(region); off += 32 {
		e := region[off : off+32]
		if e[0] == 0x00 {
			break // end-of-directory marker
		}
		if e[0] == 0xE5 {
			continue // deleted
		}
		if e[11] == attrLFN && e[12] == 0 {
			seq := e[0] & 0x3F
			units := make([]uint16, 13)
			k := 0
			for _, sl := range [][2]int{{1, 5}, {14, 6}, {28, 2}} {
				o, cnt := sl[0], sl[1]
				for j := 0; j < cnt; j++ {
					units[k] = binary.LittleEndian.Uint16(e[o+j*2:])
					k++
				}
			}
			lfn[seq] = units
			lfnSum = e[13]
			continue
		}
		short := decodeShort(e[:11])
		name := short
		if len(lfn) > 0 {
			maxSeq := byte(0)
			for s := range lfn {
				if s > maxSeq {
					maxSeq = s
				}
			}
			var all []uint16
			okSum := true
			for s := byte(1); s <= maxSeq; s++ {
				u, ok := lfn[s]
				if !ok {
					okSum = false
					break
				}
				all = append(all, u...)
			}
			lfn = map[byte][]uint16{} // consumed either way
			if okSum && lfnChecksum(e[:11]) == lfnSum {
				for i, u := range all {
					if u == 0x0000 {
						all = all[:i]
						break
					}
					if u == 0xFFFF {
						break
					}
				}
				name = string(utf16.Decode(all))
			}
		}
		start := uint32(binary.LittleEndian.Uint16(e[26:]))
		size := binary.LittleEndian.Uint32(e[28:])
		var sh [11]byte
		copy(sh[:], e[:11])
		out = append(out, rdEntry{name: name, short: sh, attr: e[11],
			start: start, size: size})
	}
	return out, nil
}

func decodeShort(b []byte) string {
	base := strings.TrimRight(string(b[:8]), " ")
	ext := strings.TrimRight(string(b[8:]), " ")
	if ext != "" {
		return base + "." + ext
	}
	return base
}

// find looks up path ("/vm/app") from the root.
func (fr *fatReader) find(path string) (rdEntry, bool, error) {
	cur := rdEntry{name: "/", attr: attrDir, start: 0}
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if cur.attr&attrDir == 0 {
			return rdEntry{}, false, errBad("not a directory: " + cur.name)
		}
		kids, err := fr.listDir(cur.start)
		if err != nil {
			return rdEntry{}, false, err
		}
		found := false
		for _, k := range kids {
			if k.name == seg {
				cur = k
				found = true
				break
			}
		}
		if !found {
			return rdEntry{}, false, nil
		}
	}
	return cur, true, nil
}

// readFile returns file content by following its cluster chain, truncated
// to the directory-entry size.
func (fr *fatReader) readFile(path string) ([]byte, error) {
	e, ok, err := fr.find(path)
	if err != nil || !ok {
		return nil, err
	}
	if e.attr&attrDir != 0 {
		return nil, errBad("is a directory: " + path)
	}
	data := fr.chain(e.start)
	if uint32(len(data)) < e.size {
		return nil, errBad("chain shorter than file size: " + path)
	}
	return data[:e.size], nil
}
