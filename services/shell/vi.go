package main

import (
	"fmt"
	"strings"

	lib "kernel.lane/guests/lib"
)

type viBuffer struct {
	lines     []string
	undoLines []string
	undoCurs  struct{ ln, col int }
	yanked    []string
}

type viEditor struct {
	s     *Shell
	buf   viBuffer
	file  string
	curs  struct{ ln, col int }
	mode  int
	ex    strings.Builder
	dirty bool
	done  bool
}

const (
	modeNormal = 0
	modeInsert = 1
	modeEx     = 2
)

func (s *Shell) cmdVi(args []string) {
	if len(args) < 1 {
		s.out("vi: usage: vi <file>")
		return
	}
	path := s.resolve(args[0])
	var data []byte
	// Check if file exists first (avoids ReadFile error on empty file)
	if _, err := s.fs.Stat(path); err == nil {
		buf := make([]byte, 4096)
		off := uint64(0)
		for {
			n, err := s.fs.ReadFile(path, off, buf)
			if err != nil {
				break
			}
			data = append(data, buf[:n]...)
			off += uint64(n)
			if n < len(buf) {
				break
			}
		}
	}
	ed := &viEditor{s: s, file: path, mode: modeNormal}
	if len(data) > 0 {
		ed.buf.lines = strings.Split(string(data), "\n")
	} else {
		ed.buf.lines = []string{""}
	}
	ed.run()
}

func (e *viEditor) run() {
	e.draw()
	for {
		if e.done {
			e.s.k.PortSend(e.s.con, []byte{'\r', '\n'})
			e.s.prompt()
			return
		}
		ev, ok := lib.PollInput(e.s.k)
		if !ok {
			e.s.k.Yield()
			continue
		}
		if ev.Kind != lib.KeyDown {
			continue
		}
		c := rune(ev.Codepoint)
		if e.mode == modeInsert {
			if c == 0x1b || ev.Codepoint == 27 {
				e.mode = modeNormal
				e.draw()
				continue
			}
			if c == '\r' || c == '\n' {
				e.enterNewline()
				e.draw()
				continue
			}
			if c == 8 || c == 127 {
				if e.curs.col > 0 {
					e.curs.col--
					e.deleteChar()
					e.dirty = true
				}
				e.draw()
				continue
			}
			if c >= 32 && c < 127 {
				e.insertChar(c)
				e.draw()
				continue
			}
			e.draw()
			continue
		}
		if e.mode == modeEx {
			if c == '\r' || c == '\n' {
				e.doEx(strings.TrimSpace(e.ex.String()))
				continue
			}
			if c == 0x1b || ev.Codepoint == 27 {
				e.mode = modeNormal
				e.ex.Reset()
				e.draw()
				continue
			}
			if c == 8 || c == 127 {
				str := e.ex.String()
				if len(str) > 0 {
					e.ex.Reset()
					e.ex.WriteString(str[:len(str)-1])
				}
				e.drawEx()
				continue
			}
			if c >= 32 && c < 127 {
				e.ex.WriteRune(c)
			}
			e.drawEx()
			continue
		}
		e.handleNormal(c)
		if e.mode == modeEx {
			e.drawEx()
		} else {
			e.draw()
		}
	}
}

func (e *viEditor) draw() {
	s := e.s
	var msg []byte
	msg = append(msg, '\r')
	for i, line := range e.buf.lines {
		marker := ' '
		if i == e.curs.ln {
			marker = '^'
		}
		rendered := line
		if i == e.curs.ln && len(rendered) == 0 {
			rendered = " "
		}
		if i == e.curs.ln {
			col := e.curs.col
			lineRunes := []rune(rendered)
			if col > len(lineRunes) {
				col = len(lineRunes)
			}
			if col < len(lineRunes) {
				rendered = string(lineRunes[:col]) + "[" + string(lineRunes[col:]) + "]"
			} else {
				rendered = string(lineRunes[:col]) + "[]"
			}
		}
		msg = append(msg, fmt.Sprintf("%c %s\n", marker, rendered)...)
	}
	msg = append(msg, ':', ' ')
	s.k.PortSend(s.con, msg)
}

func (e *viEditor) drawEx() {
	s := e.s
	ex := e.ex.String()
	msg := []byte{'\r', ':', ' '}
	msg = append(msg, []byte(ex)...)
	s.k.PortSend(s.con, msg)
}

func (e *viEditor) insertChar(c rune) {
	ln := e.curs.ln
	if ln >= len(e.buf.lines) {
		return
	}
	line := []rune(e.buf.lines[ln])
	col := e.curs.col
	if col > len(line) {
		col = len(line)
	}
	line = append(line[:col], append([]rune{c}, line[col:]...)...)
	e.buf.lines[ln] = string(line)
	e.curs.col = col + 1
	e.dirty = true
}

func (e *viEditor) enterNewline() {
	ln := e.curs.ln
	if ln >= len(e.buf.lines) {
		return
	}
	line := []rune(e.buf.lines[ln])
	col := e.curs.col
	if col > len(line) {
		col = len(line)
	}
	before := string(line[:col])
	after := string(line[col:])
	e.buf.lines[ln] = before
	e.buf.lines = append(e.buf.lines[:ln+1], append([]string{after}, e.buf.lines[ln+1:]...)...)
	e.curs.ln = ln + 1
	e.curs.col = 0
	e.dirty = true
}

func (e *viEditor) deleteChar() {
	ln := e.curs.ln
	if ln >= len(e.buf.lines) {
		return
	}
	line := []rune(e.buf.lines[ln])
	col := e.curs.col
	if col < len(line) {
		e.buf.lines[ln] = string(append(line[:col], line[col+1:]...))
	}
}

func (e *viEditor) handleNormal(c rune) {
	l := &e.curs.ln
	col := &e.curs.col
	switch c {
	case 'h':
		if *col > 0 {
			*col--
		}
	case 'l':
		line := []rune(e.buf.lines[*l])
		if *col < len(line) {
			*col++
		}
	case 'j':
		if *l < len(e.buf.lines)-1 {
			*l++
			lineLen := len([]rune(e.buf.lines[*l]))
			if *col > lineLen {
				*col = lineLen
			}
		}
	case 'k':
		if *l > 0 {
			*l--
			lineLen := len([]rune(e.buf.lines[*l]))
			if *col > lineLen {
				*col = lineLen
			}
		}
	case 'i':
		e.mode = modeInsert
	case 'a':
		line := []rune(e.buf.lines[*l])
		if *col < len(line) {
			*col++
		}
		e.mode = modeInsert
	case 'x':
		e.deleteChar()
		e.dirty = true
	case 'D':
		if *l < len(e.buf.lines) {
			line := []rune(e.buf.lines[*l])
			e.buf.lines[*l] = string(line[:*col])
			e.dirty = true
		}
	case 'd':
		ev, ok := lib.PollInput(e.s.k)
		if ok && ev.Kind == lib.KeyDown && rune(ev.Codepoint) == 'd' {
			e.deleteLine()
			return
		}
	case 'y':
		ev, ok := lib.PollInput(e.s.k)
		if ok && ev.Kind == lib.KeyDown && rune(ev.Codepoint) == 'y' {
			e.yankLine()
			return
		}
	case 'p':
		e.pasteAfter()
	case ':':
		e.mode = modeEx
		e.ex.Reset()
	case 0x1b:
	}
}

func (e *viEditor) deleteLine() {
	l := e.curs.ln
	if l >= len(e.buf.lines) {
		return
	}
	e.buf.yanked = []string{e.buf.lines[l]}
	e.buf.lines = append(e.buf.lines[:l], e.buf.lines[l+1:]...)
	if len(e.buf.lines) == 0 {
		e.buf.lines = []string{""}
	}
	if l >= len(e.buf.lines) {
		l = len(e.buf.lines) - 1
	}
	e.curs.ln = l
	line := []rune(e.buf.lines[l])
	if e.curs.col > len(line) {
		e.curs.col = len(line)
	}
	e.dirty = true
}

func (e *viEditor) yankLine() {
	l := e.curs.ln
	if l < len(e.buf.lines) {
		e.buf.yanked = []string{e.buf.lines[l]}
	}
}

func (e *viEditor) pasteAfter() {
	if len(e.buf.yanked) == 0 {
		return
	}
	l := e.curs.ln
	insertIdx := l + 1
	if insertIdx > len(e.buf.lines) {
		insertIdx = len(e.buf.lines)
	}
	for i, y := range e.buf.yanked {
		e.buf.lines = append(e.buf.lines[:insertIdx+i], append([]string{y}, e.buf.lines[insertIdx+i:]...)...)
	}
	e.curs.ln = l + 1
	e.dirty = true
}

func (e *viEditor) doEx(cmd string) {
	parts := strings.SplitN(cmd, " ", 2)
	switch parts[0] {
	case "w":
		e.write()
		e.mode = modeNormal
		e.ex.Reset()
		e.draw()
	case "q":
		if e.dirty {
			e.s.out("vi: E37: No write since last change")
			e.mode = modeNormal
			e.ex.Reset()
			e.draw()
		} else {
			e.quit()
		}
	case "q!":
		e.quit()
	case "wq", "x":
		e.write()
		e.quit()
	case "":
		e.mode = modeNormal
		e.ex.Reset()
		e.draw()
	default:
		e.s.out("vi: unknown ex command: " + parts[0])
		e.mode = modeNormal
		e.ex.Reset()
		e.draw()
	}
}

func (e *viEditor) quit() {
	e.done = true
}

func (e *viEditor) write() {
	if e.file == "" {
		e.s.out("vi: no file name")
		return
	}
	_ = e.s.fs.Create(e.file) // create if not exists; ignore if exists
	content := strings.Join(e.buf.lines, "\n")
	if _, err := e.s.fs.WriteFile(e.file, 0, []byte(content)); err != nil {
		e.s.out("vi: " + err.Error())
		return
	}
	e.dirty = false
}
