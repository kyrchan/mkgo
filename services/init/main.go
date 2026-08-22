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
	vecs := make([]uint32, argc)
	buf := make([]byte, bl)
	args_get(&vecs[0], &buf[0])
	out := make([]string, 0, argc-1)
	for i := 1; i < int(argc); i++ {
		start := int(vecs[i])
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if start <= end && end <= len(buf) {
			out = append(out, string(buf[start:end]))
		}
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
