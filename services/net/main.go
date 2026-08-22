//go:build wasip1

// net.wasm entry (AGENTS.md Phase 9): attach to the §6 packet windows
// reported by devman ENUM (class=net, ordered by instance: [0]=RX,
// [1]=TX — lane-local instantiation, see services/ABI-NOTES.md §10),
// then serve the "net" socket port forever.
//
// Address assignment: argv[1] = "<mac> <ip>" (e.g. "02:00:00:00:00:09
// 10.0.2.15"); defaults apply when absent.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o net.wasm .
package main

import (
	"errors"
	"os"
	"unsafe"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 args_sizes_get
func args_sizes_get(argc *int32, bufLen *int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

func ptrAt(off uint64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

func readAddrArg() (MAC, IP4) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	ip := MustIP("10.0.2.15")
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc >= 2 && bl > 0 {
		vecs := make([]uint32, argc)
		buf := make([]byte, bl)
		args_get(&vecs[0], &buf[0])
		start, end := int(vecs[1]), int(vecs[1])
		for end < len(buf) && buf[end] != 0 {
			end++
		}
		if start <= end && end <= len(buf) {
			arg := string(buf[start:end])
			if i := indexByteStr(arg, ' '); i > 0 {
				if m, err := ParseMAC(arg[:i]); err == nil {
					mac = m
				}
				if p, err := ParseIP(trimSpaces(arg[i:])); err == nil {
					ip = p
				}
			}
		}
	}
	return mac, ip
}

func indexByteStr(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSpaces(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	return s
}

func attachWindows() (*WindowRing, *WindowRing, error) {
	k := lib.Real()
	dm, err := lib.BindDevman(k)
	if err != nil {
		return nil, nil, err
	}
	recs, err := dm.Enum()
	if err != nil {
		return nil, nil, err
	}
	var offs []uint64
	for _, r := range recs {
		if r.Class == lib.ClassNet {
			offs = append(offs, r.WinOff)
		}
	}
	if len(offs) < 2 {
		return nil, nil, errors.New("net: expected RX+TX windows from devman")
	}
	rx, err := NewWindowRing(ptrAt(offs[0], RingSize))
	if err != nil {
		return nil, nil, err
	}
	tx, err := NewWindowRing(ptrAt(offs[1], RingSize))
	if err != nil {
		return nil, nil, err
	}
	return rx, tx, nil
}

func main() {
	os.Stdout.WriteString("[net] up\n")
	rx, tx, err := attachWindows()
	if err != nil {
		os.Stdout.WriteString("[net] " + err.Error() + "\n")
		return
	}
	mac, ip := readAddrArg()
	stack := NewStack(mac, ip, dualFeed{rx: rx, tx: tx})
	os.Stdout.WriteString("[net] serving " + ip.String() + "\n")
	ServeNet(lib.Real(), stack, nil)
}

// dualFeed splits the §6 pair into one PacketFeed for the stack.
type dualFeed struct{ rx, tx *WindowRing }

func (d dualFeed) Recv() ([]byte, bool) { return d.rx.Recv() }
func (d dualFeed) Send(f []byte) bool   { return d.tx.Send(f) }

var _ PacketFeed = dualFeed{}
