//go:build wasip1

// login.wasm entry: wires the frozen ABI surface into the portable
// Serve loop. Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o login.wasm .
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

func main() {
	os.Stdout.WriteString("[login] ready\n")
	Serve(lib.Real(), LoginOptions{})
}
