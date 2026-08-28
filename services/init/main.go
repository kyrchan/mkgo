//go:build wasip1

// init.wasm entry: the kernel spawns ONLY this module (AGENTS.md). The
// loader preloads \etc\init.conf and \etc\kernel.conf from the ESP and
// hands them over via WASI argv: argv[1] = init.conf text,
// argv[2] = kernel.conf text (services/ABI-NOTES.md §6).
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o init.wasm .
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

// readArgs returns argv strings 1..n-1 (config texts).
func readArgs() []string {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 2 || bl <= 0 {
		return nil
	}
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	// The kernel fills buf sequentially as argc NUL-terminated strings in
	// argv order; vecs[] holds linear-memory offsets (not slice indices),
	// so split buf directly instead of dereferencing offsets.
	out := make([]string, 0, argc-1)
	start := 0
	for i := 0; i < int(argc) && start < len(buf); i++ {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if i >= 1 {
			out = append(out, string(buf[start:end]))
		}
		start = end + 1
	}
	return out
}

func main() {
	os.Stdout.WriteString("[init] up\n")
	confText := ""
	knobText := ""
	if args := readArgs(); len(args) > 0 {
		confText = args[0]
		if len(args) > 1 {
			knobText = args[1]
		}
	} else {
		os.Stdout.WriteString("[init] no init.conf in argv; nothing to spawn\n")
	}
	svcs, err := ParseConf(confText)
	if err != nil {
		os.Stdout.WriteString("[init] bad init.conf: " + err.Error() + "\n")
		return
	}
	Run(lib.Real(), InitOptions{Services: svcs, Knobs: knobText})
}

