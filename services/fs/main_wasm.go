//go:build wasip1 && wasm

package main

// fs.wasm shell: WASI/kernel imports, sync exports, port server.

import (
	"os"
	"unsafe"
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

//go:wasmimport kernel kern_blk_read
func blk_read(lba uint32, buf *byte, count uint32) int32

//go:wasmimport kernel kern_blk_write
func blk_write(lba uint32, buf *byte, count uint32) int32

var replyHandles = map[string]int32{}

func replyTo(rname string, resp []byte) {
	if rname == "" {
		return
	}
	h, ok := replyHandles[rname]
	if !ok {
		h = port_bind(&cstr(rname)[0], uint32(len(rname)))
		if h < 0 {
			return
		}
		replyHandles[rname] = h
	}
	port_send(h, &resp[0], uint32(len(resp)))
}

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// ---- synchronous kernel-route exports ----

var reqBuf [4096]byte
var respBuf [8192]byte

//go:wasmexport _fsbuf
func fsBuf() uint32 { return uint32(uintptr(unsafe.Pointer(&reqBuf[0]))) }

//go:wasmexport _fsrespbuf
func fsRespBuf() uint32 { return uint32(uintptr(unsafe.Pointer(&respBuf[0]))) }

//go:wasmexport _fsreq
func fsReq(l int32, cap_ int32) int32 {
	if l < 0 || int(l) > len(reqBuf) {
		return 0
	}
	resp, _ := handleReq(reqBuf[:l])
	if int(cap_) < len(resp) {
		resp = resp[:cap_]
	}
	copy(respBuf[:], resp)
	return int32(len(resp))
}

// seedEtc provisions /etc/motd on freshly formatted disks.
func seedEtc() {
	mkdirPath("/etc")
	resp := dispatch(opOpen, 0, "/etc/motd",
		[]byte{0, 0, 0, 0, 1, 0, 0, 0})
	if g16(resp, 0) != 0 {
		os.Stdout.WriteString("[fs] motd seed open failed\n")
		return
	}
	fh := uint32(g32(resp, 2))
	text := []byte("Welcome to the capability microkernel.\n")
	wp := make([]byte, 8+len(text))
	le32(wp, 0, int(fh))
	le32(wp, 4, len(text))
	copy(wp[8:], text)
	dispatch(opWrite, 0, "", wp)
	dispatch(opClose, 0, "", []byte{byte(fh), 0, 0, 0})
}

func sessionName() string {
	var argc int32
	var bl int32
	args_sizes_get(&argc, &bl)
	if argc < 1 || bl <= 0 {
		return "fs"
	}
	vecs := make([]uint32, argc)
	buf := make([]byte, bl)
	args_get(&vecs[0], &buf[0])
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return string(buf[:end])
}

func main() {
	blkRead = func(lba uint32, buf *byte, count uint32) int32 {
		return blk_read(lba, buf, count)
	}
	blkWrite = func(lba uint32, buf *byte, count uint32) int32 {
		return blk_write(lba, buf, count)
	}
	h := port_create(&cstr("fs")[0], 2)
	if h < 0 {
		h = port_bind(&cstr("fs")[0], 2)
	}
	os.Stdout.WriteString("[fs] mounting ramdisk as ")
	os.Stdout.WriteString(sessionName())
	os.Stdout.WriteString("\n")
	rdSector(0, sect[:])
	if string(sect[54:58]) != "FAT16   " {
		fmtDisk()
		os.Stdout.WriteString("[fs] ramdisk formatted\n")
		seedEtc()
	}
	os.Stdout.WriteString("[fs] ready\n")
	buf := make([]byte, 8192)
	idle := 0
	for idle < 700000 {
		n := port_recv(h, &buf[0], uint32(len(buf)))
		if n > 0 {
			resp, rname := handleReq(append([]byte{}, buf[:n]...))
			replyTo(rname, resp)
			idle = 0
		} else {
			idle++
			sched_yield()
		}
	}
	os.Stdout.WriteString("[fs] idle exit\n")
}
