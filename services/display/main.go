//go:build wasip1

// display.wasm — text-mode terminal on the §9.FB framebuffer (ABI v1.2).
// Locates the FB window via devman ENUM (class 9), mirrors everything
// arriving on the well-known "console" relay into the terminal grid,
// and flushes damage to the framebuffer.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o display.wasm .
package main

import (
	"os"
	"time"

	lib "kernel.lane/guests/lib"
	"kernel.lane/services/display/terminal"
)

func main() {
	os.Stdout.WriteString("[display] up\n")
	fb, err := attachFB()
	if err != nil {
		os.Stdout.WriteString("[display] no framebuffer; exiting\n")
		return
	}
	tr := terminal.New(fb)
	tr.ShowCursor(true)
	tr.WriteString("\x1b[1;37mkernel-lane display ready\x1b[0m\n")
	tr.Flush()

	relay := consoleBind()
	buf := make([]byte, lib.MaxMsg)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tr.Tick()
			tr.Flush()
		default:
			n := relay.PortRecv(buf)
			if n > 0 {
				tr.WriteString(string(buf[:n]))
				tr.WriteString("\n")
				tr.Flush()
				continue
			}
			relay.Yield()
		}
	}
}
