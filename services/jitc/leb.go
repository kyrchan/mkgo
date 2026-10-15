package jitc

import "errors"

var (
	ErrTruncated = errors.New("jitc: truncated input")
	ErrOverflow  = errors.New("jitc: LEB128 overflow")
)

// cursor is a bounds-checked reader over a byte slice. All methods
// advance the offset on success; any overrun returns ErrTruncated and
// leaves the cursor unusable (callers must abort on first error).
type cursor struct {
	b   []byte
	off int
}

func (c *cursor) rest() int { return len(c.b) - c.off }

func (c *cursor) u8() (byte, error) {
	if c.rest() < 1 {
		return 0, ErrTruncated
	}
	v := c.b[c.off]
	c.off++
	return v, nil
}

func (c *cursor) bytes(n int) ([]byte, error) {
	if n < 0 || c.rest() < n {
		return nil, ErrTruncated
	}
	v := c.b[c.off : c.off+n]
	c.off += n
	return v, nil
}

// u32leb decodes an unsigned 32-bit LEB128 (max 5 bytes).
func (c *cursor) u32leb() (uint32, error) {
	var v uint32
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := c.u8()
		if err != nil {
			return 0, err
		}
		if shift == 28 && b&0xF0 != 0 {
			return 0, ErrOverflow
		}
		v |= uint32(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, ErrOverflow
}

// s32leb decodes a signed 32-bit LEB128 (max 5 bytes).
func (c *cursor) s32leb() (int32, error) {
	var v int32
	var shift uint
	var b byte
	for i := 0; i < 5; i++ {
		var err error
		b, err = c.u8()
		if err != nil {
			return 0, err
		}
		v |= int32(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
		if i == 4 {
			return 0, ErrOverflow
		}
	}
	// sign-extend
	if shift < 32 && b&0x40 != 0 {
		v |= int32(-1 << shift)
	}
	return v, nil
}

// s64leb decodes a signed 64-bit LEB128 (max 10 bytes).
func (c *cursor) s64leb() (int64, error) {
	var v int64
	var shift uint
	var b byte
	for i := 0; i < 10; i++ {
		var err error
		b, err = c.u8()
		if err != nil {
			return 0, err
		}
		v |= int64(b&0x7F) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
		if i == 9 {
			return 0, ErrOverflow
		}
	}
	if shift < 64 && b&0x40 != 0 {
		v |= int64(-1 << shift)
	}
	return v, nil
}

// name reads a length-prefixed byte string (module/export/custom names).
func (c *cursor) name() (string, error) {
	n, err := c.u32leb()
	if err != nil {
		return "", err
	}
	raw, err := c.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// vecLen reads a vector count, rejecting counts that cannot fit in the
// remaining input (anti-OOM under fuzz: count <= remaining bytes).
func (c *cursor) vecLen() (uint32, error) {
	n, err := c.u32leb()
	if err != nil {
		return 0, err
	}
	if uint64(n) > uint64(c.rest()) {
		return 0, ErrTruncated
	}
	return n, nil
}
