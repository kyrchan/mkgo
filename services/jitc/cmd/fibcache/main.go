// Command fibcache builds the Stage 3a QEMU smoke-gate artifacts:
// a hand-built fib.wasm module plus its native cache container.
// Usage: go run ./cmd/fibcache <outdir>
// Writes: fib.wasm, fib.native
package main

import (
	"fmt"
	"os"

	jitc "kernel.lane/services/jitc"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fibcache <outdir>")
		os.Exit(2)
	}
	out := os.Args[1]
	wasm := jitc.FibModule()
	m, err := jitc.Decode(wasm)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}
	ir, err := jitc.CompileModuleWithRelocs(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compile:", err)
		os.Exit(1)
	}
	cache, err := jitc.BuildCache(wasm, 2, m, ir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cache:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out+"/fib.wasm", wasm, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(out+"/fib.native", cache, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("fib.wasm %d bytes, fib.native %d bytes (%d funcs)\n",
		len(wasm), len(cache), len(m.Code))
}
