// Package kern is the guest "libc" for the kernel-lane microkernel:
// idiomatic Go wrappers over the frozen abi/ABI.md v1 surface (message
// ports §1, input/focus §4) plus typed clients for the kernel-owned
// service ports (§7 registry/devman/power) and for lane services that
// speak the framing documented in services/ABI-NOTES.md.
//
// Service logic is written against [Kernel]; the same code compiles as
// wasm via Real() (raw //go:wasmimport bindings) and runs on the host
// under Bus()/FakeKernel() so plain `go test` exercises it with exact
// core/ports.cc semantics.
package kern

import "fmt"

// ABIVersion is the abi/ABI.md contract version these wrappers encode.
const ABIVersion = 1

// MaxMsg is the §1 datagram payload cap; MaxName the kernel port-name cap
// (core/ports.cc enforces both).
const (
	MaxMsg  = 4096
	MaxName = 15
)

// Port call results (ABI §1).
const (
	StatusOK         = int32(0)
	StatusErr        = int32(-1)
	StatusWouldBlock = int32(-2)
)

// Handle is a per-session port handle (small non-negative integer or -1).
type Handle = int32

// InvalidHandle marks create/bind failure.
const InvalidHandle Handle = -1

// Well-known port names (ABI §1 user services, §7 kernel-owned).
const (
	NameConsole  = "console"
	NameFS       = "fs"
	NameNet      = "net"
	NameLogin    = "login"
	NameShell    = "shell"
	NameRegistry = "registry"
	NameDevman   = "devman"
	NamePower    = "power"
)

// Capability bits (ABI §7 u64 mask, enforced by the kernel registry).
const (
	CapKill     uint64 = 1 << 0
	CapDevman   uint64 = 1 << 1
	CapPower    uint64 = 1 << 2
	CapFocus    uint64 = 1 << 3
	CapFSAdmin  uint64 = 1 << 4
	CapNetAdmin uint64 = 1 << 5
	CapSpawn    uint64 = 1 << 6
	CapConf     uint64 = 1 << 7 // v1.1: SETCONF right (init.wasm)
	CapPCI      uint64 = 1 << 8 // v2.0: VFIO PCI access (graphics/e1000/ahci/usb)
	CapFB       uint64 = 1 << 9 // v2.0: framebuffer modesetting/cursor

	// CapAll is the admin mask ("admin" holds every bit per AGENTS.md).
	CapAll = CapKill | CapDevman | CapPower | CapFocus |
		CapFSAdmin | CapNetAdmin | CapSpawn | CapConf | CapPCI | CapFB
)

var capNames = [...]string{
	"kill", "devman", "power", "focus", "fs_admin", "net_admin",
	"spawn", "conf", "pci", "fb",
}

// CapNames renders a capability mask as bit names (unknown bits hex).
func CapNames(mask uint64) []string {
	var out []string
	for b := 0; b < len(capNames); b++ {
		if mask&(1<<uint(b)) != 0 {
			out = append(out, capNames[b])
		}
	}
	if rest := mask &^ uint64((1<<len(capNames))-1); rest != 0 {
		out = append(out, "__0x"+hex64(rest))
	}
	return out
}

func hex64(v uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[v&0xf]
		v >>= 4
	}
	return string(b[:])
}

// SessionInfo is one registry LIST record (ABI §7).
type SessionInfo struct {
	Sid   uint32
	UID   uint32
	State uint8
	Name  string
}

// Session states (core/sched.cc enum st mirrored).
const (
	StateFree     uint8 = 0
	StateRunnable uint8 = 1
	StateRunning  uint8 = 2
	StateZombie   uint8 = 3
)

// Alive reports whether a LIST state byte denotes a live session.
func Alive(state uint8) bool { return state != StateFree && state != StateZombie }

// usernameTable is a tiny static uid → login-name map for shell display.
// Real identity lookup lives in the kernel; this is just a display
// helper so `id` and `whoami` can print a recognizable name without
// round-tripping to the fs.wasm /etc/users table.
var usernameTable = map[uint32]string{
	0: "root",
	1: "admin",
	2: "u1",
	3: "u2",
	4: "guest",
}

// Username returns a human-readable name for uid (best-effort).
// Returns "uid-<n>" for unmapped uids.
func Username(uid uint32) string {
	if n, ok := usernameTable[uid]; ok {
		return n
	}
	return fmt.Sprintf("uid-%d", uid)
}

// Kernel abstracts the guest↔kernel contact surface (ABI §1/§4). The
// error-code conventions are exactly the ABI's; helpers in this package
// turn them into Go errors where convenient.
type Kernel interface {
	// PortCreate owns a fresh endpoint named name; InvalidHandle if the
	// name exists or is malformed (one owner per name, ABI §1).
	PortCreate(name string) Handle
	// PortBind attaches to an existing name (fan-in onto its queue);
	// InvalidHandle if no such port exists yet.
	PortBind(name string) Handle
	// PortSend returns StatusOK, StatusWouldBlock (queue full) or
	// StatusErr (bad handle, empty payload, >MaxMsg).
	PortSend(h Handle, data []byte) int32
	// PortRecv never blocks: copied length, 0 if none, StatusErr on bad
	// handle. Datagrams longer than len(buf) truncate.
	PortRecv(h Handle, buf []byte) int32
	// InputRecv copies one §4 record (4 bytes) if one is queued for the
	// focused session; returns its length or 0.
	InputRecv(buf []byte) int32
	// FocusSet moves kernel focus to the session owning h's port name
	// (ABI §4; login/shell use this after auth).
	FocusSet(h Handle)
	// Yield cooperatively reschedules (WASI sched_yield).
	Yield()

	// === VFIO (ABI §12/§13/§14, v2.0) ===
	// PciRead32 reads a PCI config dword. Returns -1 on error.
	PciRead32(bus, dev, fn, offset uint32) int32
	// PciWrite32 writes a PCI config dword. Returns 0 on success, -1 on error.
	PciWrite32(bus, dev, fn, offset, val uint32) int32
	// PciMapBar maps a PCI BAR into guest linear memory. Returns window offset or -1.
	PciMapBar(bus, dev, fn, bar uint32) int64
	// PciUnmapBar unmaps a previously mapped BAR.
	PciUnmapBar(bus, dev, fn, bar uint32) int32
	// PciEnableBusmaster enables PCI bus mastering for DMA.
	PciEnableBusmaster(bus, dev, fn uint32) int32
	// PciBindIrq binds a device IRQ to a session doorbell. Returns handle or -1.
	PciBindIrq(bus, dev, fn, irqType uint32) (int32, error)
	// PciFlr issues a Function Level Reset.
	PciFlr(bus, dev, fn uint32) int32
	// FbSetMode sets display mode (needs CAP_FB). Returns 0 on success.
	FbSetMode(w, h, bpp uint32) int32
	// FbSetCursor sets hardware cursor position (needs CAP_FB).
	FbSetCursor(x, y uint32) int32
	// FbPresent flips the guest framebuffer window to the physical LFB (needs CAP_FB).
	FbPresent() int32
	// DoorbellWait blocks until doorbell fires or timeout. Returns 0 fired, 1 timeout, -1 err.
	DoorbellWait(handle, timeoutMs uint32) int32
	// DevmanEnum returns PCI device info for class 10 (VFIO) devices.
	DevmanEnum() []PciDeviceInfo

	// HasClock reports whether the kernel exposes a millisecond-resolution
	// clock (clock_time_get over TSC). True on production guests; tests
	// can override to false to skip time-based busy waits.
	HasClock() bool
	// ClockMs returns monotonic milliseconds since boot.
	ClockMs() uint64
}

// PciDeviceInfo is a PCI device record from devman ENUM.
type PciDeviceInfo struct {
	Bus, Dev, Fn uint8
	Vendor, Device uint16
}
