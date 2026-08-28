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
	// unsupported finals are swallowed silently; X lands inline
	tr.WriteString("\x1b[2J\x1b[5;10HX")
	cx, cy := tr.Cursor()
	if ch, _, _ := tr.CellAt(cx-1, cy); ch != 'X' {
		t.Fatalf("unknown CSI handling wrong; cursor %d,%d", cx, cy)
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
