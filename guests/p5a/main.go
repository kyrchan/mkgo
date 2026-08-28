//go:build wasip1

// test_p5a -- Phase 5 gate: kernel-routed fs ops via lib abstractions.
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

func libArgv() []string {
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc < 1 || bl <= 0 {
		return nil
	}
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	var out []string
	start := 0
	for start < len(buf) {
		end := start
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		out = append(out, string(buf[start:end]))
		start = end + 1
	}
	return out
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

func main() {
	argv := libArgv()
	if len(argv) > 0 {
		argv0 = argv[0]
	} else {
		argv0 = "?"
	}
	os.Stdout.WriteString("[p5a] start " + argv0 + "\n")

	k := lib.Real()

	if !auth(k, argv0, "u1", "u1") {
		os.Stdout.WriteString("[p5a] FAIL auth\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5a] auth ok\n")

	fsc, err := lib.BindFS(k, argv0)
	if err != nil {
		os.Stdout.WriteString("[p5a] FAIL bindfs: " + err.Error() + "\n")
		os.Exit(1)
	}

	text := []byte("hello from p5a roundtrip")
	// The login service registers this user with fs right after the AUTH
	// reply; under cooperative scheduling the first op can race that
	// registration. Bounded retry on access-denied absorbs the window.
	var cerr error
	for i := 0; i < 50; i++ {
		cerr = fsc.Create("hello.txt")
		if cerr == nil || cerr != lib.ErrFSAccess {
			break
		}
		sched_yield()
	}
	if cerr != nil {
		os.Stdout.WriteString("[p5a] create err: " + cerr.Error() + "\n")
		return
	}
	n, werr := fsc.WriteFile("hello.txt", 0, text)
	if werr != nil {
		os.Stdout.WriteString("[p5a] write err: " + werr.Error() + "\n")
		return
	}
	buf := make([]byte, len(text))
	n, rerr := fsc.ReadFile("hello.txt", 0, buf)
	if rerr != nil {
		os.Stdout.WriteString("[p5a] read err: " + rerr.Error() + "\n")
		return
	}
	match := true
	for i := range text {
		if i < n && buf[i] != text[i] {
			match = false
			break
		}
	}
	if match && n == len(text) {
		os.Stdout.WriteString("[p5a] roundtrip ok bytes=" + itoa(n) + "\n")
	} else {
		os.Stdout.WriteString("[p5a] FAIL mismatch n=" + itoa(n) + "\n")
	}
}

func auth(k lib.Kernel, session, user, pw string) bool {
	lh := k.PortBind(lib.NameLogin)
	for i := 0; i < 200000 && lh == lib.InvalidHandle; i++ {
		sched_yield()
		lh = k.PortBind(lib.NameLogin)
	}
	if lh == lib.InvalidHandle {
		return false
	}
	q := k.PortCreate(session)
	if q == lib.InvalidHandle {
		q = k.PortBind(session)
	}
	req := make([]byte, 24)
	req[0] = 1 // opAuth
	req[3] = 1 // seq lo
	copy(req[8:24], session) // rname = our session name for reply routing
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
			return st == 0 // statusOK
		}
		sched_yield()
	}
	return false
}
