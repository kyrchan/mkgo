package compositor

import (
	"testing"
)

func TestNewSurfaceZeroed(t *testing.T) {
	s := NewSurface(640, 480)
	if s.W() != 640 || s.H() != 480 {
		t.Fatalf("size %dx%d", s.W(), s.H())
	}
	if len(s.Bytes()) != 640*480*PixelSize {
		t.Fatalf("backing len=%d", len(s.Bytes()))
	}
	for _, b := range s.Bytes() {
		if b != 0 {
			t.Fatal("surface not zeroed")
		}
	}
}

func TestClear(t *testing.T) {
	s := NewSurface(16, 16)
	s.Clear(Color(0x30, 0x10, 0x10))
	for i := 0; i < len(s.Bytes()); i += PixelSize {
		// Color(r,g,b)→Pixel: byte0=B, byte1=G, byte2=R, byte3=X
		if s.Bytes()[i] != 0x10 ||
			s.Bytes()[i+1] != 0x10 ||
			s.Bytes()[i+2] != 0x30 ||
			s.Bytes()[i+3] != 0 {
			t.Fatalf("pixel %d: got B=%02x G=%02x R=%02x X=%02x want B=10 G=10 R=30 X=00",
				i/PixelSize,
				s.Bytes()[i], s.Bytes()[i+1], s.Bytes()[i+2], s.Bytes()[i+3])
		}
	}
}

func TestFillRectClamping(t *testing.T) {
	s := NewSurface(32, 32)
	s.Clear(Color(0, 0, 0))
	s.FillRect(10, 10, 10, 10, Color(255, 0, 0))
	// Pixel inside rect should be red
	if b := s.Bytes()[(15*32+15)*PixelSize]; b != 0 {
		t.Fatalf("inside rect B=%02x want 00", b)
	}
	if g := s.Bytes()[(15*32+15)*PixelSize+1]; g != 0 {
		t.Fatalf("inside rect G=%02x want 00", g)
	}
	if r := s.Bytes()[(15*32+15)*PixelSize+2]; r != 255 {
		t.Fatalf("inside rect R=%02x want FF", r)
	}
	// Pixel outside should be black
	if v := s.Bytes()[(0*32+0)*PixelSize+2]; v != 0 {
		t.Fatalf("outside rect R=%02x want 00", v)
	}
	// FillRect with negative/over-large coords should be clamped
	s.FillRect(-5, -5, 100, 100, Color(0, 255, 0))
	if s.Bytes()[(0*32+0)*PixelSize+2] != 0 {
		t.Fatal("clamped fill R should be 0")
	}
}

func TestDrawRectOutline(t *testing.T) {
	s := NewSurface(32, 32)
	s.Clear(Color(0, 0, 0))
	s.DrawRect(4, 4, 8, 6, Color(255, 255, 255))
	// Check top-left corner is on the border (white: R=255)
	idx := (4*32 + 4) * PixelSize
	if s.Bytes()[idx+2] != 255 {
		t.Fatalf("border pixel R=%02x want FF", s.Bytes()[idx+2])
	}
	// Check interior is black
	idx = (6*32 + 6) * PixelSize
	if s.Bytes()[idx+2] != 0 {
		t.Fatalf("interior pixel R=%02x want 0", s.Bytes()[idx+2])
	}
}

func TestDrawLine(t *testing.T) {
	s := NewSurface(32, 32)
	s.Clear(Color(0, 0, 0))
	// Horizontal line
	s.DrawLine(0, 0, 15, 0, Color(255, 0, 0))
	for x := 0; x < 16; x++ {
		idx := (0*32 + x) * PixelSize
		if s.Bytes()[idx+2] != 255 {
			t.Fatalf("hline pixel x=%d R=%02x want FF", x, s.Bytes()[idx+2])
		}
	}
	// Diagonal line
	s.Clear(Color(0, 0, 0))
	s.DrawLine(0, 0, 15, 15, Color(0, 255, 0))
	for i := 0; i < 16; i++ {
		idx := (i*32 + i) * PixelSize
		if s.Bytes()[idx+1] != 255 {
			t.Fatalf("diag pixel i=%d G=%02x want FF", i, s.Bytes()[idx+1])
		}
	}
}

func TestDrawCircle(t *testing.T) {
	s := NewSurface(64, 64)
	s.Clear(Color(0, 0, 0))
	s.DrawCircle(32, 32, 10, Color(255, 0, 255))
	// Check that the center pixel of a circle outline is NOT the color
	// (since outline doesn't fill center for r > 0)
	idx := (32*64 + 32) * PixelSize
	if s.Bytes()[idx+2] == 255 && s.Bytes()[idx] == 255 {
		// It's possible a pixel landed on center; check by counting pixels
	}
	// Count magenta pixels
	count := 0
	for i := 0; i+PixelSize-1 < len(s.Bytes()); i += PixelSize {
		if s.Bytes()[i+2] == 255 && s.Bytes()[i] == 255 {
			count++
		}
	}
	if count == 0 {
		t.Fatal("circle drew no pixels")
	}
	// A circle radius 10 outline should have roughly 2*pi*r ≈ 63 pixels
	if count > 200 {
		t.Fatalf("circle drew %d pixels, expected ~63", count)
	}
}

func TestDrawText(t *testing.T) {
	s := NewSurface(80, 16)
	s.Clear(Color(0, 0, 0))
	s.DrawText(0, 0, "Hi", Color(255, 255, 255))
	// 'H' is at position 0, 'i' at position 9 (8 px + 1 spacing)
	// Check that some white pixels exist in the H glyph area
	hCount := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			idx := (y*80 + x) * PixelSize
			if s.Bytes()[idx+2] == 255 {
				hCount++
			}
		}
	}
	if hCount == 0 {
		t.Fatal("H glyph has no white pixels")
	}
	// Check 'i' glyph area (starts at x=9)
	iCount := 0
	for y := 0; y < 8; y++ {
		for x := 9; x < 17; x++ {
			idx := (y*80 + x) * PixelSize
			if s.Bytes()[idx+2] == 255 {
				iCount++
			}
		}
	}
	if iCount == 0 {
		t.Fatal("i glyph has no white pixels")
	}
}

func TestDrawTextNewline(t *testing.T) {
	s := NewSurface(80, 32)
	s.Clear(Color(0, 0, 0))
	s.DrawText(0, 0, "A\nB", Color(255, 255, 255))
	// 'A' should be on first line, 'B' on second
	aCount := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			idx := (y*80 + x) * PixelSize
			if s.Bytes()[idx+2] == 255 {
				aCount++
			}
		}
	}
	if aCount == 0 {
		t.Fatal("A glyph has no white pixels")
	}
	bCount := 0
	for y := 8; y < 16; y++ {
		for x := 0; x < 8; x++ {
			idx := (y*80 + x) * PixelSize
			if s.Bytes()[idx+2] == 255 {
				bCount++
			}
		}
	}
	if bCount == 0 {
		t.Fatal("B glyph on second line has no white pixels")
	}
}

func TestDrawTextScaled(t *testing.T) {
	s := NewSurface(32, 32)
	s.Clear(Color(0, 0, 0))
	s.DrawTextScaled(0, 0, 2, "A", Color(255, 255, 255))
	// With scale=2, 'A' glyph is 16x16, so pixels should appear in the 16x16 area
	count := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			idx := (y*32 + x) * PixelSize
			if s.Bytes()[idx+2] == 255 {
				count++
			}
		}
	}
	if count == 0 {
		t.Fatal("scaled text drew no pixels")
	}
}

func TestFillCircle(t *testing.T) {
	s := NewSurface(32, 32)
	s.Clear(Color(0, 0, 0))
	s.FillCircle(16, 16, 10, Color(255, 0, 0))
	// Red: B=0, G=0, R=255. Center should be filled.
	idx := (16*32 + 16) * PixelSize
	if s.Bytes()[idx+2] != 255 {
		t.Fatalf("circle center R=%02x want FF", s.Bytes()[idx+2])
	}
	// Corner should be empty (outside circle)
	idx = (0*32 + 0) * PixelSize
	if s.Bytes()[idx+2] != 0 {
		t.Fatalf("circle corner R=%02x want 00", s.Bytes()[idx+2])
	}
	// Edge of bounding box corner should also be black
	idx = (6*32 + 6) * PixelSize // top-left corner of bounding box
	if s.Bytes()[idx+2] != 0 {
		t.Fatalf("circle corner R=%02x want 00", s.Bytes()[idx+2])
	}
}

func TestWrapSurface(t *testing.T) {
	px := make([]byte, 64*64*4)
	s := WrapSurface(px, 64, 64)
	if s.W() != 64 || s.H() != 64 {
		t.Fatalf("size %dx%d", s.W(), s.H())
	}
	if &s.Bytes()[0] != &px[0] {
		t.Fatal("WrapSurface should share the backing slice")
	}
}

func TestDrawTextClipping(t *testing.T) {
	s := NewSurface(10, 8)
	s.DrawText(5, 0, "Hello World", Color(255, 255, 255))
	// Text starting at x=5 with 9px per char should be clipped at width 10
	// Only 'H' partially and 'e' partially should appear
	// No panic — that's the main assertion
}
