package terminal

import (
	"bytes"
	"testing"
)

func TestFBWindowHeaderAndHeadless(t *testing.T) {
	ff, w, err := NewFake(320, 200, FBCapDamageRects)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Attached() || w.Width() != 320 || w.Height() != 200 {
		t.Fatalf("geometry %dx%d", w.Width(), w.Height())
	}
	if w.Caps()&FBCapDamageRects == 0 {
		t.Fatal("caps lost")
	}

	// growing beyond the fixed window budget is rejected (-1);
	// shrinking/within-bounds succeeds
	if st := w.SetMode(640, 400); st != -1 {
		t.Fatalf("oversize setmode st=%d want -1", st)
	}
	if st := w.SetMode(320, 100); st != 0 {
		t.Fatalf("in-bounds setmode st=%d", st)
	}
	ff.Mu.Lock()
	mode := ff.Mode
	ff.Mu.Unlock()
	if mode[0] != 320 || mode[1] != 100 || mode[2] != FBBpp {
		t.Fatalf("backend mode %+v", mode)
	}

	if st := w.UpdateRect(1, 2, 3, 4); st != 0 {
		t.Fatal("updaterect failed")
	}
	ff.Mu.Lock()
	last := ff.Rects[len(ff.Rects)-1]
	ff.Mu.Unlock()
	if last[0] != 1 || last[1] != 2 || last[2] != 3 {
		t.Fatalf("rect %+v", last)
	}

	if _, err := NewFBWindow(make([]byte, 16)); err == nil {
		t.Fatal("bogus window accepted")
	}
	// wrong magic rejected
	bad := make([]byte, fbPixBase+64)
	if _, err := NewFBWindow(bad); err == nil {
		t.Fatal("bad magic accepted")
	}
}

func TestFBHeadlessNoOps(t *testing.T) {
	_, w, err := NewFake(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if w.Attached() {
		t.Fatal("headless reported attached")
	}
	if st := w.SetMode(1, 1); st != 0 {
		t.Fatalf("headless setmode st=%d (want silent no-op)", st)
	}
	if pix := w.Pixels(); pix != nil {
		t.Fatal("headless returned pixels")
	}
}

func TestTerminalTextAndScroll(t *testing.T) {
	_, fb, err := NewFake(DefaultCols*GlyphW, DefaultRows*GlyphH, FBCapDamageRects)
	if err != nil {
		t.Fatal(err)
	}
	tr := New(fb)

	tr.WriteString("HELLO")
	ch, fg, bg := tr.CellAt(0, 0)
	if ch != 'H' || fg != 7 || bg != 0 {
		t.Fatalf("cell=%q %d %d", ch, fg, bg)
	}
	x, y := tr.Cursor()
	if x != 5 || y != 0 {
		t.Fatalf("cursor %d,%d", x, y)
	}

	// fill the screen; scrolling keeps the LAST row as the newest text
	for r := 0; r < DefaultRows+3; r++ {
		tr.WriteString("row\r\n") // \r\n so each line starts at col 0
	}
	ch, _, _ = tr.CellAt(0, DefaultRows-2) // last completed line
	if ch != 'r' {
		t.Fatalf("scroll content ch=%q", ch)
	}
	_, cy := tr.Cursor()
	if cy != DefaultRows-1 {
		t.Fatalf("cursor row %d", cy)
	}
	// the very first "HELLO" must have scrolled away
	if ch, _, _ = tr.CellAt(0, 0); ch == 'H' && tr.grid[1].ch == 'E' {
		t.Fatal("no scroll happened")
	}
}

func TestTerminalANSI(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)

	tr.WriteString("\x1b[31;44mRED") // red on blue
	ch, fg, bg := tr.CellAt(0, 0)
	if ch != 'R' || fg != 1 || bg != 4 {
		t.Fatalf("ansi cell=%q fg=%d bg=%d", ch, fg, bg)
	}
	tr.WriteString("\x1b[1m!") // bold → bright variant of current fg
	if _, fg, _ = tr.CellAt(3, 0); fg != 9 {
		t.Fatalf("bold fg=%d want 9", fg)
	}
	tr.WriteString("\x1b[0mplain")
	if _, fg, bg = tr.CellAt(8, 0); fg != 7 || bg != 0 {
		t.Fatalf("reset fg=%d bg=%d", fg, bg)
	}

	// cursor movement
	tr.WriteString("\x1b[10;20HX")
	cx, cy := tr.Cursor()
	if cx != 20 || cy != 9 {
		t.Fatalf("cursor move cx=%d cy=%d want 20,9", cx, cy)
	}
	ch, _, _ = tr.CellAt(19, 9)
	if ch != 'X' {
		t.Fatalf("cursor cell ch=%q want X", ch)
	}

	// relative movement
	tr.WriteString("\x1b[3B\x1b[2C\x1b[A\x1b[D")
	cx, cy = tr.Cursor()
	if cx != 21 || cy != 11 {
		t.Fatalf("relative move cx=%d cy=%d want 21,11", cx, cy)
	}

	// erase display/line
	tr.WriteString("\x1b[2J\x1b[H") // full clear + home
	cx, cy = tr.Cursor()
	if cx != 0 || cy != 0 {
		t.Fatalf("RIS cursor cx=%d cy=%d want 0,0", cx, cy)
	}

	// save/restore cursor
	tr.WriteString("\x1b[10;5H") // move to row 10, col 5 (0-based: 9,4)
	tr.WriteString("\x1b[s")     // save
	tr.WriteString("\x1b[3;8H")  // move to row 3, col 8 (0-based: 2,7)
	tr.WriteString("\x1b[u")     // restore to 9,4
	cx, cy = tr.Cursor()
	if cx != 4 || cy != 9 {
		t.Fatalf("save/restore cx=%d cy=%d want 4,9", cx, cy)
	}

	// erase line variants
	tr.WriteString("\x1b[2K")         // erase whole line
	tr.WriteString("\x1b[1;3H\x1b[K") // erase to end of line from col 1
	if ch, _, _ := tr.CellAt(3, 0); ch != ' ' {
		t.Fatalf("erase-line ch=%q want ' '", ch)
	}

	// unknown finals are swallowed; write an X at the home position
	// after the clear so the DSR reply has a nearby glyph to verify.
	tr.WriteString("\x1b[HX\x1b[6n") // home, X, DSR — silent
	cx, cy = tr.Cursor()
	if cx != 1 || cy != 0 {
		t.Fatalf("post-DSR cursor cx=%d cy=%d want 1,0", cx, cy)
	}
	if ch, _, _ := tr.CellAt(0, 0); ch != 'X' {
		t.Fatalf("unknown CSI handling wrong; cursor %d,%d", cx, cy)
	}

	// DEC private mode: hide/show cursor
	tr.HideCursor()
	tr.ShowCursor(false)
	tr.ShowCursor(true)
	tr.Tick()
	tr.Tick()
	tr.Tick()

	// DEC private mode: alternate screen buffer restores on exit
	tr.WriteString("\x1b[?1049h") // enter alt screen
	tr.WriteString("\x1b[2J\x1b[H")
	tr.WriteString("ALT") // on alt screen
	tr.Flush()
	tr.WriteString("\x1b[?1049l") // leave alt screen
	tr.Flush()
	// main screen is restored to its pre-alt content ('X' at 0,0)
	ch, _, _ = tr.CellAt(0, 0)
	if ch != 'X' {
		t.Fatalf("alt-screen exit ch=%q want 'X'", ch)
	}

	// DECSTBM scrolling region
	tr.WriteString("\x1b[2;4r") // rows 2-4 (0-based: 1-3)
	tr.WriteString("\x1b[H")    // home
	if tr.scrollTop != 1 || tr.scrollBottom != 3 {
		t.Fatalf("scroll region top=%d bottom=%d want 1,3", tr.scrollTop, tr.scrollBottom)
	}
	tr.WriteString("ABCDEFG")
	cx, cy = tr.Cursor()
	if cx != 7 || cy != 0 {
		t.Fatalf("region write cursor cx=%d cy=%d want 7,0", cx, cy)
	}
	// reset region
	tr.WriteString("\x1b[r")
	if tr.scrollTop != 0 || tr.scrollBottom != tr.rows-1 {
		t.Fatalf("scroll reset top=%d bottom=%d want 0,%d", tr.scrollTop, tr.scrollBottom, tr.rows-1)
	}
}

func TestTerminalRendersPixels(t *testing.T) {
	const W = int(DefaultCols * GlyphW)
	const H = int(DefaultRows * GlyphH)
	ff, fb, _ := NewFake(uint32(W), uint32(H), FBCapDamageRects)
	tr := New(fb)

	// white background via SGR then a glyph; check exact pixels
	tr.WriteString("\x1b[47m ")
	tr.Flush()
	fbPix := fb.Pixels()
	if fbPix == nil {
		t.Fatal("no pixels exposed")
	}
	// pixel at (0,0): bg color 7 = light gray BGR
	wantBG := []byte{170, 170, 170, 0}
	if !bytes.Equal(fbPix[0:4], wantBG) {
		t.Fatalf("bg pixel %x want %x", fbPix[0:4], wantBG)
	}

	tr.WriteString("\x1b[0m")
	tr.WriteString("A")
	tr.Flush()

	// 'A' sits at column 1 (after the bg space); row0=0x7E, row1=0x11.
	// Default fg is EGA-7 (light gray, R=170); bg black.
	stride := W * 4
	baseX := GlyphW
	lit, unlit := 0, 0
	for gx := 0; gx < 8; gx++ {
		o := stride*2 + (baseX+gx)*4 + 2 // R channel at y=2 (row1 doubled)
		if 0x11&(1<<uint(7-gx)) != 0 {
			if fbPix[o] == 170 {
				lit++
			}
		} else if fbPix[o] == 0 {
			unlit++
		}
	}
	if lit != 2 || unlit != 6 {
		t.Fatalf("'A' row1 lit=%d unlit=%d want 2/6", lit, unlit)
	}

	// damage rect issued covering the full grid
	ff.Mu.Lock()
	rects := len(ff.Rects)
	ff.Mu.Unlock()
	if rects == 0 {
		t.Fatal("no UPDATE_RECT issued")
	}
	if st := fb.Flip(); st != 0 {
		t.Fatal("flip failed")
	}
	ff.Mu.Lock()
	if ff.Flips == 0 {
		ff.Mu.Unlock()
		t.Fatal("flip not counted")
	}
	ff.Mu.Unlock()
}

func TestTerminalCursorBlink(t *testing.T) {
	const W = 80 * GlyphW
	const H = 25 * GlyphH
	_, fb, _ := NewFake(W, H, 0)
	tr := New(fb)
	tr.ShowCursor(true)
	tr.WriteString("\x1b[47m \x1b[32m@\x1b[47m ") // white bg, green @, space
	tr.Flush()

	pix := fb.Pixels()
	stride := int(fb.Width()) * 4
	// cursor is at column 3 after the trailing space
	cursorOff := stride*0 + 3*GlyphW*4
	checkCursor := func(wantOn bool) {
		on := pix[cursorOff] == 0xFF && pix[cursorOff+1] == 0xFF && pix[cursorOff+2] == 0xFF
		if on != wantOn {
			t.Fatalf("cursor on=%v want %v at cursorOff=0x%x pix=0x%x 0x%x 0x%x",
				on, wantOn, cursorOff, pix[cursorOff], pix[cursorOff+1], pix[cursorOff+2])
		}
	}
	checkCursor(true)

	// two ticks toggles blink state
	tr.Tick()
	tr.Tick()
	tr.Flush()
	checkCursor(false)

	tr.Tick()
	tr.Tick()
	tr.Flush()
	checkCursor(true)

	// hide cursor stops rendering regardless of blink state
	tr.HideCursor()
	tr.Tick()
	tr.Tick()
	tr.Flush()
	checkCursor(false)
}

func TestTerminalAlternateScreen(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)

	tr.WriteString("MAIN")
	tr.Flush()
	if ch, _, _ := tr.CellAt(0, 0); ch != 'M' {
		t.Fatalf("main screen ch=%q want M", ch)
	}

	tr.WriteString("\x1b[?1049h") // enter alt screen
	tr.WriteString("\x1b[2J\x1b[H")
	tr.WriteString("ALT")
	tr.Flush()
	if ch, _, _ := tr.CellAt(0, 0); ch != 'A' {
		t.Fatalf("alt screen ch=%q want A", ch)
	}

	tr.WriteString("\x1b[?1049l") // leave alt screen
	tr.Flush()
	if ch, _, _ := tr.CellAt(0, 0); ch != 'M' {
		t.Fatalf("restored main screen ch=%q want M", ch)
	}
}

func TestTerminalBracketedPaste(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if tr.BracketedPaste() {
		t.Fatal("bracketed paste initially on")
	}
	tr.WriteString("\x1b[?2004h")
	if !tr.BracketedPaste() {
		t.Fatal("bracketed paste not enabled")
	}
	tr.WriteString("\x1b[?2004l")
	if tr.BracketedPaste() {
		t.Fatal("bracketed paste not disabled")
	}
}

func TestTerminalFocusReporting(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if tr.FocusReporting() {
		t.Fatal("focus reporting initially on")
	}
	tr.WriteString("\x1b[?1004h")
	if !tr.FocusReporting() {
		t.Fatal("focus reporting not enabled")
	}
	tr.WriteString("\x1b[?1004l")
	if tr.FocusReporting() {
		t.Fatal("focus reporting not disabled")
	}
}

func TestTerminalWindowTitle(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if tr.Title() != "" {
		t.Fatalf("initial title=%q", tr.Title())
	}
	tr.WriteString("\x1b]0;kernel-lane\x07")
	if tr.Title() != "kernel-lane" {
		t.Fatalf("title=%q want kernel-lane", tr.Title())
	}
	tr.WriteString("\x1b]0;\x07") // empty title clears
	if tr.Title() != "" {
		t.Fatalf("cleared title=%q", tr.Title())
	}
}

func TestTerminalMouseReporting(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if tr.MouseMode() != 0 {
		t.Fatalf("initial mouse mode=%d", tr.MouseMode())
	}
	tr.WriteString("\x1b[?1000h") // button press/release
	if tr.MouseMode() != 1000 {
		t.Fatalf("mouse mode=%d want 1000", tr.MouseMode())
	}
	tr.WriteString("\x1b[?1002h") // motion tracking
	if tr.MouseMode() != 1002 {
		t.Fatalf("mouse mode=%d want 1002", tr.MouseMode())
	}
	tr.WriteString("\x1b[?1006h") // SGR extended
	if tr.MouseMode() != 1006 {
		t.Fatalf("mouse mode=%d want 1006", tr.MouseMode())
	}
	// disable
	tr.WriteString("\x1b[?1006l")
	tr.WriteString("\x1b[?1002l")
	tr.WriteString("\x1b[?1000l")
	if tr.MouseMode() != 0 {
		t.Fatalf("mouse mode=%d want 0", tr.MouseMode())
	}
}

func TestTerminalAutowrap(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if !tr.AutoWrap() {
		t.Fatal("autowrap initially off")
	}
	tr.WriteString("\x1b[?7l")
	if tr.AutoWrap() {
		t.Fatal("autowrap not disabled")
	}
	tr.WriteString("\x1b[?7h")
	if !tr.AutoWrap() {
		t.Fatal("autowrap not re-enabled")
	}
	// default is on
	_, fb2, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr2 := New(fb2)
	if !tr2.AutoWrap() {
		t.Fatal("default autowrap should be on")
	}
}

func TestTerminalAppCursorKeys(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	if tr.AppCursorKeys() {
		t.Fatal("app cursor keys initially on")
	}
	tr.WriteString("\x1b[?1h")
	if !tr.AppCursorKeys() {
		t.Fatal("app cursor keys not enabled")
	}
	tr.WriteString("\x1b[?1l")
	if tr.AppCursorKeys() {
		t.Fatal("app cursor keys not disabled")
	}
}

func TestTerminalEraseChars(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	tr.WriteString("ABCDEFG")
	tr.Flush()
	tr.WriteString("\x1b[3D") // move back 3 (from pos 7 to pos 4)
	tr.WriteString("\x1b[3X") // erase 3 chars starting at pos 4
	ch, _, _ := tr.CellAt(4, 0)
	if ch != ' ' {
		t.Fatalf("erase char ch=%q want ' ' at pos 4", ch)
	}
	ch, _, _ = tr.CellAt(6, 0)
	if ch != ' ' {
		t.Fatalf("erase char ch=%q want ' ' at pos 6", ch)
	}
	ch, _, _ = tr.CellAt(3, 0)
	if ch != 'D' {
		t.Fatalf("preserved ch=%q want D at pos 3", ch)
	}
}

func TestTerminalFocusChanged(t *testing.T) {
	_, fb, _ := NewFake(80*GlyphW, 25*GlyphH, 0)
	tr := New(fb)
	tr.WriteString("\x1b[?1004h") // enable focus reporting
	out := tr.FocusChanged(true)
	if len(out) == 0 {
		t.Fatal("expected focus in sequence")
	}
	out = tr.FocusChanged(false)
	if len(out) == 0 {
		t.Fatal("expected focus out sequence")
	}
}
