//go:build wasip1

package main

import (
	"errors"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport kernel kern_uart_read
func uart_read(buf *byte, length uint32) int32

//go:wasmimport kernel kern_uart_write
func uart_write(buf *byte, length uint32) int32

// uartTransport implements OffloadTransport over a UART byte stream using
// the offload framing: 4-byte LE length prefix + payload. Read/write block
// until the full frame is transferred (UART is byte-stream, not datagrams).
type uartTransport struct {
	rx []byte // unparsed receive buffer
	tx []byte // bytes queued for transmission
}

func newUARTTransport() *uartTransport {
	return &uartTransport{}
}

// readFull fills dst from the UART, blocking until len(dst) bytes arrive.
func (u *uartTransport) readFull(dst []byte) error {
	for len(dst) > 0 {
		n := int(uart_read(&dst[0], uint32(len(dst))))
		if n <= 0 {
			continue
		}
		dst = dst[n:]
	}
	return nil
}

// writeAll drains src to the UART.
func (u *uartTransport) writeAll(src []byte) error {
	for len(src) > 0 {
		n := int(uart_write(&src[0], uint32(len(src))))
		if n <= 0 {
			continue
		}
		src = src[n:]
	}
	return nil
}

func (u *uartTransport) SendFrame(f []byte) bool {
	if len(f) > OffloadMaxPayload {
		return false
	}
	hdr := make([]byte, 4)
	lib.Put32(hdr, uint32(len(f)))
	if err := u.writeAll(hdr); err != nil {
		return false
	}
	return u.writeAll(f) == nil
}

func (u *uartTransport) RecvFrame() ([]byte, bool) {
	for {
		if len(u.rx) >= 4 {
			sz := int(lib.Get32(u.rx[:4]))
			if sz > OffloadMaxPayload {
				u.rx = u.rx[4:] // drop corrupt
				continue
			}
			if len(u.rx) >= 4+sz {
				f := u.rx[4 : 4+sz]
				out := make([]byte, len(f))
				copy(out, f)
				u.rx = u.rx[4+sz:]
				return out, true
			}
		}
		buf := make([]byte, 256)
		n := int(uart_read(&buf[0], 256))
		if n <= 0 {
			return nil, false
		}
		u.rx = append(u.rx, buf[:n]...)
	}
}

var _ OffloadTransport = (*uartTransport)(nil)

// uartErrRead is unused on wasip1 — the import returns int32 count.
var _ = errors.New
