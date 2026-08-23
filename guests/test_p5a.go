// test_p5a.go -- Phase 5 gate, KERNEL-ROUTED preview1 side.
// Auth as u1 via "login", then uses ONLY plain WASI path/fd imports
// (path_open/path_create_directory-free flow: open-create -> write ->
// close -> reopen -> read) on hello.txt. fs roots it at /home/1/.
package main

import (
	"os"
	"unsafe"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport wasi_snapshot_preview1 fd_write
func fd_write(fd int32, iovs *byte, iovsLen int32, nwritten *int32) int32

//go:wasmimport wasi_snapshot_preview1 fd_read
func fd_read(fd int32, iovs *byte, iovsLen int32, nread *int32) int32

//go:wasmimport wasi_snapshot_preview1 fd_close
func fd_close(fd int32) int32

//go:wasmimport wasi_snapshot_preview1 path_open
func path_open(dirfd int32, dirflags int32, path *byte, pathLen int32,
	oflags int32, rightsBase uint64, rightsInheriting uint64,
	fdflags int32, opened *int32) int32

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
		os.Stdout.WriteString("[auth] bind failed\n")
		return false
	}
	q := port_create(&cstr(argv0)[0], uint32(len(argv0)))
	if q < 0 {
		q = port_bind(&cstr(argv0)[0], uint32(len(argv0)))
	}
	req := make([]byte, 50)
	req[0] = 1
	copy(req[2:18], user)
	copy(req[18:34], pw)
	copy(req[34:50], argv0)
	os.Stdout.WriteString("[authA] creds len=")
	os.Stdout.WriteString(itoa(len(req)))
	os.Stdout.WriteString("\n")
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

func writeFile(fd int32, buf []byte) int32 {
	iovs := make([]byte, 8)
	p := uintptr(unsafe.Pointer(&buf[0]))
	for i := 0; i < 4; i++ {
		iovs[i] = byte(p >> (8 * i))
	}
	le32(iovs, 4, len(buf))
	var n int32
	return fd_write(fd, &iovs[0], 1, &n)
}

func readFile(fd int32, dst []byte) int32 {
	p := uintptr(unsafe.Pointer(&dst[0]))
	iovs := make([]byte, 8)
	for i := 0; i < 4; i++ {
		iovs[i] = byte(p >> (8 * i))
	}
	le32(iovs, 4, len(dst))
	var n int32
	rc := fd_read(fd, &iovs[0], 1, &n)
	if rc != 0 {
		return -rc
	}
	return n
}

func le32(b []byte, o int, v int) {
	b[o] = byte(v)
	b[o+1] = byte(v >> 8)
	b[o+2] = byte(v >> 16)
	b[o+3] = byte(v >> 24)
}

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

const msgText = "hello from u1 via preview1 routing"

func main() {
	os.Stdout.WriteString("[p5a] start\n")
	if !authAs("u1", "u1") {
		os.Stdout.WriteString("[p5a] FAIL auth\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5a] auth ok (uid=1)\n")

	/* create + write */
	fd := int32(-1)
	errno := path_open(3, 0, &cstr("hello.txt")[0], 9, 1 /*CREATE*/, 0, 0, 0, &fd)
	if errno != 0 || fd < 3 {
		os.Stdout.WriteString("[p5a] FAIL open-create errno=")
		os.Stdout.WriteString(itoa(int(errno)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	wc := writeFile(fd, []byte(msgText))
	if wc != 0 {
		os.Stdout.WriteString("[p5a] FAIL write errno=")
		os.Stdout.WriteString(itoa(int(wc)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	if fd_close(fd) != 0 {
		os.Stdout.WriteString("[p5a] FAIL close\n")
		os.Exit(1)
	}

	/* reopen + read back */
	fd = -1
	errno = path_open(3, 0, &cstr("hello.txt")[0], 9, 0, 0, 0, 0, &fd)
	if errno != 0 || fd < 3 {
		os.Stdout.WriteString("[p5a] FAIL reopen errno=")
		os.Stdout.WriteString(itoa(int(errno)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	dst := make([]byte, len(msgText)+1)
	rn := readFile(fd, dst)
	fd_close(fd)
	if rn != int32(len(msgText)) || string(dst[:rn]) != msgText {
		os.Stdout.WriteString("[p5a] FAIL readback rn=")
		os.Stdout.WriteString(itoa(int(rn)))
		os.Stdout.WriteString("\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p5a] roundtrip ok bytes=")
	os.Stdout.WriteString(itoa(int(rn)))
	os.Stdout.WriteString("\n")
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
