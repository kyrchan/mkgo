//go:build wasip1

// shell.wasm entry: wires the frozen ABI surface into the portable Run
// loop. argv[1], when present, is the user root ("/home/<user>") passed by
// login at SPAWN time.
//
// When spawned with --io-port <name>, the shell creates a port named <name>
// for bidirectional I/O (SSH sessions). Otherwise, keyboard+console mode.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o shell.wasm .
package main

import (
	"os"
	"strings"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

func readArgs() (root, ioPort, ioIn string) {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 2 || bl <= 0 {
		return "", "", ""
	}
	buf := make([]byte, bl)
	vecs := make([]uint32, argc)
	args_get(&vecs[0], &buf[0])
	var args []string
	start := 0
	for i := 0; i < int(argc) && start < len(buf); i++ {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		args = append(args, string(buf[start:end]))
		start = end + 1
	}
	for i := 1; i < len(args); i++ {
		if args[i] == "--io-port" && i+1 < len(args) {
			ioPort = args[i+1]
			i++
		} else if args[i] == "--io-in" && i+1 < len(args) {
			ioIn = args[i+1]
			i++
		} else if root == "" && !strings.HasPrefix(args[i], "--") && len(args[i]) > 0 && args[i][0] == '/' {
			root = args[i]
		}
	}
	return root, ioPort, ioIn
}

func main() {
	os.Stdout.WriteString("[shell] up\n")
	root, ioPort, ioIn := readArgs()
	opts := ShellOptions{Root: root, IOPort: ioPort, IOPortIn: ioIn}
	Run(lib.Real(), opts)
}
