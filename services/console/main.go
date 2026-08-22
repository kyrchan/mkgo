// console.wasm -- well-known "console" server (abi/ABI.md §1/§2).
// Binds the name, relays datagrams to its own stdout (the kernel console
// window). Crash-isolation target of the Phase 4 gate.
package main

import (
	"os"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h int32, buf *byte, cap uint32) int32

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func main() {
	os.Stdout.WriteString("[console] up\n")
	h := port_create(&cstr("console")[0], 7)
	if h < 0 { // already owned (e.g. respawn): attach instead
		h = port_bind(&cstr("console")[0], 7)
	}
	buf := make([]byte, 4096)
	for {
		if h >= 0 {
			n := port_recv(h, &buf[0], uint32(len(buf)))
			if n > 0 {
				os.Stdout.Write(buf[:n])
				os.Stdout.WriteString("\n")
			}
		}
		sched_yield()
	}
}
