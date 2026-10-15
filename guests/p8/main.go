// test_p8.go -- Phase 8 gates. Spawned twice by the kernel:
//
//	"busy"  (admin caps): tight compute loop that yields only every 1000
//	  iterations -- the wasm3 cooperative path needs that to interleave
//	  at all; under the owned engine preemption is timer-driven anyway.
//	  Prints [busy] progress every 200k iterations, 60 marks.
//	"polite": yields constantly; prints [polite] tick every 50k yields,
//	  6 ticks, then "done".
//
// Gate: with preemption ON, busy's presence must NOT stop polite's
// progress -- both markers interleave on serial. Loop bounds are sized
// so the whole gate finishes in seconds: one sched_yield costs ~60us
// through a full session switch.
package main

import (
	"os"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

var argv0 string

func readArgs() {
	var argc int32
	var bl int32
	args_sizes_get(&argc, &bl)
	if argc < 1 || bl <= 0 {
		argv0 = "?"
		return
	}
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	argv0 = string(buf[:end])
}

func main() {
	readArgs()
	os.Stdout.WriteString("[" + argv0 + "] start\n")
	if argv0 == "ppb" || argv0 == "busy" {
		busy()
	} else {
		polite()
	}
	os.Exit(0)
}

func busy() {
	x := uint64(1469598103934665603)
	marks := 0
	// 60 marks x 200k iterations keeps the hog alive for roughly as
	// long as polite's whole run, so the two really do overlap.
	for i := uint64(0); i < 200000000; i++ {
		x = x*6364136223846793005 + 1442695040888963407
		if i%1000 == 0 {
			sched_yield()
		}
		if i%200000 == 0 && i > 0 {
			marks++
			os.Stdout.WriteString("[busy] progress " + itoa(int(marks)) +
				" x=" + itoa(int(x&0xFFFF)) + "\n")
			if marks >= 60 {
				break
			}
		}
	}
	os.Stdout.WriteString("[busy] done marks=" + itoa(marks) + "\n")
}

func polite() {
	ticks := 0
	// One sched_yield costs ~60us through the full session switch, so
	// the old 60M-iteration bound needed ~an hour to reach the "done"
	// marker and the gate could never pass inside any QEMU timeout.
	// 300k yields / 6 ticks is the same no-starvation evidence
	// (a hog sharing the core the whole time) in seconds.
	for i := 0; i < 300000; i++ {
		sched_yield()
		if i%50000 == 0 && i > 0 {
			ticks++
			os.Stdout.WriteString("[polite] tick " + itoa(ticks) + "\n")
			if ticks >= 6 {
				break
			}
		}
	}
	os.Stdout.WriteString("[polite] done ticks=" + itoa(ticks) + "\n")
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
