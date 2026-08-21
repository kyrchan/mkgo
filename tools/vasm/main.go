// vasm: assembler for the kernel's restricted ISA (see kernel/vm/isa.h).
//
// Usage: vasm input.vasm output.vbin
//
// Syntax:
//   .text | .data            switch section
//   label:                   define label in current section
//   mov r1, 42               imm -> reg
//   mov r1, r2               reg -> reg
//   mov r1, [r2+8]           mem -> reg
//   mov [r2+8], r1           reg -> mem
//   lea r1, [r2+16]          r1 = r2 + 16   ([sym] == [r0+symoff])
//   sub r1, r2 | sub r1, 10  sets ZF
//   jz label                 branch if ZF
//   vmovdqu v1, [r2+32]      load 256 bits
//   vmovdqu [r2+32], v1      store 256 bits
//   vpbroadcast.q v1, r6     broadcast scalar (.b/.w/.d/.q)
//   vpsub.q v4, v2, v3       v4 = v2 - v3 (lanewise)
//   vpcmpeq.q v7, v5, v6     v7 = lane mask; ZF = all lanes equal
//   halt                     stop the machine
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type insn struct {
	op, dst, src, aux byte
	imm               uint64
}

type prog struct {
	text []insn
	data []byte
	labels map[string]label
}

type label struct {
	section string // "text" or "data"
	offset  uint64
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: vasm in.vasm out.vbin")
		os.Exit(2)
	}
	src, err := os.ReadFile(os.Args[1])
	check(err)
	p := assemble(string(src))
	check(os.WriteFile(os.Args[2], p.emit(), 0o644))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vasm:", err)
		os.Exit(1)
	}
}

func assemble(src string) *prog {
	p := &prog{labels: map[string]label{}}
	type pending struct {
		mnemonic string
		ops      []string
		line     int
	}
	var pend []pending

	section := "text"
	ninsn := 0 // instructions queued so far (p.text fills in pass 2)
	for ln, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, ";"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		// peel off any leading "name:" labels
		for {
			col := strings.Index(line, ":")
			if col < 0 || strings.Contains(line[:col], " ") ||
				strings.Contains(line[:col], "[") {
				break
			}
			name := line[:col]
			var off uint64
			if section == "text" {
				off = uint64(ninsn) * 16
			} else {
				off = uint64(len(p.data))
			}
			p.labels[name] = label{section, off}
			line = strings.TrimSpace(line[col+1:])
		}
		if line == "" {
			continue
		}
		switch {
		case line == ".text":
			section = "text"
			continue
		case line == ".data":
			section = "data"
			continue
		case strings.HasPrefix(line, ".space"):
			f := strings.Fields(line)
			n, _ := strconv.Atoi(f[1])
			p.data = append(p.data, make([]byte, n)...)
			continue
		case strings.HasPrefix(line, ".int"):
			for _, f := range strings.Fields(line)[1:] {
				v, _ := parseNum(f)
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], v)
				p.data = append(p.data, b[:]...)
			}
			continue
		case strings.HasPrefix(line, ".byte"):
			for _, f := range strings.Fields(line)[1:] {
				v, _ := parseNum(f)
				p.data = append(p.data, byte(v))
			}
			continue
		}
		f := strings.Fields(line)
		mn := f[0]
		rest := ""
		if len(f) > 1 {
			rest = strings.Join(f[1:], "")
		}
		ops := splitOps(rest)
		if mn != "halt" && len(ops) == 0 {
			fail(ln, "missing operands")
		}
		pend = append(pend, pending{mn, ops, ln})
		ninsn++
	}

	// pass 2: encode with labels resolved
	for _, pd := range pend {
		in := p.encode(pd.mnemonic, pd.ops, pd.line)
		p.text = append(p.text, in)
	}
	return p
}

func fail(line int, msg string) {
	check(fmt.Errorf("line %d: %s", line+1, msg))
}

func splitOps(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	out = append(out, cur.String())
	return out
}

func parseNum(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 3 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return uint64(s[1]), nil
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

func isReg(s string) bool  { return len(s) >= 2 && s[0] == 'r' }
func isVec(s string) bool  { return len(s) >= 2 && s[0] == 'v' }
func regNum(s string) byte { n, _ := strconv.Atoi(s[1:]); return byte(n) }

// mem parses "[expr]" where expr = reg | reg+N | sym | sym+N.
func mem(s string) (base byte, disp uint64, sym string, ok bool) {
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return
	}
	ok = true
	e := s[1 : len(s)-1]
	if i := strings.Index(e, "+"); i >= 0 {
		symPart, offPart := e[:i], e[i+1:]
		if isReg(symPart) {
			base = regNum(symPart)
		} else {
			sym = symPart
		}
		disp, _ = parseNum(offPart)
		return
	}
	if isReg(e) {
		base = regNum(e)
		return
	}
	sym = e
	return
}

func sizeClass(suffix string) (byte, bool) {
	switch suffix {
	case ".b":
		return 0, true
	case ".w":
		return 1, true
	case ".d":
		return 2, true
	case ".q", "":
		return 3, true
	}
	return 0, false
}

func (p *prog) resolve(op string) uint64 {
	l, ok := p.labels[op]
	if !ok {
		check(fmt.Errorf("undefined symbol %q", op))
	}
	return l.offset
}

func (p *prog) memEA(o string, line int) (base byte, disp uint64) {
	b, d, sym, ok := mem(o)
	if !ok {
		fail(line, "bad memory operand "+o)
	}
	if sym != "" {
		d += p.resolve(sym)
	}
	return b, d
}

func (p *prog) encode(mn string, ops []string, line int) insn {
	in := insn{}
	switch mn {
	case "mov":
		in.op = 1
		dst, src := ops[0], ops[1]
		if strings.HasPrefix(dst, "[") {
			// reg -> mem; optional width suffix on mnemonic handled below
			in.aux = 3 | (3 << 4) // mode 3, width q by default
			b, d := p.memEA(dst, line)
			in.dst, in.src, in.imm = b, regNum(src), d
			return in
		}
		if strings.HasPrefix(src, "[") {
			in.aux = 2 | (3 << 4) // mem -> reg
			b, d := p.memEA(src, line)
			in.dst, in.src, in.imm = regNum(dst), b, d
			return in
		}
		if isReg(src) {
			in.aux = 1
			in.dst, in.src = regNum(dst), regNum(src)
			return in
		}
		v, _ := parseNum(src)
		in.aux = 0
		in.dst, in.imm = regNum(dst), v
		return in

	case "lea":
		in.op = 2
		b, d := p.memEA(ops[1], line)
		in.dst, in.src, in.imm = regNum(ops[0]), b, d
		return in

	case "sub":
		in.op = 3
		if isReg(ops[1]) {
			in.aux = 1
			in.dst, in.src = regNum(ops[0]), regNum(ops[1])
		} else {
			in.aux = 0
			in.dst, _ = regNum(ops[0]), 0
			in.imm, _ = parseNum(ops[1])
		}
		return in

	case "jz":
		in.op = 4
		in.imm = p.resolve(ops[0])
		return in

	case "vmovdqu":
		in.op = 5
		if strings.HasPrefix(ops[0], "[") { // store
			in.aux = 0x80
			b, d := p.memEA(ops[0], line)
			in.dst, in.src, in.imm = regNum(ops[1]), b, d
			return in
		}
		b, d := p.memEA(ops[1], line) // load
		in.dst, in.src, in.imm = regNum(ops[0]), b, d
		return in

	case "vpbroadcast", "vpbroadcast.b", "vpbroadcast.w", "vpbroadcast.d", "vpbroadcast.q":
		cls, _ := sizeClass(strings.TrimPrefix(mn, "vpbroadcast"))
		in.op = 6
		in.aux = cls
		in.dst = regNum(ops[0])
		in.src = regNum(ops[1])
		return in

	case "vpsub", "vpsub.b", "vpsub.w", "vpsub.d", "vpsub.q",
		"vpcmpeq", "vpcmpeq.b", "vpcmpeq.w", "vpcmpeq.d", "vpcmpeq.q":
		cls, _ := sizeClass(suffixOf(mn))
		in.op = 7
		if strings.HasPrefix(mn, "vpcmpeq") {
			in.op = 8
		}
		in.aux = cls | regNum(ops[2])<<4
		in.dst, in.src = regNum(ops[0]), regNum(ops[1])
		return in

	case "halt":
		in.op = 0xFF
		return in
	}
	fail(line, "unknown instruction "+mn)
	return in
}

func suffixOf(s string) string {
	if i := strings.Index(s, "."); i >= 0 {
		return s[i:]
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (p *prog) emit() []byte {
	hdrLen := 32
	codeLen := len(p.text) * 16
	entry := uint64(0)
	if l, ok := p.labels["start"]; ok && l.section == "text" {
		entry = l.offset
	}
	out := make([]byte, hdrLen+codeLen+len(p.data))
	copy(out[0:4], "VBIN")
	binary.LittleEndian.PutUint32(out[4:], 1)
	binary.LittleEndian.PutUint32(out[8:], uint32(entry))
	binary.LittleEndian.PutUint32(out[12:], uint32(hdrLen))
	binary.LittleEndian.PutUint32(out[16:], uint32(codeLen))
	binary.LittleEndian.PutUint32(out[20:], uint32(hdrLen+codeLen))
	binary.LittleEndian.PutUint32(out[24:], uint32(len(p.data)))
	for i, in := range p.text {
		o := hdrLen + i*16
		out[o] = in.op
		out[o+1] = in.dst
		out[o+2] = in.src
		out[o+3] = in.aux
		binary.LittleEndian.PutUint64(out[o+8:], in.imm)
	}
	copy(out[hdrLen+codeLen:], p.data)
	return out
}
