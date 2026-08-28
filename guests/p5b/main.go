// p5b -- Phase 5 gate, DIRECT-PORT route (guests speak to "fs" directly
// over §1 ports using lib.FSClient). Auth as u2, then relative-path ops
// rooted at /home/u2: create/write/read b.txt, delete + verify-gone, then
// attempt u1's file -- fs rooting hides it => denial on serial.
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
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	argv0 = string(buf[:end])
}

// authAs sends the canonical v1.1 AUTH frame; rname = session name so
// login's registry LOGIN targets THIS session with u2's uid+caps.
func authAs(user, pw string) bool {
	k := lib.Real()
	lh := k.PortBind(lib.NameLogin)
	for i := 0; i < 200000 && lh == lib.InvalidHandle; i++ {
		sched_yield()
		lh = k.PortBind(lib.NameLogin)
	}
	if lh == lib.InvalidHandle {
		os.Stdout.WriteString("[authB] bind failed\n")
		return false
	}
	q := k.PortCreate(argv0)
	if q == lib.InvalidHandle {
		q = k.PortBind(argv0)
	}
	req := make([]byte, 24, 24+2+len(user)+len(pw))
	req[0] = 1 // opAuth
	req[3] = 1 // seq
	copy(req[8:24], argv0)
	req = append(req, byte(len(user)))
	req = append(req, user...)
	req = append(req, byte(len(pw)))
	req = append(req, pw...)
	k.PortSend(lh, req)

	out := make([]byte, 128)
	for i := 0; i < 300000; i++ {
		n := k.PortRecv(q, out)
		if n >= 28 { // canonical header + {status i32,...}
			st := int32(out[24]) | int32(out[25])<<8 | int32(out[26])<<16 | int32(out[27])<<24
			return st == 0
		}
		sched_yield()
	}
	return false
}

const bText = "b-file owned by u2"

func main() {
	readArgs()
	os.Stdout.WriteString("[p5b] start\n")

	k := lib.Real()

	if !authAs("u2", "u2") {
		os.Stdout.WriteString("[p5b] FAIL auth\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5b] auth ok\n")

	fsc, err := lib.BindFS(k, argv0)
	if err != nil {
		os.Stdout.WriteString("[p5b] FAIL bindfs: " + err.Error() + "\n")
		os.Exit(1)
	}

	// The login service registers this user with fs right after the AUTH
	// reply; retry the first op through that provisioning window.
	var cerr error
	for i := 0; i < 50; i++ {
		cerr = fsc.Create("b.txt")
		if cerr == nil || cerr != lib.ErrFSAccess {
			break
		}
		sched_yield()
	}
	if cerr != nil {
		os.Stdout.WriteString("[p5b] FAIL create: " + eStr(cerr) + "\n")
		os.Exit(1)
	}
	if _, err := fsc.WriteFile("b.txt", 0, []byte(bText)); err != nil {
		os.Stdout.WriteString("[p5b] FAIL write: " + err.Error() + "\n")
		os.Exit(1)
	}
	buf := make([]byte, len(bText))
	n, err := fsc.ReadFile("b.txt", 0, buf)
	if err != nil || n != len(bText) || string(buf) != bText {
		os.Stdout.WriteString("[p5b] FAIL readback n=" + itoa(n) +
			" err=" + eStr(err) + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5b] write/read ok bytes=" + itoa(n) + "\n")

	if err := fsc.Delete("b.txt"); err != nil {
		os.Stdout.WriteString("[p5b] FAIL delete: " + err.Error() + "\n")
		os.Exit(1)
	}
	if _, serr := fsc.Stat("b.txt"); serr == nil {
		os.Stdout.WriteString("[p5b] FAIL still exists after delete\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5b] delete verified\n")

	// cross-user denial: u2 must NOT see u1's file (hidden, no oracle)
	if _, serr := fsc.Stat("/home/u1/hello.txt"); serr == nil {
		os.Stdout.WriteString("[p5b] FAIL u2 saw u1 file!\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p5b] deny ok (u2->u1 blocked, " +
			eStr(serr) + ")\n")
	}

	// write-denial: u2 cannot create inside u1's home either
	if cerr := fsc.Create("/home/u1/evil.txt"); cerr == nil {
		os.Stdout.WriteString("[p5b] FAIL u2 wrote into u1 home!\n")
		os.Exit(1)
	} else {
		os.Stdout.WriteString("[p5b] deny ok (u2 write->u1 blocked)\n")
	}

	os.Stdout.WriteString("[p5b] all ok\n")
}

func eStr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
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
