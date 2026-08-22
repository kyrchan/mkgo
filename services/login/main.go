// login.wasm -- well-known "login" server, stub auth (Phase 4).
// Announces readiness over the console port (relay), then heartbeats on
// its own stdout so crash-isolation is observable after console dies.
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

//go:wasmimport kernel kern_port_send
func port_send(h int32, buf *byte, len uint32) int32

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func main() {
	os.Stdout.WriteString("[login] ready\n")
	ch := int32(-1)
	for i := 0; i < 100000 && ch < 0; i++ { // wait for console to bind its name
		ch = port_bind(&cstr("console")[0], 7)
		if ch < 0 {
			sched_yield()
		}
	}
	msg := cstr("[login] auth service online")
	if ch >= 0 {
		port_send(ch, &msg[0], uint32(len(msg)-1))
		os.Stdout.WriteString("[login] ready-line relayed via console server\n")
	}
	const beats = 8
	for i := 0; i < 4000000; i++ {
		sched_yield()
		if i%500000 == 0 && i/beats >= 0 {
			n := i / 500000
			if n > 0 {
				os.Stdout.WriteString("[login] alive ")
				os.Stdout.WriteString(itoa(n))
				os.Stdout.WriteString("\n")
			}
		}
	}
	os.Stdout.WriteString("[login] done\n")
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
