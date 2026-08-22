// Package kern wraps the kernel-facing imports every service needs:
// mini-WASI bits, §1 message ports, §4 input/focus, blk transport.
// Canonical "guest libc" — keep in lockstep with abi/ABI.md.
package kern

import "unsafe"

//go:wasmimport wasi_snapshot_preview1 sched_yield
func SchedYield() int32

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func argsSizesGet(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func argsGet(argv *uint32, buf *byte) int32

//go:wasmimport wasi_snapshot_preview1 fd_write
func fdWrite(fd int32, iovs *byte, iovsLen int32, nwritten *int32) int32

//go:wasmimport kernel kern_port_create
func portCreate(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func portBind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func portSend(h int32, buf *byte, len uint32) int32

//go:wasmimport kernel kern_port_recv
func portRecv(h int32, buf *byte, cap uint32) int32

// Create claims a NEW well-known name; returns handle or -1.
func Create(name string) int32 {
	b := Cstr(name)
	return portCreate(&b[0], uint32(len(b)-1)) // exclude NUL
}

// Bind attaches to an existing name; returns handle or -1.
func Bind(name string) int32 {
	b := Cstr(name)
	return portBind(&b[0], uint32(len(b)-1)) // exclude NUL
}

// SendOrBlock sends, yielding on would-block. Returns 0 ok / -1 err.
func SendOrBlock(h int32, data []byte) int32 {
	for {
		rc := portSend(h, &data[0], uint32(len(data)))
		if rc == -2 {
			SchedYield()
			continue
		}
		return rc
	}
}

// RecvNonBlocking returns a datagram or nil.
func RecvNonBlocking(h int32, cap int32) []byte {
	buf := make([]byte, cap)
	n := portRecv(h, &buf[0], uint32(cap))
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

// RecvBlocking polls until a datagram arrives.
func RecvBlocking(h int32, cap int32) []byte {
	for {
		if m := RecvNonBlocking(h, cap); m != nil {
			return m
		}
		SchedYield()
	}
}

// ---- input / focus (ABI §4) ----

//go:wasmimport kernel kern_input_recv
func inputRecv(ptr *byte, cap uint32) int32

//go:wasmimport kernel kern_focus_set
func focusSet(h int32) int32

// InputRecord is one 4-byte key event {kind,mods,codepoint}.
type InputRecord struct {
	Kind byte
	Mods byte
	CP   uint16
}

// RecvInput drains up to n key events from the focused stream.
func RecvInput(n int) []InputRecord {
	raw := make([]byte, n*4)
	got := inputRecv(&raw[0], uint32(len(raw)))
	out := make([]InputRecord, 0, got/4)
	for i := 0; i+3 < int(got); i += 4 {
		out = append(out, InputRecord{raw[i], raw[i+1],
			uint16(raw[i+2]) | uint16(raw[i+3])<<8})
	}
	return out
}

// FocusTo moves keyboard focus to the session owning the given handle.
func FocusTo(h int32) int32 { return focusSet(h) }

// ---- misc helpers ----

func Cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// Argv returns the session arguments (argv[0] = session name).
func Argv() []string {
	var argc int32
	var bl int32
	argsSizesGet(&argc, &bl)
	if argc < 1 || bl <= 0 {
		return nil
	}
	vecs := make([]uint32, argc)
	buf := make([]byte, bl)
	argsGet(&vecs[0], &buf[0])
	out := make([]string, 0, argc)
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

// ConsoleOut writes to the serial console via fd_write(1).
func ConsoleOut(s string) {
	ConsoleOutBytes([]byte(s))
}

// ConsoleOutBytes writes raw bytes to the serial console via fd_write(1).
func ConsoleOutBytes(b []byte) {
	var iov [8]byte
	p := uintptr(unsafe.Pointer(&b[0]))
	for i := 0; i < 4; i++ {
		iov[i] = byte(p >> (8 * i))
	}
	iov[4] = byte(len(b))
	iov[5] = byte(len(b) >> 8)
	iov[6] = byte(len(b) >> 16)
	iov[7] = byte(len(b) >> 24)
	var n int32
	fdWrite(1, &iov[0], 1, &n)
}
