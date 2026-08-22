//go:build wasip1

// console.wasm entry: wires the frozen ABI surface (kern.Real) and
// serial stdout into the portable Serve loop. Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o console.wasm main.go console.go
package main

import (
	"os"

	"kernel.lane/guests/lib"
)

func main() {
	os.Stdout.WriteString("[console] up\n")
	Serve(kern.Real(), os.Stdout, Options{})
}
