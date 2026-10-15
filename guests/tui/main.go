// tui.wasm — minimal TUI guest proving the --tty pipeline.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o tui.wasm .
package main

import (
	"os"
	"time"

	kern "kernel.lane/guests/lib"
)

func main() {
	k := kern.Real()

	outName := "tty.out"
	if len(os.Args) > 1 {
		outName = os.Args[1]
	}
	outH := k.PortBind(outName)
	if outH == kern.InvalidHandle {
		os.Stderr.WriteString("tui: bind output failed\n")
		return
	}

	inName := outName + ".in"
	inH := k.PortCreate(inName)
	if inH == kern.InvalidHandle {
		inH = k.PortBind(inName)
	}
	if inH == kern.InvalidHandle {
		os.Stderr.WriteString("tui: create input failed\n")
		return
	}

	send := func(p []byte) {
		for i := 0; i < 2000; i++ {
			if k.PortSend(outH, p) == 0 {
				return
			}
			k.Yield()
		}
	}

	send([]byte("\x1b[?1049h\x1b[2J\x1b[?25l"))
	send([]byte("\x1b[1;37m  kernel-lane TUI demo  \x1b[0m\n"))
	send([]byte("  press 'q' to quit\n"))
	send([]byte("\x1b[H"))

	buf := make([]byte, 64)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := k.PortRecv(inH, buf)
		if n > 0 {
			send(buf[:n])
			if buf[0] == 'q' || buf[0] == 0x03 {
				break
			}
		}
		k.Yield()
	}

	send([]byte("\x1b[?1049l\x1b[?25h"))
}
