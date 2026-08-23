package main
import "strings"

// FS logic core — block IO injected via blkRead/blkWrite so tests run on
// the host. Wire framing: {u16 op,u16 seq,u32 uid,char rname[16],
// u16 path_len,path,payload} (abi/ABI.md v1.1).

var blkRead = func(lba uint32, buf *byte, count uint32) int32 { return -1 }
var blkWrite = func(lba uint32, buf *byte, count uint32) int32 { return -1 }

const Sect = 512

func rdSector(lba uint64, dst []byte) {
	if blkRead(uint32(lba), &dst[0], 1) != 0 {
		panic("blk read")
	}
}

func wrSector(lba uint64, src []byte) {
	if blkWrite(uint32(lba), &src[0], 1) != 0 {
		panic("blk write")
	}
}

func rdSectors(lba uint64, dst []byte) {
	n := uint32(len(dst) / Sect)
	for done := uint32(0); done < n; {
		chunk := n - done
		if chunk > 128 {
			chunk = 128
		}
		off := int(done) * Sect
		if blkRead(uint32(lba+uint64(done)), &dst[off], chunk) != 0 {
			panic("blk bulk read")
		}
		done += chunk
	}
}

func wrSectors(lba uint64, src []byte) {
	n := uint32(len(src) / Sect)
	for done := uint32(0); done < n; {
		chunk := n - done
		if chunk > 128 {
			chunk = 128
		}
		off := int(done) * Sect
		if blkWrite(uint32(lba+uint64(done)), &src[off], chunk) != 0 {
			panic("blk bulk write")
		}
		done += chunk
	}
}

// ---- FAT16 layout (16 MiB disk) ----

const (
	totSec     = 32768
	reserved   = 1
	nFats      = 2
	fatSecs    = 33
	rootEnts   = 512
	rootSecs   = rootEnts * 32 / Sect
	firstRoot  = reserved + nFats*fatSecs
	firstData  = firstRoot + rootSecs
	secPerClus = 4
	clusSize   = secPerClus * Sect
	maxClus    = (totSec - firstData) / secPerClus
	eoc        = 0xFFFF
)

var sect [Sect]byte

func fatOff(i int) int { return i * 2 }

func fatGet(i int) int {
	lba := reserved*Sect + fatOff(i)/Sect
	rdSector(uint64(lba), sect[:])
	v := int(sect[fatOff(i)%Sect]) | int(sect[fatOff(i)%Sect+1])<<8
	if v == eoc {
		return -1
	}
	return v
}

func fatSet(i, v int) {
	lba := reserved*Sect + fatOff(i)/Sect
	off := fatOff(i) % Sect
	rdSector(uint64(lba), sect[:])
	sect[off] = byte(v)
	sect[off+1] = byte(v >> 8)
	wrSector(uint64(lba), sect[:])
	wrSector(uint64(lba+fatSecs), sect[:]) // mirror FAT2
}

func allocClus() int {
	for i := 2; i < maxClus+2; i++ {
		if fatGet(i) == 0 {
			fatSet(i, eoc)
			return i
		}
	}
	return 0
}

func clusLBA(c int) uint64 { return uint64(firstData + (c-2)*secPerClus) }

func fmtDisk() {
	zeros := make([]byte, 128*Sect)
	for lba := 0; lba < totSec; lba += 128 {
		wrSectors(uint64(lba), zeros)
	}
	b := make([]byte, Sect)
	copy(b[0:3], []byte{0xEB, 0x3C, 0x90})
	copy(b[3:11], "MSDOS5.0")
	le16(b, 11, Sect)
	le16(b, 13, secPerClus)
	le16(b, 14, reserved)
	le16(b, 16, nFats)
	le16(b, 17, rootEnts)
	le16(b, 19, totSec)
	b[21] = 0xF8
	le16(b, 22, fatSecs)
	copy(b[54:58], "FAT16   ")
	wrSector(0, b)
	ft := make([]byte, Sect)
	le16(ft, 0, 0xFFF8)
	le16(ft, 2, eoc)
	for f := 0; f < nFats; f++ {
		wrSector(uint64(reserved+f*fatSecs), ft)
	}
}

func le16(b []byte, o int, v int) {
	b[o] = byte(v)
	b[o+1] = byte(v >> 8)
}
func le32(b []byte, o int, v int) {
	le16(b, o, v&0xFFFF)
	le16(b, o+2, v>>16&0xFFFF)
}
func g16(b []byte, o int) int { return int(b[o]) | int(b[o+1])<<8 }
func g32(b []byte, o int) int { return g16(b, o) | g16(b, o+2)<<16 }

// ---- directories ----

type dent struct {
	attr  byte
	name  string
	clus  int
	size  int
	valid bool
	off   int
}

func to83(elem string) [11]byte {
	var r [11]byte
	for i := range r {
		r[i] = ' '
	}
	dot := -1
	for i := len(elem) - 1; i >= 0; i-- {
		if elem[i] == '.' {
			dot = i
			break
		}
	}
	base, ext := elem, ""
	if dot >= 0 {
		base, ext = elem[:dot], elem[dot+1:]
	}
	if len(base) > 8 {
		base = base[:8]
	}
	if len(ext) > 3 {
		ext = ext[:3]
	}
	for i := 0; i < len(base); i++ {
		r[i] = up(base[i])
	}
	for i := 0; i < len(ext); i++ {
		r[8+i] = up(ext[i])
	}
	return r
}

func up(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func readDir(area []byte) []dent {
	var out []dent
	for o := 0; o+32 <= len(area); o += 32 {
		c := area[o]
		if c == 0 {
			break
		}
		if c == 0xE5 || c == '.' || area[o+11]&0x3F == 0x0F {
			continue
		}
		nm := make([]byte, 0, 12)
		for i := 0; i < 8 && area[o+i] != ' '; i++ {
			nm = append(nm, area[o+i])
		}
		if area[o+8] != ' ' {
			nm = append(nm, '.')
			for i := 8; i < 11 && area[o+i] != ' '; i++ {
				nm = append(nm, area[o+i])
			}
		}
		out = append(out, dent{
			attr:  area[o+11],
			name:  lower(string(nm)),
			clus:  g16(area, o+26),
			size:  g32(area, o+28),
			valid: true,
			off:   o,
		})
	}
	return out
}

func loadDir(firstClus int) []byte {
	if firstClus == 0 {
		out := make([]byte, rootSecs*Sect)
		rdSectors(uint64(firstRoot), out)
		return out
	}
	var out []byte
	for c := firstClus; c > 0; c = fatGet(c) {
		chunk := make([]byte, clusSize)
		rdSectors(clusLBA(c), chunk)
		out = append(out, chunk...)
	}
	return out
}

func saveRoot(area []byte) { wrSectors(uint64(firstRoot), area) }

func saveDirChain(firstClus int, area []byte) {
	c := firstClus
	o := 0
	for c > 0 && o < len(area) {
		end := o + clusSize
		if end > len(area) {
			end = len(area)
		}
		wrSectors(clusLBA(c), area[o:end])
		o += clusSize
		c = fatGet(c)
	}
}

func freeChain(c int) {
	for c > 1 {
		n := fatGet(c)
		fatSet(c, 0)
		if n < 0 {
			break
		}
		c = n
	}
}

func elems(p string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(p[i])
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func walk(path string) (int, *dent, []byte, string) {
	es := elems(path)
	area := loadDir(0)
	dc := 0
	for i := 0; i < len(es); i++ {
		ds := readDir(area)
		var hit *dent
		for k := range ds {
			if ds[k].name == lower(es[i]) {
				hit = &ds[k]
				break
			}
		}
		last := i == len(es)-1
		if last {
			return dc, hit, area, es[i]
		}
		if hit == nil || hit.attr&0x10 == 0 {
			return dc, nil, area, es[i]
		}
		dc = hit.clus
		area = loadDir(hit.clus)
	}
	return dc, nil, area, ""
}

func extendTo(first int, size int) int {
	need := (size + clusSize - 1) / clusSize
	if need == 0 {
		need = 1
	}
	have := 0
	for c := first; c > 0 && have < need; c = fatGet(c) {
		have++
	}
	if first == 0 {
		nf := allocClus()
		if nf == 0 {
			return 0
		}
		first = nf
		have = 1
	}
	if have < need {
		c := first
		for fatGet(c) > 0 {
			c = fatGet(c)
		}
		for h := have; h < need; h++ {
			nf := allocClus()
			if nf == 0 {
				return first
			}
			fatSet(c, nf)
			c = nf
		}
	}
	return first
}

func writeRange(first int, off int, data []byte) {
	c := first
	skipped := 0
	for c > 0 && skipped+clusSize <= off {
		skipped += clusSize
		c = fatGet(c)
	}
	pos := off - skipped
	d := 0
	for d < len(data) && c > 0 {
		chunk := make([]byte, Sect)
		baseInClus := pos % clusSize
		s := baseInClus / Sect
		so := pos % Sect
		n := Sect - so
		if n > len(data)-d {
			n = len(data) - d
		}
		LBA := clusLBA(c) + uint64(s)
		rdSector(LBA, chunk)
		for i := 0; i < n; i++ {
			chunk[so+i] = data[d+i]
		}
		wrSector(LBA, chunk)
		d += n
		pos += n
		if pos%clusSize == 0 {
			c = fatGet(c)
		}
	}
}

func readRange(first int, off int, n int) []byte {
	out := make([]byte, 0, n)
	c := first
	skipped := 0
	for c > 0 && skipped+clusSize <= off {
		skipped += clusSize
		c = fatGet(c)
	}
	pos := off - skipped
	for len(out) < n && c > 0 {
		chunk := make([]byte, Sect)
		baseInClus := pos % clusSize
		s := baseInClus / Sect
		so := pos % Sect
		rdSector(clusLBA(c)+uint64(s), chunk)
		take := Sect - so
		if take > n-len(out) {
			take = n - len(out)
		}
		out = append(out, chunk[so:so+take]...)
		pos += take
		if pos%clusSize == 0 {
			c = fatGet(c)
		}
	}
	return out
}

// ---- request protocol ----

const (
	opOpen  = 1
	opClose = 2
	opRead  = 3
	opWrite = 4
	opStat  = 5
	opMkdir = 6
	opDel   = 7
	opLS    = 8
)

type ofile struct {
	clus int
	cur  int
	size int
	dir  bool
	path string
}

var openTab = map[uint32]*ofile{}
var nextFH uint32 = 16

func newFHP(clus, size int, dir bool, path string) uint32 {
	nextFH++
	openTab[nextFH] = &ofile{clus: clus, cur: 0, size: size, dir: dir, path: path}
	return nextFH
}

func rootFor(uid uint32) string {
	if uid == 0 {
		return "/"
	}
	return "/home/" + itoa(int(uid)) + "/"
}

func rooted(uid uint32, p string) string {
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	// /etc is world-readable (ABI v1: readable by all, admin-write only)
	if p == "etc" || strings.HasPrefix(p, "etc/") {
		return "/" + p
	}
	r := rootFor(uid)
	return r + p
}

// writeDenied reports whether uid may not modify system paths.
func writeDenied(uid uint32, path string) bool {
	if uid == 0 {
		return false
	}
	lp := lower(path)
	return lp == "etc" || strings.HasPrefix(lp, "etc/")
}

func errnoReply(e int, extra ...byte) []byte {
	out := []byte{byte(e), byte(e >> 8)}
	return append(out, extra...)
}

func cstrz(b []byte) string {
	end := 0
	for end < len(b) && b[end] != 0 {
		end++
	}
	return string(b[:end])
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// handleReq parses a framed request and returns (resp, rname). rname is
// the reply channel for port-routed callers; empty for sync callers.
func handleReq(req []byte) ([]byte, string) {
	if len(req) < 26 {
		return errnoReply(52), ""
	}
	op := g16(req, 0)
	uid := g32(req, 4)
	rname := cstrz(req[8:24])
	plen := g16(req, 24)
	if 26+plen > len(req) {
		return errnoReply(52), rname
	}
	path := rooted(uint32(uid), string(req[26:26+plen]))
	payload := req[26+plen:]
	resp := dispatch(op, uid, path, payload)
	return resp, rname
}

func dispatch(op int, uid int, path string, payload []byte) []byte {
	if writeDenied(uint32(uid), path) &&
		(op == opOpen || op == opWrite || op == opMkdir || op == opDel) {
		// reads of /etc are allowed; creates/writes/deletes are not
		if op != opOpen || (len(payload) >= 8 && g32(payload, 4) != 0) {
			return errnoReply(63)
		}
	}
	if op == opOpen || op == opWrite || op == opMkdir {
		ensureUserRoot(uint32(uid))
	}
	switch op {
	case opOpen:
		create := false
		if len(payload) >= 8 {
			create = g32(payload, 4) != 0
		}
		dc, hit, area, last := walk(path)
		if hit == nil {
			if !create {
				return errnoReply(44)
			}
			nc := allocClus()
			if nc == 0 {
				return errnoReply(28)
			}
			e83 := to83(last)
			slot := -1
			for o := 0; o+32 <= len(area); o += 32 {
				if area[o] == 0x00 || area[o] == 0xE5 {
					slot = o
					break
				}
			}
			if slot < 0 {
				return errnoReply(28)
			}
			copy(area[slot:slot+11], e83[:])
			area[slot+11] = 0x20
			le16(area, slot+26, nc)
			le32(area, slot+28, 0)
			saveArea(dc, area)
			fh := newFHP(nc, 0, false, path)
			return errnoReply(0, byte(fh), byte(fh>>8), byte(fh>>16), byte(fh>>24))
		}
		if hit.attr&0x10 != 0 {
			return errnoReply(70)
		}
		fh := newFHP(hit.clus, hit.size, false, path)
		if create {
			freeChain(hit.clus)
			nc := allocClus()
			hit.clus = nc
			hit.size = 0
			o := hit.off
			le16(area, o+26, nc)
			le32(area, o+28, 0)
			saveArea(dc, area)
			openTab[fh].clus = nc
		}
		return errnoReply(0, byte(fh), byte(fh>>8), byte(fh>>16), byte(fh>>24))

	case opClose:
		if len(payload) < 4 {
			return errnoReply(52)
		}
		delete(openTab, uint32(g32(payload, 0)))
		return errnoReply(0)

	case opRead:
		fh := uint32(g32(payload, 0))
		n := g32(payload, 4)
		of := openTab[fh]
		if of == nil || of.dir {
			return errnoReply(8)
		}
		if of.cur+n > of.size {
			n = of.size - of.cur
		}
		data := readRange(of.clus, of.cur, n)
		of.cur += n
		out := errnoReply(0)
		var nb [4]byte
		le32(nb[:], 0, n)
		out = append(out, nb[:]...)
		out = append(out, data...)
		return out

	case opWrite:
		fh := uint32(g32(payload, 0))
		n := g32(payload, 4)
		of := openTab[fh]
		if of == nil || of.dir || n == 0 || len(payload) < 8+n {
			return errnoReply(8)
		}
		data := payload[8 : 8+n]
		first := extendTo(of.clus, of.cur+n)
		of.clus = first
		writeRange(first, of.cur, data)
		of.cur += n
		if of.cur > of.size {
			of.size = of.cur
		}
		updateSize(of.path, of.clus, of.size)
		out := errnoReply(0)
		var nb [4]byte
		le32(nb[:], 0, n)
		out = append(out, nb[:]...)
		return out

	case opStat:
		_, hit, _, _ := walk(path)
		if hit == nil {
			return errnoReply(44)
		}
		out := errnoReply(0)
		var nb [4]byte
		le32(nb[:], 0, hit.size)
		out = append(out, nb[:]...)
		isdir := byte(0)
		if hit.attr&0x10 != 0 {
			isdir = 1
		}
		out = append(out, isdir)
		return out

	case opMkdir:
		if err := mkdirPath(path); err != 0 {
			return errnoReply(err)
		}
		return errnoReply(0)

	case opDel:
		dc, hit, area, _ := walk(path)
		if hit == nil {
			return errnoReply(44)
		}
		if hit.attr&0x10 != 0 {
			return errnoReply(54)
		}
		freeChain(hit.clus)
		area[hit.off] = 0xE5
		saveArea(dc, area)
		return errnoReply(0)

	case opLS:
		_, hit, area, last := walk(path)
		if hit != nil && hit.attr&0x10 != 0 {
			area = loadDir(hit.clus)
		} else if hit == nil && (path == "/" || path == "" || last == "") {
			area = loadDir(0)
		} else if hit == nil {
			return errnoReply(44)
		}
		ds := readDir(area)
		out := errnoReply(0)
		cnt := len(ds)
		out = append(out, byte(cnt), byte(cnt>>8))
		for _, d := range ds {
			t := byte(0)
			if d.attr&0x10 != 0 {
				t = 1
			}
			out = append(out, t, byte(len(d.name)))
			out = append(out, d.name...)
		}
		return out
	}
	return errnoReply(52)
}

func saveArea(dc int, area []byte) {
	if dc == 0 {
		saveRoot(area)
	} else {
		saveDirChain(dc, area)
	}
}

func createDirIn(dc int, area []byte, name string) int {
	nc := allocClus()
	if nc == 0 {
		return 0
	}
	zeros := make([]byte, clusSize)
	wrSectors(clusLBA(nc), zeros)
	e83 := to83(name)
	slot := -1
	for o := 0; o+32 <= len(area); o += 32 {
		if area[o] == 0x00 || area[o] == 0xE5 {
			slot = o
			break
		}
	}
	if slot < 0 {
		return 0
	}
	copy(area[slot:slot+11], e83[:])
	area[slot+11] = 0x10
	le16(area, slot+26, nc)
	le32(area, slot+28, 0)
	saveArea(dc, area)
	return nc
}

func mkdirPath(path string) int {
	dc, hit, area, last := walk(path)
	if hit != nil {
		return 40
	}
	if createDirIn(dc, area, last) == 0 {
		return 28
	}
	return 0
}

func ensureUserRoot(uid uint32) {
	if uid == 0 {
		return
	}
	_, hit, rootArea, _ := walk("/home")
	var homeClus int
	if hit == nil {
		homeClus = createDirIn(0, rootArea, "home")
		if homeClus == 0 {
			return
		}
	} else if hit.attr&0x10 != 0 {
		homeClus = hit.clus
	} else {
		return
	}
	uname := itoa(int(uid))
	_, uhit, harea, _ := walk("/home/" + uname)
	if uhit == nil {
		createDirIn(homeClus, harea, uname)
	}
}

func updateSize(path string, clus, size int) {
	dc, hit, area, _ := walk(path)
	if hit == nil {
		return
	}
	o := hit.off
	le16(area, o+26, clus)
	le32(area, o+28, size)
	saveArea(dc, area)
}

func errnoOf(rep []byte) int {
	if len(rep) < 2 {
		return -99
	}
	return g16(rep, 0)
}
