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
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	// kernel fills buf as argc sequential NUL-terminated strings; entry 1
	// is our config text (entry 0 = program name). Offsets in vecs[] are
	// linear addresses, not slice indices -- never index buf with them.
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
	os.Stdout.WriteString("[shell] up\n")
	Run(lib.Real(), ShellOptions{Root: readRoot()})
}
