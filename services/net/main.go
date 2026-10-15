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

//go:wasmimport wasi_snapshot_preview1 random_get
func random_get(buf *byte, bufLen int32) int32

//go:wasmimport wasi_snapshot_preview1 args_get
func args_get(argv *uint32, buf *byte) int32

// random_get is declared raw on purpose. Go's crypto/rand would work and
// would also link crypto/internal/fips140/drbg.memory -- a 32 MiB BSS for
// four bytes of entropy (MEMORY.md GO-CRYPTO-TAX). The frozen WASI profile
// already provides the entropy; there is no reason to pay for a second
// source in guest memory.
var randomErr = errors.New("net: random_get unavailable")

func entropy(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if rc := random_get(&b[0], int32(len(b))); rc != 0 {
		return randomErr
	}
	return nil
}

func ptrAt(off uint64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

func readAddrArg() (MAC, IP4, bool) {
	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	ip := MustIP("10.0.2.15")
	pinned := false
	var argc, bl int32
	args_sizes_get(&argc, &bl)
	if argc >= 2 && bl > 0 {
		buf := make([]byte, bl)
		var vecs [1]uint32
		args_get(&vecs[0], &buf[0])
		// buf holds argc sequential NUL-terminated strings; entry 1 is the
		// mac/ip config (entry 0 = program name). vecs[] are linear
		// offsets, not slice indices -- split buf directly.
		start := 0
		arg := ""
		for i := 0; i < int(argc) && start < len(buf); i++ {
			end := start
			for end < len(buf) && buf[end] != 0 {
				end++
			}
			if i == 1 {
				arg = string(buf[start:end])
			}
			start = end + 1
		}
		if arg != "" {
			if i := indexByteStr(arg, ' '); i > 0 {
				if m, err := ParseMAC(arg[:i]); err == nil {
					mac = m
				}
				if p, err := ParseIP(trimSpaces(arg[i:])); err == nil {
					ip = p
					// An address on the command line is a deliberate pin:
					// it reproduces a fixed-topology boot and must not be
					// overridden by a lease.
					pinned = true
				}
			}
		}
	}
	return mac, ip, pinned
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
	// Store devman-reported offsets for dualFeed to use.
	setDevmanOffsets(offs)
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

// Window bases inside this session's linear memory (devman-reported;
// pinned instantiation, services/ABI-NOTES.md §10). Slices must be
// RE-DERIVED on every access: wasm3 can relocate linear memory when the
// Go heap grows, which would silently strand cached pointers.
// These are the fallback constants used when devman is unavailable
// (e.g. host tests); production boot always uses devman-reported offsets.
const (
	rxWinBase = 0x4000000 // 64 MiB
	txWinBase = rxWinBase + RingSize
)

// devmanOffsets stores the RX/TX window offsets reported by devman
// at boot. If devman enumeration succeeded, these override the
// hardcoded constants in dualFeed.
var devmanOffsets [2]uint64
var devmanOffsetsValid bool

func setDevmanOffsets(offs []uint64) {
	if len(offs) >= 2 {
		devmanOffsets[0] = offs[0]
		devmanOffsets[1] = offs[1]
		devmanOffsetsValid = true
	}
}

func rxBase() uint64 {
	if devmanOffsetsValid {
		return devmanOffsets[0]
	}
	return rxWinBase
}

func txBase() uint64 {
	if devmanOffsetsValid {
		return devmanOffsets[1]
	}
	return txWinBase
}

func main() {
	os.Stdout.WriteString("[net] up\n")
	if _, _, err := attachWindows(); err != nil {
		os.Stdout.WriteString("[net] " + err.Error() + "\n")
		return
	}
	mac, ip, pinned := readAddrArg()
	stack := NewStack(mac, ip, dualFeed{})

	// Prefer a lease. The static address was a placeholder that happened
	// to be what SLIRP offers, so the guest was sitting on the right
	// address by luck; a DHCP client that never asks for one is not a
	// client. An explicitly pinned address (argv) still wins, which is the
	// escape hatch for reproducing a fixed-topology boot.
	var lease *Lease
	if !pinned {
		ip, lease = acquireLease(stack, mac)
	}
	os.Stdout.WriteString("[net] serving " + ip.String() + "\n")
	k := lib.Real()
	// The renewer re-runs DORA. It cannot reuse the startup xid: a
	// transaction id is per exchange, and reusing one across a renewal
	// invites a server to confuse the two.
	// NewRenewer is the production renewal step: fresh xid, DORA, adopt
	// address and routing. It lives outside main so the tests can run it.
	ServeNetRenewing(k, stack, nil, lease, NewRenewer(stack, entropy))
}

// acquireLease runs DORA and adopts the result, or returns the stack's
// current address. The stack ARPs every destination (SLIRP answers for
// anything), so adopting a lease is setting one field -- there is no
// netmask or route table to reconcile. Both outcomes are reported, because
// "serving 10.0.2.15" means something quite different depending on whether
// a server granted it or a constant did.
func acquireLease(stack *Stack, mac MAC) (IP4, *Lease) {
	// Bounded: 3 attempts of each request, and a wait budget small enough
	// that a network with no DHCP server costs this a few seconds rather
	// than stalling the boot.
	xid, xerr := NewXID(entropy)
	if xerr != nil {
		// No entropy means no safe transaction id. A fixed one is exactly
		// the bug NewXID exists to prevent, so serve statically and say so
		// rather than pretending to be a DHCP client.
		os.Stdout.WriteString("[net] dhcp no entropy (" + xerr.Error() + "), static " + stack.IP.String() + "\n")
		return stack.IP, nil
	}
	lease, err := stack.DHCPAcquire(xid, "net", DefaultDHCPOptions(), 3, 20000)
	if err != nil {
		os.Stdout.WriteString("[net] dhcp no lease (" + err.Error() + "), static " + stack.IP.String() + "\n")
		return stack.IP, nil
	}
	stack.IP = lease.YIAddr
	// Adopt the subnet and route from the lease. Until this, every
	// destination was ARP-resolved directly, which only ever worked
	// because SLIRP answers ARP for any address; with a real mask, an
	// off-subnet destination is sent to the gateway instead.
	stack.Netmask = lease.Netmask
	stack.Gateway = lease.Router
	out := "[net] dhcp lease " + lease.YIAddr.String() + " xid=" + hex8(xid)
	if !lease.Netmask.IsZero() {
		out += " mask " + lease.Netmask.String()
	}
	if !lease.Router.IsZero() {
		out += " router " + lease.Router.String()
	}
	if len(lease.DNS) > 0 {
		out += " dns " + lease.DNS[0].String()
	}
	if lease.LeaseTime > 0 {
		out += " lease " + itoa(int(lease.LeaseTime)) + "s"
	}
	os.Stdout.WriteString(out + "\n")
	// Renewal needs a clock. Without one the lease is still usable but can
	// never be renewed, and Lease says so rather than guessing a deadline.
	var clock func() uint64
	if k := lib.Real(); k.HasClock() {
		clock = func() uint64 { return k.ClockMs() }
	}
	return lease.YIAddr, NewLease(lease, clock)
}

// hex8 renders an xid so the serial log shows it is not a constant.
func hex8(v uint32) string {
	const digits = "0123456789abcdef"
	b := []byte("0x00000000")
	for i := 0; i < 8; i++ {
		b[9-i] = digits[(v>>(4*i))&0xf]
	}
	return string(b)
}

// dualFeed splits the §6 pair into one PacketFeed for the stack.
// Uses devman-reported offsets when available, falling back to
// the hardcoded constants (host tests / no-devman path).
type dualFeed struct{}

func (d dualFeed) Recv() ([]byte, bool) {
	r, err := NewWindowRing(ptrAt(rxBase(), RingSize))
	if err != nil {
		return nil, false
	}
	return r.Recv()
}

func (d dualFeed) Send(f []byte) bool {
	t, err := NewWindowRing(ptrAt(txBase(), RingSize))
	if err != nil {
		return false
	}
	return t.Send(f)
}

var _ PacketFeed = dualFeed{}
