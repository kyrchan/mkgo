//go:build wasip1

// wlan.wasm entry (AGANTS.md Phase 12): wires the frozen ABI surface
// (kern.Real) + UART offload transport into the portable Run loop.
// argv[1] = target SSID (default "testnet").
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o wlan.wasm .
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

func readSSIDArg() string {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 2 || bl <= 0 {
		return ""
	}
	buf := make([]byte, bl)
	var vecs [16]uint32
	args_get(&vecs[0], &buf[0])
	start := 0
	for i := 0; i < int(argc) && start < len(buf); i++ {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if i == 1 {
			return string(buf[start:end])
		}
		start = end + 1
	}
	return ""
}

func main() {
	os.Stdout.WriteString("[wlan] up\n")
	off := newUARTTransport()
	code := Run(lib.Real(), off, os.Stdout, readSSIDArg())
	os.Exit(code)
}
