// test_p5b.go -- Phase 5 gate, DIRECT-PORT route (guests speak to "fs"
// directly per the Phase-5 routing decision). Auth as u2, then framed
// ops on b.txt under /home/2/, DELETE + verify-gone, then attempt to
// open u1's file -- fs rooting makes it invisible => denial on serial.
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

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func port_send(h int32, buf *byte, len uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h int32, buf *byte, cap uint32) int32

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

func authAs(user, pw string) bool {
	readArgs()
	lh := int32(-1)
	for i := 0; i < 200000 && lh < 0; i++ {
		lh = port_bind(&cstr("login")[0], 5)
		if lh < 0 {
			sched_yield()
		}
	}
	if lh < 0 {
		os.Stdout.WriteString("[authB] bind failed\n")
		return false
	}
	q := port_create(&cstr(argv0)[0], uint32(len(argv0)))
	if q < 0 {
		q = port_bind(&cstr(argv0)[0], uint32(len(argv0)))
	}
	req := make([]byte, 24, 24+2+len(user)+len(pw))
	req[0] = 1
	req[3] = 1 // seq
	copy(req[8:24], argv0) // rname
	req = append(req, byte(len(user)))
	req = append(req, user...)
	req = append(req, byte(len(pw)))
	req = append(req, pw...)
	copy(req[34:50], argv0)
	port_send(lh, &req[0], uint32(len(req)))
	out := make([]byte, 64)
	for i := 0; i < 200000; i++ {
		n := port_recv(q, &out[0], uint32(len(out)))
		if n >= 2 {
			return out[0] == 0 && out[1] == 0
		}
		sched_yield()
	}
	return false
}

/* fs framing: {u16 op, u16 seq, u32 uid(ignored by fs--it uses its own
 * view of the requester... v1: fs roots by uid field), path, payload} */
var seq uint16 = 10

func fsReq(fsH int32, op uint16, path string, payload []byte) []byte {
	/* frame: {u16 op,u16 seq,u32 uid,char rname[16],u16 plen,path,payload} */
	req := make([]byte, 26+len(path)+len(payload))
	req[0] = byte(op)
	req[1] = byte(op >> 8)
	seq++
	req[2] = byte(seq)
	req[3] = byte(seq >> 8)
	req[4] = 2 // u2 stamps its own uid for the direct route
	copy(req[8:24], argv0)
	req[24] = byte(len(path))
	req[25] = byte(len(path) >> 8)
	copy(req[26:], path)
	copy(req[26+len(path):], payload)
	port_send(fsH, &req[0], uint32(len(req)))
	out := make([]byte, 8192)
	for i := 0; i < 50000; i++ {
		n := port_recv(qH, &out[0], uint32(len(out)))
		if n >= 2 { // one outstanding op: first reply is ours
			return out[:n]
		}
		sched_yield()
	}
	return nil
}

func seqMatch(b []byte, want uint16) int {
	if len(b) >= 4 && (int(b[2])|int(b[3])<<8) == int(want) {
		return len(b)
	}
	return -1
}

var qH int32 = -1

func fhOf(rep []byte) uint32 {
	return uint32(rep[2]) | uint32(rep[3])<<8 | uint32(rep[4])<<16 | uint32(rep[5])<<24
}

func errnoOf(rep []byte) int {
	if len(rep) < 2 {
		return -99
	}
	return int(rep[0]) | int(rep[1])<<8
}

func le32(b []byte, o int, v int) {
	b[o] = byte(v)
	b[o+1] = byte(v >> 8)
	b[o+2] = byte(v >> 16)
	b[o+3] = byte(v >> 24)
}
func g32(b []byte, o int) int {
	return int(b[o]) | int(b[o+1])<<8 | int(b[o+2])<<16 | int(b[o+3])<<24
}

const bText = "b-file owned by u2"

var argv0 string

func readArgs() {
	var argc int32
	var bl int32
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

func main() {
	readArgs()
	os.Stdout.WriteString("[p5b] start\n")
	qH = port_create(&cstr(argv0)[0], uint32(len(argv0)))
	if !authAs("u2", "u2") {
		os.Stdout.WriteString("[p5b] FAIL auth\n")
		os.Exit(1)
	}
	fsH := int32(-1)
	for i := 0; i < 200000 && fsH < 0; i++ {
		fsH = port_bind(&cstr("fs")[0], 2)
		if fsH < 0 {
			sched_yield()
		}
	}
	if fsH < 0 {
		os.Stdout.WriteString("[p5b] FAIL no fs server\n")
		os.Exit(1)
	}

	/* create+write via OPEN(create)+WRITE */
	rep := fsReq(fsH, 1, "b.txt", []byte{0, 0, 0, 0, 1, 0, 0, 0})
	if errnoOf(rep) != 0 {
		os.Stdout.WriteString("[p5b] FAIL open-create errno=")
		os.Stdout.WriteString(itoa(errnoOf(rep)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	fh := fhOf(rep)
	wp := make([]byte, 8+len(bText))
	le32(wp, 0, int(fh))
	le32(wp, 4, len(bText))
	copy(wp[8:], bText)
	rep = fsReq(fsH, 4, "", wp)
	if errnoOf(rep) != 0 {
		os.Stdout.WriteString("[p5b] FAIL write errno=")
		os.Stdout.WriteString(itoa(errnoOf(rep)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	portClose(fsH, fh)

	/* read back */
	rep = fsReq(fsH, 1, "b.txt", []byte{0, 0, 0, 0, 0})
	if errnoOf(rep) != 0 {
		os.Stdout.WriteString("[p5b] FAIL reopen errno=")
		os.Stdout.WriteString(itoa(errnoOf(rep)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	fh = fhOf(rep)
	rp := make([]byte, 8)
	le32(rp, 0, int(fh))
	le32(rp, 4, len(bText))
	rep = fsReq(fsH, 3, "", rp)
	if errnoOf(rep) != 0 || g32(rep, 2) != len(bText) ||
		string(rep[6:6+len(bText)]) != bText {
		os.Stdout.WriteString("[p5b] FAIL readback errno=")
		os.Stdout.WriteString(itoa(errnoOf(rep)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	portClose(fsH, fh)
	os.Stdout.WriteString("[p5b] write/read ok bytes=")
	os.Stdout.WriteString(itoa(g32(rep, 2)))
	os.Stdout.WriteString("\n")

	/* delete + verify gone */
	rep = fsReq(fsH, 7, "b.txt", nil)
	if errnoOf(rep) != 0 {
		os.Stdout.WriteString("[p5b] FAIL delete errno=")
		os.Stdout.WriteString(itoa(errnoOf(rep)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	rep = fsReq(fsH, 5, "b.txt", nil)
	if errnoOf(rep) == 0 {
		os.Stdout.WriteString("[p5b] FAIL still exists after delete\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5b] delete verified\n")

	/* cross-user denial: as u2, try u1's file */
	rep = fsReq(fsH, 5, "home/1/hello.txt", nil)
	if errnoOf(rep) == 0 {
		os.Stdout.WriteString("[p5b] FAIL u2 saw u1 file!\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5b] deny ok (u2->u1 blocked, errno=")
	os.Stdout.WriteString(itoa(errnoOf(rep)))
	os.Stdout.WriteString(")\n")
	os.Stdout.WriteString("[p5b] all ok\n")
}

func portClose(fsH int32, fh uint32) {
	p := make([]byte, 4)
	le32(p, 0, int(fh))
	fsReq(fsH, 2, "", p)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
