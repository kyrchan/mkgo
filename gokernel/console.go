package main

// Serial console (COM1) on top of Plan 9 asm primitives.

func puts(s string) {
	for i := 0; i < len(s); i++ {
		putc(s[i])
	}
}

const hexdigits = "0123456789abcdef"

func puthex(v uint64) {
	puts("0x")
	for i := 60; i >= 0; i -= 4 {
		putc(hexdigits[(v>>uint(i))&0xF])
	}
}

func putdec(v uint64) {
	var buf [24]byte
	i := len(buf)
	if v == 0 {
		putc('0')
		return
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	puts(string(buf[i:]))
}
