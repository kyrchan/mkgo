// shell.wasm -- interactive shell (Phase 7). Reads key events while
// focused; built-ins: echo, cat, ls, stat, kill-session. Output goes to
// the console server; file ops use kernel-routed preview1.
package main

import (
	"strings"
	"unsafe"

	lib "kernel.services/lib"
)

var consoleH int32 = -1

var consoleDead bool

func out(s string) {
	outBytes([]byte(s))
}

func outBytes(b []byte) {
	if len(b) == 0 {
		return
	}
	if consoleDead || consoleH < 0 {
		lib.ConsoleOutBytes(b)
		return
	}
	if rc := portSendRaw(consoleH, &b[0], uint32(len(b))); rc != 0 {
		consoleDead = true
		lib.ConsoleOutBytes(b)
	}
}

//go:wasmimport kernel kern_port_send
func portSendRaw(h int32, buf *byte, len uint32) int32

/* preview1 file helpers (routed by the kernel to fs.wasm) */

//go:wasmimport wasi_snapshot_preview1 path_open
func pathOpen(dirfd int32, dirflags int32, path *byte, pathLen int32,
	oflags int32, rightsBase uint64, rightsInheriting uint64,
	fdflags int32, opened *int32) int32

//go:wasmimport wasi_snapshot_preview1 fd_read
func fdRead(fd int32, iovs *byte, iovsLen int32, nread *int32) int32

//go:wasmimport wasi_snapshot_preview1 fd_close
func fdClose(fd int32) int32

//go:wasmimport kernel kern_input_recv
func inputRecv(ptr *byte, cap uint32) int32

//go:wasmimport kernel kern_focus_set
func focusSet(h int32) int32

func openRO(path string) int32 {
	var fd int32
	cb := []byte(path)
	errno := pathOpen(3, 0, &cb[0], int32(len(cb)), 0, 0, 0, 0, &fd)
	if errno != 0 {
		return -errno
	}
	return fd
}

func readFile(fd int32) []byte {
	var out []byte
	chunk := make([]byte, 512)
	for {
		var iov [8]byte
		p := uintptr(unsafe.Pointer(&chunk[0]))
		for i := 0; i < 4; i++ {
			iov[i] = byte(p >> (8 * i))
		}
		ln := len(chunk)
		iov[4] = byte(ln)
		iov[5] = byte(ln >> 8)
		iov[6] = byte(ln >> 16)
		iov[7] = byte(ln >> 24)
		var n int32
		rc := fdRead(fd, &iov[0], 1, &n)
		if rc != 0 || n <= 0 {
			break
		}
		out = append(out, chunk[:n]...)
		if n < int32(len(chunk)) {
			break
		}
	}
	return out
}

/* registry frames (§7 v1.1) */
var regSeq uint16 = 30

func registryCall(op uint16, payload []byte) []byte {
	rg := lib.Bind("registry")
	if rg < 0 {
		return nil
	}
	regSeq++
	req := make([]byte, 24+len(payload))
	req[0] = byte(op)
	req[1] = byte(op >> 8)
	req[2] = byte(regSeq)
	req[3] = byte(regSeq >> 8)
	copy(req[8:24], argv0)
	copy(req[24:], payload)
	lib.SendOrBlock(rg, req)
	for i := 0; i < 100000; i++ {
		if m := lib.RecvNonBlocking(rg, 4096); m != nil && len(m) >= 4 &&
			m[2] == byte(regSeq) && m[3] == byte(regSeq>>8) {
			return m
		}
		lib.SchedYield()
	}
	return nil
}

var argv0 string

func cmdLine(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case "echo":
		out(strings.Join(fields[1:], " ") + "\n")
	case "cat":
		if len(fields) != 2 {
			out("usage: cat <path>\n")
			return
		}
		fd := openRO(fields[1])
		if fd < 0 {
			out("cat: " + fields[1] + ": errno " +
				itoa(int(-fd)) + "\n")
			return
		}
		data := readFile(fd)
		fdClose(fd)
		outBytes(data)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			outBytes([]byte("\n"))
		}
	case "ls":
		path := "/"
		if len(fields) == 2 {
			path = fields[1]
		}
		rep := registryCall(9, nil) // placeholder: fs LS via direct port below
		_ = rep
		out("ls: not implemented yet (" + path + ")\n")
	case "stat":
		out("stat: not implemented yet\n")
	case "kill-session":
		if len(fields) != 2 {
			out("usage: kill-session <sid>\n")
			return
		}
		var sid uint32
		for _, c := range []byte(fields[1]) {
			sid = sid*10 + uint32(c-'0')
		}
		pl := make([]byte, 4)
		pl[0] = byte(sid)
		rep := registryCall(3, pl)
		if rep == nil {
			out("kill-session: no reply\n")
		} else {
			st := uint32(rep[4]) | uint32(rep[5])<<8 |
				uint32(rep[6])<<16 | uint32(rep[7])<<24
			out("kill-session rc=" + itoa(int(int32(st))) + "\n")
		}
	default:
		out(line + ": unknown command\n")
	}
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

func main() {
	argv := lib.Argv()
	if len(argv) > 0 {
		argv0 = argv[0]
	}
	if lib.Create("shell") < 0 { // focus target identity
		lib.Bind("shell")
	}
	consoleH = lib.Bind("console")
	lib.ConsoleOut("[shell] ready\n")

	line := make([]byte, 0, 256)
	for {
		recs := lib.RecvInput(16)
		if len(recs) == 0 {
			lib.SchedYield()
			continue
		}
		for _, r := range recs {
			if r.Kind != 1 {
				continue
			}
			switch r.CP {
			case '\n':
				out("\n")
				cmdLine(string(line))
				line = line[:0]
			case 8, 127:
				if len(line) > 0 {
					line = line[:len(line)-1]
				}
			default:
				if r.CP >= 32 && r.CP < 127 && len(line) < 200 {
					line = append(line, byte(r.CP))
				}
			}
		}
	}
}
