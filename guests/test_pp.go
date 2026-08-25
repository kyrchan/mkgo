// test_pp.wasm -- Phase 4 gate payload, spawned twice ("ppa" with admin
// caps, "ppb" unprivileged). Role comes from argv[0] == session name.
//
//	ppa: create chan-a, bind chan-b, 3 ping rounds, registry LIST,
//	     KILL console, then idle-yield (isolation observation window).
//	ppb: create chan-b, bind chan-a, echo up to 8 datagrams, exit.
package main

import (
	"os"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func port_send(h int32, buf *byte, len uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h int32, buf *byte, cap uint32) int32

var argv0 string

func readArgs() {
	var argc int32
	var bl int32
	args_sizes_get(&argc, &bl)
	if argc < 1 || bl <= 0 {
		argv0 = "x"
		return
	}
	buf := make([]byte, bl)
	var vecs [1]uint32
	args_get(&vecs[0], &buf[0])
	// first arg: NUL-terminated at buf[vecs[0]-base]... base is 0 for us
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	argv0 = string(buf[:end])
}

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

/* ---- §7 request framing: {u16 op, u16 seq, payload} ---- */
/* §7 framing v1.1: {u16 op,u16 seq,u32 uid,char rname[16],payload} */
var myQ int32 = -1

func ensureQ() {
	if myQ < 0 {
		myQ = port_create(&cstr(argv0)[0], uint32(len(argv0)))
		if myQ < 0 {
			myQ = port_bind(&cstr(argv0)[0], uint32(len(argv0)))
		}
	}
}

func regList(h int32) []byte {
	ensureQ()
	req := make([]byte, 24)
	req[0] = 1 // LIST
	req[2] = 1 // seq
	copy(req[8:24], argv0)
	port_send(h, &req[0], 24)
	out := make([]byte, 4096)
	for i := 0; i < 20000; i++ {
		n := port_recv(myQ, &out[0], uint32(len(out)))
		if n >= 28 && out[2] == 1 {
			return out[:n]
		}
		sched_yield()
	}
	return nil
}

func regKill(h int32, sid uint32) int32 {
	ensureQ()
	req := make([]byte, 28)
	req[0] = 3 // KILL
	req[2] = 2 // seq
	copy(req[8:24], argv0)
	req[24] = byte(sid)
	req[25] = byte(sid >> 8)
	req[26] = byte(sid >> 16)
	req[27] = byte(sid >> 24)
	port_send(h, &req[0], 28)
	out := make([]byte, 256)
	for i := 0; i < 20000; i++ {
		n := port_recv(myQ, &out[0], uint32(len(out)))
		if n >= 28 && out[2] == 2 {
			return int32(uint32(out[24]) | uint32(out[25])<<8 |
				uint32(out[26])<<16 | uint32(out[27])<<24)
		}
		sched_yield()
	}
	return -99
}

func main() {
	readArgs()
	os.Stdout.WriteString("[")
	os.Stdout.WriteString(argv0)
	os.Stdout.WriteString("] start\n")

	switch argv0 {
	case "ppb":
		ppb()
	case "ppa":
		ppa()
	default:
		os.Stdout.WriteString("[?] unknown role\n")
	}
	os.Exit(0)
}

func ppb() {
	own := port_create(&cstr("chan-b")[0], 6)
	peer := int32(-1)
	for peer < 0 { // wait until ppa creates its channel
		peer = port_bind(&cstr("chan-a")[0], 6)
		if peer < 0 {
			sched_yield()
		}
	}
	os.Stdout.WriteString("[ppb] ready\n")
	buf := make([]byte, 4096)
	idle := 0
	n := 0
	for n < 8 && idle < 200000 {
		got := port_recv(own, &buf[0], uint32(len(buf)))
		if got > 0 {
			port_send(peer, &buf[0], uint32(got)) // echo back
			n++
			idle = 0
		} else {
			idle++
			sched_yield()
		}
	}
	os.Stdout.WriteString("[ppb] done echoed=")
	os.Stdout.WriteString(itoa(n))
	os.Stdout.WriteString("\n")
}

func ppa() {
	own := port_create(&cstr("chan-a")[0], 6)
	peer := int32(-1)
	for peer < 0 {
		peer = port_bind(&cstr("chan-b")[0], 6)
		if peer < 0 {
			sched_yield()
		}
	}
	/* ping-pong x3: send over PEER's channel, listen on our own */
	ok := 0
	ping := cstr("ping")
	for r := 1; r <= 3; r++ {
		for port_send(peer, &ping[0], 4) == -2 {
			sched_yield()
		}
		buf := make([]byte, 64)
		for i := 0; i < 100000; i++ {
			n := port_recv(own, &buf[0], uint32(len(buf)))
			if n == 4 && buf[0] == 'p' {
				ok++
				break
			}
			sched_yield()
		}
	}
	os.Stdout.WriteString("[ppa] rounds ok=")
	os.Stdout.WriteString(itoa(ok))
	os.Stdout.WriteString("\n")

	/* registry LIST */
	rg := int32(-1)
	for rg < 0 {
		rg = port_bind(&cstr("registry")[0], 8)
		if rg < 0 {
			sched_yield()
		}
	}
	rep := regList(rg)
	if len(rep) >= 28 {
		/* canonical reply: body {u32 n; rec[25]{sid,uid,state,name[16]}} @24 */
		n := uint32(rep[24]) | uint32(rep[25])<<8 | uint32(rep[26])<<16 | uint32(rep[27])<<24
		os.Stdout.WriteString("[reg] sessions=")
		os.Stdout.WriteString(itoa(int(n)))
		os.Stdout.WriteString(":")
		consoleSid := int32(-1)
		off := 28
		for i := uint32(0); i < n && off+25 <= len(rep); i++ {
			sid := uint32(rep[off]) | uint32(rep[off+1])<<8 |
				uint32(rep[off+2])<<16 | uint32(rep[off+3])<<24
			name := string(rep[off+9 : off+25])
			z := 0
			for z < len(name) && name[z] != 0 {
				z++
			}
			name = name[:z]
			os.Stdout.WriteString(name)
			os.Stdout.WriteString(",")
			if name == "console" {
				consoleSid = int32(sid)
			}
			off += 25
		}
		os.Stdout.WriteString("\n")
		/* crash isolation: kill console */
		if consoleSid >= 0 {
			rc := regKill(rg, uint32(consoleSid))
			os.Stdout.WriteString("[kill] console rc=")
			os.Stdout.WriteString(itoa(int(rc)))
			os.Stdout.WriteString("\n")
		}
	} else {
		os.Stdout.WriteString("[reg] no reply\n")
	}

	/* isolation window: keep yielding so login heartbeats hit serial */
	for i := 0; i < 1500000; i++ {
		sched_yield()
	}
	os.Stdout.WriteString("[ppa] isolation window done\n")
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
