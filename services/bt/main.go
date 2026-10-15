//go:build wasip1

// bt.wasm entry (AGANTS.md Phase 12): Bluetooth over the legacy UART shim.
//
// RX: the kernel's UART shim delivers received bytes as §4 input records
// (codepoint field = the UART byte), consumed via kern.InputRecv.
// TX: the kernel UART write import (kern_uart_write) frames bytes out.
// Debug: fd_write through os.Stdout (the kernel-routed console relay).
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o bt.wasm .
package main

import (
	"fmt"
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport kernel kern_uart_write
func kern_uart_write(b uint32) int32

// inputUART bridges the legacy UART shim over the kernel ABI: RX bytes
// arrive as §4 input records (codepoint = byte), TX bytes go out via the
// UART write import. Each input record carries one UART byte, so RX is
// byte-level and fully synchronous with the H4 decoder.
type inputUART struct {
	k   lib.Kernel
	la  [4]byte // one-record lookahead (InputRecv is destructive)
	has bool
}

func (u *inputUART) Poll() bool {
	if u.has {
		return true
	}
	n := u.k.InputRecv(u.la[:])
	if n >= int32(lib.InputRecLen) {
		u.has = true
		return true
	}
	return false
}

func (u *inputUART) Read() (byte, bool) {
	if !u.has && !u.Poll() {
		return 0, false
	}
	u.has = false
	return u.la[2], true // codepoint low byte = the UART RX byte
}

func (u *inputUART) Write(b byte) {
	kern_uart_write(uint32(b))
}

func (u *inputUART) WriteBytes(b []byte) {
	for _, x := range b {
		kern_uart_write(uint32(x))
	}
}

func main() {
	k := lib.Real()
	rc := Run(&inputUART{k: k}, os.Stdout)
	if rc != 0 {
		fmt.Println("bt: flow error")
		os.Exit(rc)
	}
	os.Exit(0)
}
