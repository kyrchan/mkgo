package terminal

// Terminal: a text-mode console on the §9.FB framebuffer.
//
// Cell grid of cols×rows (8×16 glyph cells by default), ANSI-lite SGR
// parsing (\x1b[0m reset, 30-37 fg, 40-47 bg, 1 bright, \n \r \t
// backspace), scroll-on-overflow, and damage-tracked rendering into the
// window's pixel area followed by UPDATE_RECT.
//
// DEC private-mode support: ?25l/?25h cursor show/hide, ?1049h/l
// alternate screen buffer, and DECSTBM scrolling region (r).

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
	decPm  bool // DEC private mode (CSI ? ...)
	csiBuf []byte
	osc    bool
	oscBuf []byte // OSC string buffer

	// cursor visibility and blinking
	cursorVisible bool
	cursorBlink   bool
	cursorOn      bool
	cursorTicker  int

	// saved cursor position (DECSC / DECRC)
	savedX, savedY int

	// alternate screen buffer (DECSET 1049)
	altScreen  bool
	altGrid    []cell
	altX, altY int

	// scrolling region (DECSTBM), 0-based inclusive
	scrollTop    int
	scrollBottom int

	// DEC private modes
	bracketedPaste bool
	focusReporting bool
	mouseMode      int // 0=none, 1000=press, 1002=motion, 1006=SGR

	// autowrap (?7) and application cursor keys (?1)
	autoWrap      bool
	appCursorKeys bool

	// window title (OSC 0;title BEL / OSC 2;title BEL)
	title string
}

// NewTerminal sizes the grid to the window (or defaults) and clears it.
func New(fb *FBWindow) *Terminal {
	t := &Terminal{fb: fb, fg: 7, bg: 0}
	t.resize()
	t.resetInitialState()
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
	if t.altGrid == nil {
		t.altGrid = make([]cell, w*h)
	}
	t.altX, t.altY = 0, 0
	t.scrollTop = 0
	t.scrollBottom = t.rows - 1
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
			t.decPm = false
			t.csiBuf = t.csiBuf[:0]
			return
		}
		if c == ']' {
			t.osc = true
			t.oscBuf = t.oscBuf[:0]
			return
		}
		if c == '(' || c == ')' {
			// charset selection — ignored
			return
		}
		return // other escapes unsupported (ignored)
	case t.csi:
		if c >= '0' && c <= '9' || c == ';' || c == '?' {
			if c == '?' && len(t.csiBuf) == 0 {
				t.decPm = true
			} else {
				t.csiBuf = append(t.csiBuf, c)
			}
			return
		}
		t.csi = false
		if c == 'm' {
			t.applySGR(string(t.csiBuf))
			return
		}
		if t.decPm {
			t.applyDECPrivate(c)
			return
		}
		parts := bytes.Split(t.csiBuf, []byte(";"))
		get := func(i int, def int) int {
			if i < len(parts) && len(parts[i]) > 0 {
				n, _ := strconv.Atoi(string(parts[i]))
				return n
			}
			return def
		}
		switch c {
		case 'J':
			t.eraseDisplay(get(0, 0))
		case 'K':
			t.eraseLine(get(0, 0))
		case 'H':
			t.curY = get(0, 1) - 1
			t.curX = get(1, 1) - 1
			if t.curY < 0 {
				t.curY = 0
			}
			if t.curX < 0 {
				t.curX = 0
			}
			t.clampPos()
		case 'A':
			t.moveCursor(0, -get(0, 1))
		case 'B':
			t.moveCursor(0, get(0, 1))
		case 'C':
			t.moveCursor(get(0, 1), 0)
		case 'D':
			t.moveCursor(-get(0, 1), 0)
		case 's':
			t.saveCursor()
		case 'u':
			t.restoreCursor()
		case 'n':
			if get(0, 0) == 6 {
				t.reportDeviceStatus()
			}
		case 'r':
			top := get(0, 0)
			bottom := get(1, 0)
			if top <= 0 {
				top = 1
			}
			if bottom <= 0 {
				bottom = t.rows
			}
			if top > bottom {
				top = 1
				bottom = t.rows
			}
			t.scrollTop = top - 1
			t.scrollBottom = bottom - 1
		case 'X':
			t.eraseChars(get(0, 0))
		}
		return
	case t.osc:
		if c == '\x07' || c > 127 {
			t.applyOSC(string(t.oscBuf))
			t.osc = false
			t.oscBuf = t.oscBuf[:0]
			return
		}
		if c == '\x1b' {
			if len(t.oscBuf) > 0 && t.oscBuf[len(t.oscBuf)-1] == '\x1b' {
				return
			}
			t.oscBuf = append(t.oscBuf, c)
			return
		}
		if len(t.oscBuf) > 0 && t.oscBuf[len(t.oscBuf)-1] == '\x1b' {
			if c == '\\' {
				t.applyOSC(string(t.oscBuf[:len(t.oscBuf)-1]))
				t.osc = false
				t.oscBuf = t.oscBuf[:0]
				return
			}
			t.oscBuf = t.oscBuf[:len(t.oscBuf)-1]
		}
		t.oscBuf = append(t.oscBuf, c)
		return
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
		if t.autoWrap {
			t.curX = 0
			t.newLine()
		} else {
			t.curX = t.cols - 1
		}
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

func (t *Terminal) clampPos() {
	if t.curX < 0 {
		t.curX = 0
	} else if t.curX >= t.cols {
		t.curX = t.cols - 1
	}
	if t.curY < 0 {
		t.curY = 0
	} else if t.curY >= t.rows {
		t.curY = t.rows - 1
	}
}

func (t *Terminal) moveCursor(dx, dy int) {
	t.curX += dx
	t.curY += dy
	t.clampPos()
}

func (t *Terminal) scrollUp() {
	if t.scrollTop <= 0 && t.scrollBottom >= t.rows-1 {
		copy(t.grid, t.grid[t.cols:])
		last := (t.rows - 1) * t.cols
		for i := last; i < last+t.cols; i++ {
			t.grid[i] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
		t.curY = t.rows - 1
		return
	}
	// region scroll
	if t.curY >= t.scrollBottom {
		copy(t.grid[t.scrollTop*t.cols:], t.grid[(t.scrollTop+1)*t.cols:(t.scrollBottom+1)*t.cols])
		last := t.scrollBottom * t.cols
		for i := last; i < last+t.cols; i++ {
			t.grid[i] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
		t.curY = t.scrollBottom
	}
}

func (t *Terminal) eraseDisplay(n int) {
	switch n {
	case 0:
		for y := t.curY; y < t.rows; y++ {
			start := 0
			if y == t.curY {
				start = t.curX
			}
			for x := start; x < t.cols; x++ {
				t.grid[y*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
			}
		}
	case 1:
		for y := 0; y <= t.curY; y++ {
			start := 0
			if y == t.curY {
				start = t.curX
			}
			end := t.cols
			if y == t.curY {
				end = t.cols
			}
			for x := start; x < end; x++ {
				t.grid[y*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
			}
		}
	case 2:
		for i := range t.grid {
			t.grid[i] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
	}
	t.curX, t.curY = 0, 0
	t.dirty = true
}

func (t *Terminal) eraseLine(n int) {
	switch n {
	case 0:
		for x := t.curX; x < t.cols; x++ {
			t.grid[t.curY*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
	case 1:
		for x := 0; x <= t.curX && x < t.cols; x++ {
			t.grid[t.curY*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
	case 2:
		for x := 0; x < t.cols; x++ {
			t.grid[t.curY*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
		}
	}
	t.dirty = true
}

func (t *Terminal) eraseChars(n int) {
	if n <= 0 {
		n = 1
	}
	if t.curX >= t.cols {
		return
	}
	end := t.curX + n
	if end > t.cols {
		end = t.cols
	}
	for x := t.curX; x < end; x++ {
		t.grid[t.curY*t.cols+x] = cell{ch: ' ', fg: t.fg, bg: t.bg}
	}
	t.dirty = true
}

func (t *Terminal) reportDeviceStatus() {
	// DSR is an out-of-band response; the terminal surface here only
	// consumes input, so we silently drop the reply.
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

func (t *Terminal) resetInitialState() {
	t.fg, t.bg = 7, 0
	t.curX, t.curY = 0, 0
	t.savedX, t.savedY = 0, 0
	t.cursorVisible = true
	t.cursorBlink = false
	t.cursorOn = true
	t.altScreen = false
	t.scrollTop = 0
	t.scrollBottom = t.rows - 1
	t.bracketedPaste = false
	t.focusReporting = false
	t.mouseMode = 0
	t.autoWrap = true
	t.appCursorKeys = false
	t.title = ""
	t.esc = false
	t.csi = false
	t.decPm = false
	t.osc = false
	t.csiBuf = t.csiBuf[:0]
	t.oscBuf = t.oscBuf[:0]
	t.Clear()
}

func (t *Terminal) saveCursor() {
	t.savedX, t.savedY = t.curX, t.curY
}

func (t *Terminal) restoreCursor() {
	t.curX, t.curY = t.savedX, t.savedY
	t.clampPos()
}

// ShowCursor enables the cursor. If blink is true the cursor toggles
// between a solid block and the cell's foreground colour on Tick.
func (t *Terminal) ShowCursor(blink bool) {
	t.cursorVisible = true
	t.cursorBlink = blink
	t.cursorOn = true
	t.dirty = true
}

// HideCursor disables the cursor.
func (t *Terminal) HideCursor() {
	t.cursorVisible = false
	t.cursorOn = false
	t.dirty = true
}

// Tick advances blink state when blink mode is on. Call at ~1 Hz.
func (t *Terminal) Tick() {
	if !t.cursorBlink || !t.cursorVisible {
		return
	}
	t.cursorTicker++
	if t.cursorTicker >= 2 {
		t.cursorTicker = 0
		t.cursorOn = !t.cursorOn
		t.dirty = true
	}
}

func (t *Terminal) applyDECPrivate(final byte) {
	switch final {
	case 'l':
		t.applyDECSet(false)
	case 'h':
		t.applyDECSet(true)
	}
}

func (t *Terminal) applyDECSet(set bool) {
	parts := bytes.Split(t.csiBuf, []byte(";"))
	for _, p := range parts {
		n, _ := strconv.Atoi(string(p))
		switch n {
		case 25:
			if set {
				t.ShowCursor(t.cursorBlink)
			} else {
				t.HideCursor()
			}
		case 1049:
			if set {
				t.switchToAltScreen()
			} else {
				t.switchToNormalScreen()
			}
		case 1:
			t.appCursorKeys = set
		case 7:
			t.autoWrap = set
		case 2004:
			t.bracketedPaste = set
		case 1004:
			t.focusReporting = set
		case 1000:
			if set {
				if t.mouseMode == 0 {
					t.mouseMode = 1000
				}
			} else {
				t.mouseMode = 0
			}
		case 1002:
			if set {
				if t.mouseMode < 1002 {
					t.mouseMode = 1002
				}
			} else {
				if t.mouseMode == 1002 {
					t.mouseMode = 1000
				} else if t.mouseMode == 1006 {
					t.mouseMode = 1006 // SGR implies motion, keep highest
				}
			}
		case 1006:
			if set {
				if t.mouseMode < 1006 {
					t.mouseMode = 1006
				}
			} else {
				if t.mouseMode == 1006 {
					t.mouseMode = t.mouseMode // SGR off while others active? keep current
				}
			}
		}
	}
}

func (t *Terminal) applyOSC(osc string) {
	if len(osc) == 0 {
		return
	}
	// OSC Ps ; Pt BEL / ESC \
	parts := bytes.SplitN([]byte(osc), []byte(";"), 2)
	if len(parts) == 0 {
		return
	}
	// first byte is the OSC number (0, 1, 2, etc.)
	// remaining is the payload
	payload := ""
	if len(parts) == 2 {
		payload = string(parts[1])
	}
	switch string(parts[0]) {
	case "0", "2":
		t.title = payload
	}
}

func (t *Terminal) switchToAltScreen() {
	if t.altScreen {
		return
	}
	t.altScreen = true
	// save current screen into alt buffer
	copy(t.altGrid, t.grid)
	t.altX, t.altY = t.curX, t.curY
	// clear main screen
	t.Clear()
	t.curX, t.curY = 0, 0
	t.scrollTop = 0
	t.scrollBottom = t.rows - 1
	t.dirty = true
}

func (t *Terminal) switchToNormalScreen() {
	if !t.altScreen {
		return
	}
	t.altScreen = false
	// restore main screen from alt buffer
	copy(t.grid, t.altGrid)
	t.curX, t.curY = t.altX, t.altY
	t.scrollTop = 0
	t.scrollBottom = t.rows - 1
	t.dirty = true
}

func (t *Terminal) newLine() {
	t.curX = 0
	if t.curY >= t.scrollBottom {
		t.scrollUp()
	} else {
		t.curY++
	}
	t.dirty = true
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
	if t.cursorVisible && t.cursorOn && t.fb.Attached() {
		x := t.curX
		y := t.curY
		if x >= 0 && x < t.cols && y >= 0 && y < t.rows {
			cx := x * GlyphW
			cy := y * GlyphH
			for gy := 0; gy < GlyphH; gy++ {
				py := cy + gy
				if py >= int(t.fb.Height()) {
					continue
				}
				rowOff := py*stride + cx*4
				for gx := 0; gx < GlyphW; gx++ {
					if cx+gx >= int(t.fb.Width()) {
						break
					}
					o := rowOff + gx*4
					pix[o] = 0xFF
					pix[o+1] = 0xFF
					pix[o+2] = 0xFF
					pix[o+3] = 0
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

// BracketedPaste reports whether bracketed paste mode is enabled.
func (t *Terminal) BracketedPaste() bool { return t.bracketedPaste }

// FocusReporting reports whether focus in/out reporting is enabled.
func (t *Terminal) FocusReporting() bool { return t.focusReporting }

// Title returns the current window title set by OSC 0/2.
func (t *Terminal) Title() string { return t.title }

// MouseMode returns the active mouse reporting mode: 0=none, 1000=press,
// 1002=press+motion, 1006=SGR extended.
func (t *Terminal) MouseMode() int { return t.mouseMode }

// AutoWrap reports whether autowrap mode is enabled (DECAWM).
func (t *Terminal) AutoWrap() bool { return t.autoWrap }

// AppCursorKeys reports whether application cursor keys mode is enabled (DECCKM).
func (t *Terminal) AppCursorKeys() bool { return t.appCursorKeys }

// FocusChanged returns the escape sequence to emit when focus changes.
// focused=true → focus in, focused=false → focus out.
func (t *Terminal) FocusChanged(focused bool) []byte {
	if !t.focusReporting {
		return nil
	}
	if focused {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

// EncodeMouseEvent encodes a mouse event into an escape sequence when
// mouse reporting is active. Returns nil when reporting is off.
// event: 0=press, 1=release, 2=motion
// button: 0=left, 1=middle, 2=right, 3=release, 4=scroll up, 5=scroll down
// modifiers: bit 0=shift, bit 1=alt, bit 2=ctrl
func (t *Terminal) EncodeMouseEvent(x, y int, event, button, modifiers int) []byte {
	if t.mouseMode == 0 {
		return nil
	}
	if x < 0 {
		x = 0
	} else if x >= t.cols {
		x = t.cols - 1
	}
	if y < 0 {
		y = 0
	} else if y >= t.rows {
		y = t.rows - 1
	}
	cb := button
	if event == 2 && t.mouseMode == 1000 {
		return nil
	}
	if event == 2 {
		cb = 32
	}
	if t.mouseMode == 1006 {
		suffix := "M"
		if event == 1 || (event == 0 && cb >= 3) {
			suffix = "m"
		}
		m := modifiers
		if event == 2 {
			m |= 32
		}
		return []byte("\x1b[<" + strconv.Itoa(cb) + ";" + strconv.Itoa(x+1) + ";" + strconv.Itoa(y+1) + ";" + strconv.Itoa(m) + suffix)
	}
	cb += 32
	if event == 1 || (event == 0 && cb >= 3) {
		cb += 3
	}
	if modifiers != 0 {
		cb |= 4
		if modifiers&2 != 0 {
			cb |= 8
		}
		if modifiers&4 != 0 {
			cb |= 16
		}
	}
	cx := x + 32 + 1
	cy := y + 32 + 1
	return []byte{0x1b, '[', 'M', byte(cb), byte(cx), byte(cy)}
}

func minU32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}
