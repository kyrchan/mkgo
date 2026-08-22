//go:build wasip1

// shell.wasm entry: wires the frozen ABI surface into the portable Run
// loop. The session name (argv[0]) is "shell"; argv[1], when present, is
// the user root ("/home/<user>") passed by login at SPAWN time.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o shell.wasm .
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

func readRoot() string {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 2 || bl <= 0 {
		return ""
	}
	vecs := make([]uint32, argc)
	buf := make([]byte, bl)
	args_get(&vecs[0], &buf[0])
	end := int(vecs[1])
	start := end
	for start < len(buf) && buf[start] != 0 {
		start++
	}
	_ = end
	if start > len(buf) {
		return ""
	}
	return string(buf[int(vecs[1]):start])
}

func main() {
	os.Stdout.WriteString("[shell] up\n")
	Run(lib.Real(), ShellOptions{Root: readRoot()})
}
