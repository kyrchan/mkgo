//go:build wasip1

// login.wasm entry: wires the frozen ABI surface into the portable
// Serve loop. Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o login.wasm .
package main

import (
	lib "kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()
	var conh lib.Handle = lib.InvalidHandle
	consoleOut(k, &conh, "ready\n")
	Serve(k, LoginOptions{})
}
