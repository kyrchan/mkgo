package terminal

// Terminal: a text-mode console on the §9.FB framebuffer.
//
// Cell grid of cols×rows (8×16 glyph cells by default), ANSI-lite SGR
// parsing (\x1b[0m reset, 30-37 fg, 40-47 bg, 1 bright, \n \r \t
// backspace), scroll-on-overflow, and damage-tracked rendering into the
// window's pixel area followed by UPDATE_RECT.

import (
	"bytes"
	"strconv"
)

const (
	GlyphW = 8
	GlyphH = 16 // 8×8 font doubled vertically for readability

	DefaultCols = 80
	DefaultRows = 25
)

// XRGB color word (alpha byte high; memory layout B,G,R,X on LE).
func xrgb(r, g, b uint8) uint32 {
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// EGA-ish 16-color palette (index = SGR fg/bg code).
var palette = [16]uint32{
	xrgb(0, 0, 0),       // 0 black
	xrgb(170, 0, 0),     // 1 red
	xrgb(0, 170, 0),     // 2 green
	xrgb(170, 85, 0),    // 3 brown
	xrgb(0, 0, 170),     // 4 blue
	xrgb(170, 0, 170),   // 5 magenta
	xrgb(0, 170, 170),   // 6 cyan
	xrgb(170, 170, 170), // 7 light gray
	xrgb(85, 85, 85),    // 8 dark gray
	xrgb(255, 85, 85),   // 9 light red
	xrgb(85, 255, 85),   // 10 light green
	xrgb(255, 255, 85),  // 11 yellow
	xrgb(85, 85, 255),   // 12 light blue
	xrgb(255, 85, 255),  // 13 light magenta
	xrgb(85, 255, 255),  // 14 light cyan
	xrgb(255, 255, 255), // 15 white
}

type cell struct {
	ch rune
	fg uint8
	bg uint8
}

// Terminal is a text grid rendered onto an FBWindow.
type Terminal struct {
	fb *FBWindow

	cols, rows int
	grid       []cell
	curX, curY int
	fg, bg     uint8

	dirty bool // any pending change since last Flush

	// parser state
	esc    bool
	csi    bool
	csiBuf []byte
}

// NewTerminal sizes the grid to the window (or defaults) and clears it.
func New(fb *FBWindow) *Terminal {
	t := &Terminal{fb: fb, fg: 7, bg: 0}
	t.resize()
	t.Clear()
	return t
}

func (t *Terminal) resize() {
	w := int(t.fb.Width() / GlyphW)
	h := int(t.fb.Height() / GlyphH)
	if w <= 0 {
		w = DefaultCols
	}
	if h <= 0 {
		h = DefaultRows
	}
	t.cols, t.rows = w, h
	t.grid = make([]cell, w*h)
	t.curX, t.curY = 0, 0
}

// Clear wipes the grid to spaces in the current bg and homes the cursor.
func (t *Terminal) Clear() {
	for i := range t.grid {
		t.grid[i] = cell{ch: ' ', fg: t.fg, bg: t.bg}
	}
	t.curX, t.curY = 0, 0
	t.dirty = true
}

// WriteString feeds text through the ANSI-lite parser.
func (t *Terminal) WriteString(s string) {
	for i := 0; i < len(s); i++ {
		t.putc(byte(s[i]))
	}
}

func (t *Terminal) putc(c byte) {
	switch {
	case t.esc:
		t.esc = false
		if c == '[' {
			t.csi = true
			t.csiBuf = t.csiBuf[:0]
			return
		}
		return // other escapes unsupported (ignored)
	case t.csi:
		if c >= '0' && c <= '9' || c == ';' {
			t.csiBuf = append(t.csiBuf, c)
			return
		}
		t.csi = false
		if c == 'm' {
			t.applySGR(string(t.csiBuf))
		}
		return // other CSI finals ignored
	}

	switch c {
	case 27:
		t.esc = true
		return
	case '\n':
		t.newLine()
	case '\r':
		t.curX = 0
	case '\t':
		t.advanceToTabStop()
	case 8, 127: // backspace
		if t.curX > 0 {
			t.curX--
			t.setCell(' ')
		}
	default:
		if c >= 32 && c != 127 {
			t.setCell(rune(c))
			t.advance()
		}
	}
}

func (t *Terminal) setCell(ch rune) {
	t.grid[t.curY*t.cols+t.curX] = cell{ch: ch, fg: t.fg, bg: t.bg}
	t.dirty = true
}

func (t *Terminal) advance() {
	t.curX++
	if t.curX >= t.cols {
		t.curX = 0
		t.newLine()
	}
}

func (t *Terminal) advanceToTabStop() {
	next := (t.curX/8 + 1) * 8
	for t.curX < next && t.curX < t.cols {
		t.setCell(' ')
		t.curX++
	}
	if t.curX >= t.cols {
		t.curX = t.cols - 1
	}
}

func (t *Terminal) newLine() {
	t.curX = 0
	t.curY++
	if t.curY >= t.rows {
		copy(t.grid, t.grid[t.cols:]) // scroll up one row
		last := (t.rows - 1) * t.cols
		for i := last; i < last+t.cols; i++ { // blank the new row
			t.grid[i] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
		t.curY = t.rows - 1
	}
	t.dirty = true
}

// applySGR handles the ANSI-lite subset: 0 reset, 1 bold(bright),
// 30-37 fg, 40-47 bg.
func (t *Terminal) applySGR(params string) {
	if params == "" {
		t.fg, t.bg = 7, 0
		return
	}
	parts := bytes.Split([]byte(params), []byte(";"))
	for _, p := range parts {
		n, err := strconv.Atoi(string(p))
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			t.fg, t.bg = 7, 0
		case n == 1:
			if t.fg < 8 {
				t.fg += 8
			}
		case n >= 30 && n <= 37:
			t.fg = uint8(n - 30)
		case n >= 40 && n <= 47:
			t.bg = uint8(n - 40)
		}
	}
}

// Flush renders dirty cells to the framebuffer pixels and issues one
// UPDATE_RECT covering the damage (or nothing when headless).
func (t *Terminal) Flush() {
	if !t.dirty || !t.fb.Attached() {
		return
	}
	pix := t.fb.Pixels()
	stride := int(t.fb.Width()) * 4
	for row := 0; row < t.rows; row++ {
		for col := 0; col < t.cols; col++ {
			cell := t.grid[row*t.cols+col]
			glyph := Glyph(byte(cell.ch))
			baseX, baseY := col*GlyphW, row*GlyphH
			for gy := 0; gy < 8; gy++ {
				rowBits := glyph[gy]
				for half := 0; half < 2; half++ { // double height
					y := baseY + gy*2 + half
					if y >= int(t.fb.Height()) {
						continue
					}
					rowOff := y*stride + baseX*4
					for gx := 0; gx < 8; gx++ {
						if baseX+gx >= int(t.fb.Width()) {
							break
						}
						var color uint32
						if rowBits&(1<<uint(7-gx)) != 0 {
							color = palette[cell.fg&15]
						} else {
							color = palette[cell.bg&15]
						}
						o := rowOff + gx*4
						pix[o] = byte(color)         // B
						pix[o+1] = byte(color >> 8)  // G
						pix[o+2] = byte(color >> 16) // R
						pix[o+3] = 0                 // X
					}
				}
			}
		}
	}
	w := minU32(uint32(t.cols*GlyphW), t.fb.Width())
	h := minU32(uint32(t.rows*GlyphH), t.fb.Height())
	t.fb.UpdateRect(0, 0, w, h)
	if t.fb.Caps()&FBCapDoubleBuffer != 0 {
		t.fb.Flip()
	}
	t.dirty = false
}

// Cursor returns the cursor position (tests).
func (t *Terminal) Cursor() (int, int) { return t.curX, t.curY }

// CellAt returns a grid cell (tests).
func (t *Terminal) CellAt(col, row int) (rune, uint8, uint8) {
	c := t.grid[row*t.cols+col]
	return c.ch, c.fg, c.bg
}

func minU32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
