// guest #3: stock Go, GOOS=wasip1 GOARCH=wasm -- the anti-nightmare proof.
// No patched toolchain anywhere; kernel-side WASI profile serves it.
package main

import "os"

func main() {
	os.Stdout.WriteString("hello from Go\n")
}
