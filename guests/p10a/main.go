// p10a -- Phase 10 multiuser gate, USER SIDE (u1).
// Auths as u1, then exercises BOTH fs routes on her own home:
//   direct-port  : lib.FSClient create/write/read secret.txt
//   preview1     : os.WriteFile/os.ReadFile /home/u1/via-route1.txt
//     (Go's os package issues path_open through the kernel-routed
//      preview1 transport; the "/" preopen makes stock os.* work)
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
func argv_get(argv *uint32, buf *byte) int32

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
	argv_get(&vecs[0], &buf[0])
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

const secret = "u1-private-data"

func main() {
	readArgs()
	os.Stdout.WriteString("[p10a] start\n")

	if !auth("u1", "u1") {
		os.Stdout.WriteString("[p10a] FAIL auth\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p10a] auth ok\n")

	k := lib.Real()
	fsc, err := lib.BindFS(k, argv0)
	if err != nil {
		os.Stdout.WriteString("[p10a] FAIL bindfs " + err.Error() + "\n")
		os.Exit(1)
	}

	// provisioning-window retry on the first op
	for i := 0; i < 50; i++ {
		err = fsc.Create("secret.txt")
		if err == nil || err != lib.ErrFSAccess {
			break
		}
		sched_yield()
	}
	if err != nil {
		os.Stdout.WriteString("[p10a] FAIL create " + err.Error() + "\n")
		os.Exit(1)
	}
	if _, err := fsc.WriteFile("secret.txt", 0, []byte(secret)); err != nil {
		os.Stdout.WriteString("[p10a] FAIL write " + err.Error() + "\n")
		os.Exit(1)
	}
	buf := make([]byte, len(secret))
	n, rerr := fsc.ReadFile("secret.txt", 0, buf)
	if rerr != nil || n != len(secret) || string(buf) != secret {
		os.Stdout.WriteString("[p10a] FAIL direct roundtrip\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p10a] direct ok\n")

	// route-1 via stock os package against the ABSOLUTE rooted path
	if werr := os.WriteFile("/home/u1/via-route1.txt",
		[]byte("route1"), 0o644); werr != nil {
		os.Stdout.WriteString("[p10a] FAIL route1 write " + werr.Error() + "\n")
		os.Exit(1)
	}
	got, rerr2 := os.ReadFile("/home/u1/via-route1.txt")
	if rerr2 != nil || string(got) != "route1" {
		os.Stdout.WriteString("[p10a] FAIL route1 read " +
			eStr(rerr2) + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p10a] route1 ok\n")
	os.Stdout.WriteString("[p10a] all ok\n")
}

func eStr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
