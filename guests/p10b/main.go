// p10b -- Phase 10 multiuser gate, ATTACKER SIDE (u2).
// Auths as u2 and must FAIL to reach u1's data over BOTH routes:
//   direct-port : lib.FSClient stat/create inside /home/u1
//   preview1    : os.ReadFile("/home/u1/secret.txt") -> denied
// Also proves u2's own home still works (no false-positive lockdown).
package main

import (
	"os"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

var argv0 string

func readArgs() {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 1 || bl <= 0 {
		argv0 = "x"
		return
	}
	vecs := make([]uint32, argc)
	buf := make([]byte, bl)
	args_get(&vecs[0], &buf[0])
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	argv0 = string(buf[:end])
}

func auth(user, pw string) bool {
	k := lib.Real()
	lh := k.PortBind(lib.NameLogin)
	for i := 0; i < 200000 && lh == lib.InvalidHandle; i++ {
		sched_yield()
		lh = k.PortBind(lib.NameLogin)
	}
	if lh == lib.InvalidHandle {
		return false
	}
	q := k.PortCreate(argv0)
	if q == lib.InvalidHandle {
		q = k.PortBind(argv0)
	}
	req := make([]byte, 24, 24+2+len(user)+len(pw))
	req[0] = 1
	req[3] = 1
	copy(req[8:24], argv0)
	req = append(req, byte(len(user)))
	req = append(req, user...)
	req = append(req, byte(len(pw)))
	req = append(req, pw...)
	k.PortSend(lh, req)
	out := make([]byte, 128)
	for i := 0; i < 300000; i++ {
		n := k.PortRecv(q, out)
		if n >= 28 {
			st := int32(out[24]) | int32(out[25])<<8 | int32(out[26])<<16 | int32(out[27])<<24
			return st == 0
		}
		sched_yield()
	}
	return false
}

func main() {
	readArgs()
	os.Stdout.WriteString("[p10b] start\n")

	if !auth("u2", "u2") {
		os.Stdout.WriteString("[p10b] FAIL auth\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p10b] auth ok\n")

	k := lib.Real()
	fsc, err := lib.BindFS(k, argv0)
	if err != nil {
		os.Stdout.WriteString("[p10b] FAIL bindfs " + err.Error() + "\n")
		os.Exit(1)
	}

	// wait through provisioning window using an OWN-home probe
	ownOK := false
	for i := 0; i < 50; i++ {
		if _, e := fsc.Stat("."); e == nil {
			ownOK = true
			break
		}
		sched_yield()
	}
	_ = ownOK

	// --- direct-port denials ---
	if _, serr := fsc.Stat("/home/u1/secret.txt"); serr == nil {
		os.Stdout.WriteString("[p10b] FAIL saw u1 file (direct)\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p10b] deny stat ok\n")
	}
	if cerr := fsc.Create("/home/u1/evil.txt"); cerr == nil {
		os.Stdout.WriteString("[p10b] FAIL wrote into u1 home (direct)\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p10b] deny create ok\n")
	}
	if _, werr := fsc.WriteFile("/home/u1/secret.txt", 0,
		[]byte("overwrite")); werr == nil {
		os.Stdout.WriteString("[p10b] FAIL overwrote u1 file (direct)\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p10b] deny write ok\n")
	}

	// --- preview1 (route-1) denial via stock os package ---
	if _, rerr := os.ReadFile("/home/u1/secret.txt"); rerr == nil {
		os.Stdout.WriteString("[p10b] FAIL read u1 file (route1)\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p10b] deny route1 read ok\n")
	}
	if werr := os.WriteFile("/home/u1/evil2.txt",
		[]byte("x"), 0o644); werr == nil {
		os.Stdout.WriteString("[p10b] FAIL route1 write into u1 home\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p10b] deny route1 write ok\n")
	}

	// own home still functional (positive control)
	if cerr := fsc.Create("mine.txt"); cerr != nil {
		os.Stdout.WriteString("[p10b] FAIL own create " +
			cerr.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p10b] all ok\n")
}
