// Package compositor implements a small software framebuffer compositor
// for graphics.wasm. It is a pure-Go package with no kernel or unsafe
// dependency so that every primitive can be exercised under `go test`
// before the module is linked against the kernel imports.
//
// Pixel format: 32-bit XRGB, stored little-endian in memory as
// B, G, R, X (byte 3 is reserved, set to 0). A Pixel value holds
// R in bits [23:16], G in [15:8], B in [7:0].
package compositor

// BPP is bits per pixel.
const BPP = 32

// PixelSize is bytes per pixel.
const PixelSize = 4

// Pixel holds one 32-bit XRGB pixel. The high byte is ignored (X).
type Pixel uint32

// Color packs R, G, B into a Pixel (X = 0).
func Color(r, g, b uint8) Pixel {
	return Pixel(uint32(r)<<16 | uint32(g)<<8 | uint32(b))
}

// BytesPerPixel returns PixelSize (4).
func BytesPerPixel() int { return PixelSize }

// Surface is an addressable pixel rectangle backed by a byte slice.
// The slice must be at least W*H*PixelSize bytes; Pixels() returns
// the backing slice which callers may pass back to the kernel.
type Surface struct {
	w, h int
	px   []byte
}

// NewSurface allocates a zeroed back buffer of w×h pixels.
func NewSurface(w, h int) *Surface {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Surface{w: w, h: h, px: make([]byte, w*h*PixelSize)}
}

// WrapSurface wraps an existing byte slice as a Surface. The slice
// must be at least w*h*PixelSize bytes or the result is unusable.
func WrapSurface(px []byte, w, h int) *Surface {
	return &Surface{w: w, h: h, px: px}
}

// W returns the surface width in pixels.
func (s *Surface) W() int { return s.w }

// H returns the surface height in pixels.
func (s *Surface) H() int { return s.h }

// Pixels returns the raw backing byte slice.
func (s *Surface) Bytes() []byte { return s.px }

// Clear fills every pixel with c.
func (s *Surface) Clear(c Pixel) {
	for i := 0; i+PixelSize-1 < len(s.px); i += PixelSize {
		s.px[i] = byte(c)         // B
		s.px[i+1] = byte(c >> 8)  // G
		s.px[i+2] = byte(c >> 16) // R
		s.px[i+3] = 0             // X
	}
}

// FillRect fills the axis-aligned rectangle [x, x+w) × [y, y+h) inclusive
// with c. Coordinates are clamped to the surface bounds.
func (s *Surface) FillRect(x, y, w, h int, c Pixel) {
	if w <= 0 || h <= 0 {
		return
	}
	x0 := x
	if x0 < 0 {
		x0 = 0
	}
	y0 := y
	if y0 < 0 {
		y0 = 0
	}
	x1 := x + w
	if x1 > s.w {
		x1 = s.w
	}
	y1 := y + h
	if y1 > s.h {
		y1 = s.h
	}
	b := byte(c)
	g := byte(c >> 8)
	r := byte(c >> 16)
	for row := y0; row < y1; row++ {
		base := (row*s.w + x0) * PixelSize
		for col := x0; col < x1; col++ {
			off := base + (col-x0)*PixelSize
			s.px[off] = b
			s.px[off+1] = g
			s.px[off+2] = r
			s.px[off+3] = 0
		}
	}
}

// DrawRect draws a 1-pixel outline of the rectangle [x, x+w) × [y, y+h).
func (s *Surface) DrawRect(x, y, w, h int, c Pixel) {
	if w <= 0 || h <= 0 {
		return
	}
	s.drawHLine(x, y, w, c)     // top
	s.drawHLine(x, y+h-1, w, c) // bottom
	s.drawVLine(x, y, h, c)     // left
	s.drawVLine(x+w-1, y, h, c) // right
}

func (s *Surface) drawHLine(x, y, n int, c Pixel) {
	if n <= 0 || y < 0 || y >= s.h {
		return
	}
	x0 := x
	if x0 < 0 {
		x0 = 0
	}
	x1 := x + n
	if x1 > s.w {
		x1 = s.w
	}
	base := (y*s.w + x0) * PixelSize
	b := byte(c)
	g := byte(c >> 8)
	r := byte(c >> 16)
	for col := 0; col < x1-x0; col++ {
		off := base + col*PixelSize
		s.px[off] = b
		s.px[off+1] = g
		s.px[off+2] = r
		s.px[off+3] = 0
	}
}

func (s *Surface) drawVLine(x, y, n int, c Pixel) {
	if n <= 0 || x < 0 || x >= s.w {
		return
	}
	y0 := y
	if y0 < 0 {
		y0 = 0
	}
	y1 := y + n
	if y1 > s.h {
		y1 = s.h
	}
	off := (y0*s.w + x) * PixelSize
	b := byte(c)
	g := byte(c >> 8)
	r := byte(c >> 16)
	step := s.w * PixelSize
	for row := 0; row < y1-y0; row++ {
		s.px[off+row*step] = b
		s.px[off+row*step+1] = g
		s.px[off+row*step+2] = r
		s.px[off+row*step+3] = 0
	}
}

// DrawLine draws a 1-pixel line via Bresenham's algorithm.
func (s *Surface) DrawLine(x0, y0, x1, y1 int, c Pixel) {
	dx := absI(x1 - x0)
	dy := -absI(y1 - y0)
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	for {
		s.drawHLine(x0, y0, 1, c)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// DrawCircle draws a circle outline using the midpoint algorithm.
func (s *Surface) DrawCircle(cx, cy, r int, c Pixel) {
	if r < 1 {
		return
	}
	x := r
	y := 0
	dec := 1 - r
	for x >= y {
		s.drawHLine(cx+y, cy-x, 1, c)
		s.drawHLine(cx+x, cy-y, 1, c)
		s.drawHLine(cx-x, cy-y, 1, c)
		s.drawHLine(cx-y, cy-x, 1, c)
		s.drawHLine(cx-y, cy+x, 1, c)
		s.drawHLine(cx+x, cy+y, 1, c)
		s.drawHLine(cx-x, cy+y, 1, c)
		s.drawHLine(cx+y, cy+x, 1, c)
		y++
		if dec <= 0 {
			dec += 2*y + 1
		} else {
			x--
			dec += 2*(y-x) + 1
		}
	}
}

// FillCircle fills a disc of radius r centred at (cx, cy) with c.
func (s *Surface) FillCircle(cx, cy, r int, c Pixel) {
	if r < 0 {
		return
	}
	s.FillRect(cx-r, cy-r, 2*r+1, 2*r+1, c)
	// punch corners inside the bounding box but outside the disc
	r2 := r * r
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy > r2 {
				px, py := cx+dx, cy+dy
				if px >= 0 && px < s.w && py >= 0 && py < s.h {
					off := (py*s.w + px) * PixelSize
					s.px[off] = 0
					s.px[off+1] = 0
					s.px[off+2] = 0
					s.px[off+3] = 0
				}
			}
		}
	}
}

// DrawText renders ASCII text at pixel (x, y) using the 8×8 font.
// Only printable ASCII (0x20–0x7E) is rendered; other bytes are skipped.
// Returns the y position after the last rendered line.
func (s *Surface) DrawText(x, y int, text string, c Pixel) int {
	if y < 0 || y+8 > s.h {
		return y
	}
	cx := x
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch == '\n' {
			cx = x
			y += 8
			if y+8 > s.h {
				return y
			}
			continue
		}
		if ch == '\r' {
			cx = x
			continue
		}
		if cx < 0 || cx+8 > s.w {
			continue
		}
		glyph := font8x8(ch)
		if glyph == nil {
			continue
		}
		for row := 0; row < 8; row++ {
			bits := glyph[row]
			py := y + row
			if py < 0 || py >= s.h {
				continue
			}
			off := (py*s.w + cx) * PixelSize
			for col := 0; col < 8; col++ {
				if bits&(0x80>>uint(col)) != 0 {
					s.px[off+col*PixelSize] = byte(c)
					s.px[off+col*PixelSize+1] = byte(c >> 8)
					s.px[off+col*PixelSize+2] = byte(c >> 16)
					s.px[off+col*PixelSize+3] = 0
				}
			}
		}
		cx += 9 // 8 px glyph + 1 px spacing
	}
	return y
}

// DrawTextScaled renders ASCII text at (x, y) scaled by `scale`
// (1 = normal 8×8, 2 = 16×16, etc.). Each font pixel becomes a
// scale×scale block.
func (s *Surface) DrawTextScaled(x, y, scale int, text string, c Pixel) {
	if scale < 1 {
		scale = 1
	}
	gw := 8 * scale
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch == '\n' {
			x = 0
			y += gw + scale
			continue
		}
		if ch == '\r' {
			x = 0
			continue
		}
		if ch < 32 || ch > 126 {
			x += (gw + scale)
			continue
		}
		glyph := font8x8Data[ch-32]
		for row := 0; row < 8; row++ {
			bits := glyph[row]
			for col := 0; col < 8; col++ {
				if bits&(0x80>>uint(col)) != 0 {
					s.FillRect(x+col*scale, y+row*scale, scale, scale, c)
				}
			}
		}
		x += gw + scale
	}
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
