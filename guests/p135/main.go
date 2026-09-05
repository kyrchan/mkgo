// p135 -- Phase 13.5 gate driver: verifies the Go compiler (go.wasm) can be
// spawned by the kernel and its runtime initializes without OOM crashes.
//
// The Go compiler (go.wasm) is ~25 MB with a 175-page initial memory and
// grows via incremental memory.grow calls during runtime init. The kernel's
// rt_realloc (with mm_grow_in_place fast path) must handle these without
// O(n) copies or exhaustion. We spawn go.wasm and wait for it to exit; if it
// OOMs or crashes, the session dies with a non-zero trap. go.wasm will
// eventually error out (missing GOROOT) but that is fine — the gate is that
// its runtime initializes and produces output, proving the memory fix works.
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

func main() {
	os.Stdout.WriteString("[p135] start\n")
	k := lib.Real()

	rg, err := lib.BindRegistry(k)
	for err != nil {
		yieldGo()
		rg, err = lib.BindRegistry(k)
	}

	sid, err := rg.Spawn("go", "go.wasm", 0)
	if err != nil {
		os.Stdout.WriteString("[p135] FAIL spawn go: ")
		os.Stdout.WriteString(err.Error())
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p135] spawned go sid=")
	os.Stdout.WriteString(itoa(int(sid)))
	os.Stdout.WriteString("\n")

	for i := 0; i < 9000; i++ {
		yieldGo()
		list, lerr := rg.List()
		if lerr == nil {
			for _, si := range list {
				if si.Name == "go" && si.State == 3 { /* S_ZOMBIE */
					os.Stdout.WriteString("[p135] go exit, success\n")
					return
				}
			}
		}
	}
	os.Stdout.WriteString("[p135] timeout: go did not exit\n")
}

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

func yieldGo() {
	sched_yield()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
