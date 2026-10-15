//go:build wasip1

// Command ssh.wasm — SSH server over net.wasm TCP (Phase 13.5).
// Uses golang.org/x/crypto/ssh for protocol/crypto; net.wasm for TCP.
package main

import (
	"fmt"
	"os"

	lib "kernel.lane/guests/lib"
)

func main() {
	if err := Run(lib.Real(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "[ssh] fatal: %v\n", err)
		os.Exit(1)
	}
}
