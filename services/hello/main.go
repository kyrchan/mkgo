// Command hello is the minimal service example from the README: it binds
// a well-known port name and echoes messages back with an "echo: " prefix.
// Build with: GOOS=wasip1 GOARCH=wasm go build -o hello.wasm.raw .
package main

import (
	lib "kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()
	h := k.PortBind("hello") // bind well-known name
	buf := make([]byte, lib.MaxMsg)
	for {
		n := k.PortRecv(h, buf) // >0 = length, 0 = none, -1 = err
		if n > 0 {
			k.PortSend(h, append([]byte("echo: "), buf[:int(n)]...))
		}
		if n == 0 {
			k.Yield()
		}
	}
}
