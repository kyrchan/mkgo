//go:build !wasip1

package kern

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Bus is the host-side Kernel: it mirrors core/ports.cc exactly —
// one shared FIFO queue per named port object (create owns, bind aliases),
// send of empty/oversize payloads → StatusErr, queue-full → WouldBlock,
// recv truncates and never blocks, 32-deep ring.
//
// It lets every service's logic run under plain `go test` with kernel-
// exact port semantics before any wasm wrap.
type Bus struct {
	mu      sync.Mutex
	ports   map[string]*busPort
	handles map[Handle]*busPort
	nextH   Handle

	// §4 input/focus state. InjectInput queues raw 4-byte records;
	// Focused records the port name targeted by the last FocusSet.
	InputQ  [][]byte
	Focused string

	closed bool
}

type busPort struct {
	name   string
	queue  [][]byte
	kernel bool // §7 endpoint served inline by FakeKernel
}

// NewBus returns an empty bus (no ports; create/bind as needed).
func NewBus() *Bus {
	return &Bus{
		ports:   make(map[string]*busPort),
		handles: make(map[Handle]*busPort),
	}
}

func validName(name string) bool {
	return len(name) > 0 && len(name) <= MaxName
}

func (b *Bus) PortCreate(name string) Handle {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !validName(name) {
		return InvalidHandle
	}
	if _, exists := b.ports[name]; exists {
		return InvalidHandle // one owner per name
	}
	p := &busPort{name: name}
	b.ports[name] = p
	h := b.allocH(p)
	return h
}

func (b *Bus) PortBind(name string) Handle {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !validName(name) {
		return InvalidHandle
	}
	p, ok := b.ports[name]
	if !ok {
		return InvalidHandle // bind requires an existing owned name
	}
	return b.allocH(p)
}

func (b *Bus) allocH(p *busPort) Handle {
	for {
		h := b.nextH
		b.nextH++
		if _, used := b.handles[h]; !used {
			b.handles[h] = p
			return h
		}
	}
}

func (b *Bus) PortSend(h Handle, data []byte) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.handles[h]
	if !ok || len(data) == 0 || len(data) > MaxMsg {
		return StatusErr
	}
	if len(p.queue) >= 32 {
		return StatusWouldBlock
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	p.queue = append(p.queue, cp)
	return StatusOK
}

func (b *Bus) PortRecv(h Handle, buf []byte) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.handles[h]
	if !ok || len(buf) == 0 {
		return StatusErr
	}
	if len(p.queue) == 0 {
		return 0
	}
	m := p.queue[0]
	p.queue = p.queue[1:]
	n := len(m)
	if n > len(buf) {
		n = len(buf)
	}
	copy(buf[:n], m[:n])
	return int32(n)
}

func (b *Bus) InputRecv(buf []byte) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(buf) == 0 || len(b.InputQ) == 0 {
		return 0
	}
	rec := b.InputQ[0]
	b.InputQ = b.InputQ[1:]
	n := len(rec)
	if n > len(buf) {
		n = len(buf)
	}
	copy(buf[:n], rec[:n])
	return int32(n)
}

func (b *Bus) FocusSet(h Handle) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.handles[h]; ok {
		b.Focused = p.name
	}
}

// Yield cooperatively reschedules; on the host this parks briefly so
// concurrent service loops and test drivers interleave the way they do
// across real kernel quanta (a pure Gosched spin starves peers under
// load and made RPC budgets nondeterministic).
func (b *Bus) Yield() {
	runtime.Gosched()
	time.Sleep(20 * time.Microsecond)
}

// HasClock is false on the bare bus (no kernel clock modelled; tests that
// need time use FakeKernel or wall clock explicitly).
func (b *Bus) HasClock() bool { return false }

// ClockMs returns 0 on the bare bus.
func (b *Bus) ClockMs() uint64 { return 0 }

// === VFIO stubs (host test) — return success/capability-denied ===
// hostTestCaps is a package-level capability mask used by host test stubs
// (b.Cur was removed from Bus; tests that need capability gating set this).
var hostTestCaps uint64 = CapPCI | CapFB | CapDevman

func (b *Bus) PciRead32(bus, dev, fn, offset uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	// Fake: return a plausible vendor ID for bus 0 dev 0 fn 0 (host bridge)
	if bus == 0 && dev == 0 && fn == 0 && offset == 0 {
		return 0x12345678 // Intel host bridge (fits int32)
	}
	return 0x10ec8139 // Realtek NIC (common QEMU dev)
}

func (b *Bus) PciWrite32(bus, dev, fn, offset, val uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	return 0
}

func (b *Bus) PciMapBar(bus, dev, fn, bar uint32) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	// Return a fake window offset
	return 0x5000000 + int64(bar)*0x100000
}

func (b *Bus) PciUnmapBar(bus, dev, fn, bar uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	return 0
}

func (b *Bus) PciEnableBusmaster(bus, dev, fn uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	return 0
}

func (b *Bus) PciBindIrq(bus, dev, fn, irqType uint32) (int32, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1, fmt.Errorf("no CAP_PCI")
	}
	return 1, nil
}

func (b *Bus) PciFlr(bus, dev, fn uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	return 0
}

func (b *Bus) FbSetMode(w, h, bpp uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapFB == 0 {
		return -1
	}
	return 0
}

func (b *Bus) FbSetCursor(x, y uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapFB == 0 {
		return -1
	}
	return 0
}

func (b *Bus) DoorbellWait(handle, timeoutMs uint32) int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapPCI == 0 {
		return -1
	}
	// Stub: return timeout (no real IRQ)
	return 1
}

func (b *Bus) FbPresent() int32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapFB == 0 {
		return -1
	}
	// Stub: present succeeds
	return 0
}

func (b *Bus) DevmanEnum() []PciDeviceInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostTestCaps&CapDevman == 0 {
		return nil
	}
	// Return fake PCI devices for testing
	return []PciDeviceInfo{
		{Bus: 0, Dev: 0, Fn: 0, Vendor: 0x8086, Device: 0x1234},
		{Bus: 0, Dev: 2, Fn: 0, Vendor: 0x1234, Device: 0x5678},
		{Bus: 0, Dev: 3, Fn: 0, Vendor: 0x10ec, Device: 0x8139},
	}
}

// ---- test conveniences ----

// SendTo appends a datagram directly to the named port (test driver).
func (b *Bus) SendTo(name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.ports[name]
	if !ok {
		return ErrBadHandle
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	p.queue = append(p.queue, cp)
	return nil
}

// Drain pops everything currently queued on name (test assertions).
func (b *Bus) Drain(name string) [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.ports[name]
	if !ok {
		return nil
	}
	out := p.queue
	p.queue = nil
	return out
}

// HasPort reports whether a name is taken (create-vs-bind ordering).
func (b *Bus) HasPort(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.ports[name]
	return ok
}

// ---- FakeKernel: Bus + inline §7 endpoints mirroring core/kernsvc.cc ----

// FakeSession is one registry-listed session.
type FakeSession struct {
	Sid     uint32
	UID     uint32
	Name    string
	Capmask uint64
	CapSource uint8 // 0=login, 1=chcaps, 2=init
	State   uint8
}

// SpawnFn customizes SPAWN; returning nil denies. Defaults to creating a
// session named after the module with the requested mask.
type SpawnFn func(fk *FakeKernel, name, path string, mask uint64, args []string) *FakeSession

// FakeKernel serves "registry"/"devman"/"power" inline at PortSend time,
// exactly like kernsvc_dispatch: replies land on the sending handle,
// rejections produce [audit] lines and (per ABI §7) no reply for
// unknown ops.
type FakeKernel struct {
	Bus

	Sessions    []*FakeSession
	Cur         *FakeSession      // identity attributed to sends from the test
	Audit       []string          // captured audit lines
	Knobs       map[string]uint64 // applied via SETCONF (v1.1)
	KnobsByIdx  [8]uint64         // Phase 19: knob store by index
	SpawnHook   SpawnFn
	OnPower     func(op uint16)
	// Phase 15 observability stand-ins (registry ops 8/9).
	SysMemTotal  uint64 // SYSSTAT mem_total (0 = default 512MiB)
	SysMemUsed   uint64 // SYSSTAT mem_used
	SysQuantumUs uint32 // SYSSTAT quantum_us (0 = default 5000)
	SysNCPUs     uint32 // SYSSTAT ncpus (0 = default 1)
	LogText      string // LOGDUMP retained stream

	nextSid uint32

	// ForceUID overrides sender-uid stamping when set (tests simulate
	// multiple sessions against one bus without goroutine juggling).
	ForceUID    uint32
	forceUIDSet bool
}

// HasClock is false in host tests; the kernel clock is not modelled
// here, so sleep() in shell falls back to time.Sleep.
func (FakeKernel) HasClock() bool { return false }

// ClockMs returns 0 in host tests; the real kernel has TSC->ns.
func (FakeKernel) ClockMs() uint64 { return 0 }

// NewFakeKernel boots the three kernel-owned endpoints and seeds the
// boot sessions (sid 0 = kernel itself).
func NewFakeKernel() *FakeKernel {
	fk := &FakeKernel{Bus: *NewBus(), nextSid: 1}
	for _, n := range []string{NameRegistry, NameDevman, NamePower} {
		fk.ports[n] = &busPort{name: n, kernel: true}
	}
	fk.Sessions = append(fk.Sessions, &FakeSession{Sid: 0, UID: 0, Name: "kernel", Capmask: CapAll, State: StateRunning})
	return fk
}

// As returns a function scoping sends to the given uid (defer-style):
//
//	restore := fk.As(1001)
//	...client calls...
//	restore()
func (fk *FakeKernel) As(uid uint32) func() {
	prevUID, prevSet := fk.ForceUID, fk.forceUIDSet
	fk.ForceUID, fk.forceUIDSet = uid, true
	return func() { fk.ForceUID, fk.forceUIDSet = prevUID, prevSet }
}

// AddSession registers a session (boot-preload stand-in for console.wasm
// etc.) and returns it. The session's well-known port name is created
// too when free — modeling a module that binds its own name at startup.
func (fk *FakeKernel) AddSession(name string, uid uint32, mask uint64) *FakeSession {
	s := &FakeSession{Sid: fk.nextSid, UID: uid, Name: name, Capmask: mask, CapSource: 2, State: StateRunning}
	fk.nextSid++
	fk.Sessions = append(fk.Sessions, s)
	if _, exists := fk.ports[name]; !exists && len(name) <= MaxName {
		fk.ports[name] = &busPort{name: name}
	}
	return s
}

func (fk *FakeKernel) auditf(format string, args ...any) {
	fk.Audit = append(fk.Audit, fmt.Sprintf(format, args...))
}

func knobName(idx uint8) string {
	switch idx {
	case 0:
		return "quantum_us"
	case 1:
		return "log_level"
	case 2:
		return "audit_mask"
	default:
		return ""
	}
}

// PortSend intercepts sends to kernel endpoints and dispatches inline;
// everything else defers to the shared queue semantics.
func (fk *FakeKernel) PortSend(h Handle, data []byte) int32 {
	fk.mu.Lock()
	p, ok := fk.handles[h]
	var reply []byte
	rc := int32(StatusOK)
	if !ok {
		rc = StatusErr
	} else if len(data) == 0 || len(data) > MaxMsg {
		rc = StatusErr
	} else if p.kernel {
		reply = fk.dispatch(p.name, data)
	} else if len(p.queue) >= 32 {
		rc = StatusWouldBlock
	} else {
		cp := make([]byte, len(data))
		copy(cp, data)
		if len(cp) >= 8 {
			if fk.forceUIDSet {
				Put32(cp[4:8], fk.ForceUID) // v1.1: kernel stamps sender uid
			} else if fk.Cur != nil {
				Put32(cp[4:8], fk.Cur.UID)
			}
		}
		p.queue = append(p.queue, cp)
	}
	fk.mu.Unlock()

	if reply != nil {
		// mirror ports_kernel_enqueue onto the SENDING handle
		fk.mu.Lock()
		if hp, ok := fk.handles[h]; ok && len(hp.queue) < 32 {
			hp.queue = append(hp.queue, reply)
		}
		fk.mu.Unlock()
	}
	return rc
}

// dispatch mirrors the v1.1 kernel endpoints: canonical datagram header
// (payload @24), replies under the same header on the sending handle.
func (fk *FakeKernel) dispatch(epname string, data []byte) []byte {
	if len(data) < CanonicalHeaderLen {
		return nil
	}
	op := Get16(data[0:2])
	seq := Get16(data[2:4])
	payload := data[CanonicalHeaderLen:]
	me := fk.Cur
	if me == nil {
		// sends without a test-declared identity behave like an
		// anonymous zero-capability session
		me = &FakeSession{Sid: 0xFFFFFFFF, UID: 0xFFFFFFFF}
	}

	// rep builds a canonical-header reply of total length n (>= 28).
	rep := func(n int) []byte {
		r := make([]byte, n)
		Put16(r, op)
		Put16(r[2:], seq)
		return r
	}

	switch epname {
	case NameRegistry:
		switch op {
		case OpRegistryList:
			// core/kernsvc.cc caps LIST at 12 records with NO
			// truncation flag — mirror that so supervisors built
			// against this model handle saturation honestly.
			const listCap = 12
			n := len(fk.Sessions)
			if n > listCap {
				n = listCap
			}
r := rep(28 + 26*n)
		Put32(r[24:], uint32(n))
		off := 28
		for _, s := range fk.Sessions[:n] {
			Put32(r[off:], s.Sid)
			Put32(r[off+4:], s.UID)
			r[off+8] = s.State
			copy(r[off+9:off+25], pad16(s.Name))
			r[off+25] = s.CapSource // Phase 19: cap source byte
			off += 26
		}
		return r
		case OpRegistryCaps:
			sid := uint32(0xFFFFFFFF)
			if len(payload) >= 4 {
				sid = Get32(payload)
			}
			mask := uint64(0)
			for _, s := range fk.Sessions {
				if s.Sid == sid {
					mask = s.Capmask
				}
			}
			n := uint32(popcount(mask & ((1 << 8) - 1)))
			r := rep(28 + 12*int(n))
			Put32(r[24:], n)
			off := 28
			for b := uint(0); b < 8; b++ {
				if mask&(1<<b) != 0 {
					Put32(r[off:], uint32(b))
					Put64(r[off+4:], 1<<b)
					off += 12
				}
			}
			return r
		case OpRegistryKill:
			// core/sched.cc sched_kill: always replies; -1 on
			// nosession/cap-deny (audited), 0 kills to zombie.
			sid := Get32(payload)
			rc := int32(-1)
			found := false
			for _, s := range fk.Sessions {
				if s.Sid == sid {
					found = true
					if me.Capmask&CapKill != 0 {
						s.State = StateZombie
						rc = 0
					}
				}
			}
			switch {
			case !found:
				fk.auditf("[audit] sid=%d op=KILL reason=nosession target=registry", me.Sid)
			case me.Capmask&CapKill == 0:
				fk.auditf("[audit] sid=%d op=KILL reason=cap target=registry", me.Sid)
			}
			r := rep(28)
			Put32(r[24:], uint32(rc))
			return r
		case OpRegistryLogin:
			// v1.1: caller must own the "login" well-known name; the
			// named session receives uid+capmask.
			if me.Name != "login" {
				fk.auditf("[audit] sid=%d op=LOGIN reason=owner target=registry", me.Sid)
				return nil
			}
			name := cstr16(padName(string(payload[0:16])))
			uid := Get32(payload[16:20])
			mask := uint64(Get32(payload[20:24]))
			rc := int32(-1)
			for _, s := range fk.Sessions {
				if s.Name == name {
					s.UID = uid
					s.Capmask = mask
					s.CapSource = 0 // login-issued
					rc = 0
				}
			}
			r := rep(28)
			Put32(r[24:], uint32(rc))
			return r
		case OpRegistrySetconf:
			if me.Capmask&CapConf == 0 {
				fk.auditf("[audit] sid=%d op=SETCONF reason=cap target=registry", me.Sid)
				return nil
			}
			key := cstr16(padName(string(payload[0:16])))
			val := Get64(payload[16:24])
			if fk.Knobs == nil {
				fk.Knobs = make(map[string]uint64)
			}
			fk.Knobs[key] = val
			r := rep(28)
			Put32(r[24:], 0)
			return r
		case OpRegistrySpawn:
			if me.Capmask&CapSpawn == 0 {
				// kernsvc: break → default → audited, NO reply
				fk.auditf("[audit] sid=%d op=SPAWN reason=cap target=registry", me.Sid)
				return nil
			}
			name := cstr16(pad16(string(payload[spawnNameOff : spawnNameOff+16])))
			path := cstr16(pad64(string(payload[spawnPathOff : spawnPathOff+64])))
			mask := uint64(Get32(payload[spawnMaskOff:]))
			if mask&^me.Capmask != 0 {
				fk.auditf("[audit] sid=%d op=SPAWN reason=cap target=registry", me.Sid)
				r := rep(28)
				Put32(r[24:], 0xFFFFFFFF)
				return r
			}
			var args []string
			if len(payload) >= spawnArgsOff {
				raw := payload[spawnArgsOff:]
				for _, a := range strings.Split(string(raw), "\x00") {
					if a != "" {
						args = append(args, a)
					}
				}
			}
			var ns *FakeSession
			if fk.SpawnHook != nil {
				ns = fk.SpawnHook(fk, name, path, mask, args)
			} else {
				ns = fk.AddSession(name, me.UID, mask)
			}
			r := rep(28)
			if ns == nil {
				Put32(r[24:], 0xFFFFFFFF)
			} else {
				Put32(r[24:], ns.Sid)
			}
			return r
		case OpRegistrySysstat:
			mtot := fk.SysMemTotal
			if mtot == 0 {
				mtot = 0x20000000
			}
			q := fk.SysQuantumUs
			if q == 0 {
				q = 5000
			}
			ncpus := fk.SysNCPUs
			if ncpus == 0 {
				ncpus = 1
			}
			r := rep(24 + 4 + 16 + 4 + 1 + 4)
			Put32(r[24:], 0)
			Put64(r[28:], mtot)
			Put64(r[36:], fk.SysMemUsed)
			Put32(r[44:], q)
			r[48] = 1 // kernel default preempt_on=1
			Put32(r[49:], ncpus)
			return r
		case OpRegistryLogdump:
			var off uint64
			if len(payload) >= 8 {
				off = Get64(payload)
			}
			total := uint64(len(fk.LogText))
			var data string
			if off < total {
				data = fk.LogText[off:]
				if len(data) > 4000 {
					data = data[:4000]
				}
			}
			r := rep(24 + 4 + 16 + len(data))
			Put32(r[24:], 0)
			Put64(r[28:], total)
			Put64(r[36:], off)
			copy(r[44:], data)
			return r
		case 10: // CHCAPS
			if me.Capmask&CapPower == 0 {
				fk.auditf("[audit] sid=%d op=CHCAPS reason=cap target=registry", me.Sid)
				return nil
			}
			if len(payload) < 12 {
				return nil
			}
			tsid := Get32(payload)
			clear := uint64(Get32(payload[4:]))
			set := uint64(Get32(payload[8:]))
			var t *FakeSession
			for _, ss := range fk.Sessions {
				if ss.Sid == tsid {
					t = ss
					break
				}
			}
			if t == nil {
				return nil
			}
t.Capmask = (t.Capmask &^ clear) | set
		t.CapSource = 1 // chcaps
		fk.auditf("[audit] sid=%d op=CHCAPS target=%d clear=0x%x set=0x%x", me.Sid, tsid, clear, set)
		r := rep(24 + 4)
		Put32(r[24:], 0)
		return r
	case 11: // KNOBS_GET
		var idx uint8 = 0
		if len(payload) >= 1 {
			idx = payload[0]
		}
		r := rep(24 + 4 + 8 + 16)
		Put32(r[24:], 0)
		var val uint64
		if int(idx) < len(fk.KnobsByIdx) {
			val = fk.KnobsByIdx[idx]
		}
		Put64(r[28:], val)
		copy(r[36:], knobName(idx))
		return r
	case 12: // KNOBS_SET
		if me.Capmask&CapConf == 0 {
			fk.auditf("[audit] sid=%d op=KNOBS_SET reason=cap target=registry", me.Sid)
			// mirrors kernsvc knack(): denial is a status -1 reply
			r := rep(24 + 4)
			Put32(r[24:], 0xFFFFFFFF)
			return r
		}
		if len(payload) < 9 {
			return nil
		}
		idx := payload[0]
		val := Get64(payload[1:])
		if int(idx) >= len(fk.KnobsByIdx) {
			r := rep(24 + 4)
			Put32(r[24:], 0xFFFFFFFF)
			return r
		}
		fk.KnobsByIdx[idx] = val
		r := rep(24 + 4)
		Put32(r[24:], 0)
		return r
	default:
			fk.auditf("[audit] sid=%d uid=%d op=%d reason=op target=registry", me.Sid, me.UID, op)
			return nil
		}
	case NameDevman:
		if me.Capmask&CapDevman == 0 {
			fk.auditf("[audit] sid=%d op=%d reason=cap target=devman", me.Sid, op)
			return nil
		}
		if op == OpDevmanEnum {
			r := rep(28)
			Put32(r[4:], 1) // one device
			Put32(r[8:], ClassConsole)
			Put32(r[12:], 0)      // inst
			Put64(r[16:], 0xF000) // win_off (console window)
			return r
		}
		fk.auditf("[audit] sid=%d op=%d reason=op target=devman", me.Sid, op)
		return nil
	case NamePower:
		if me.Capmask&CapPower == 0 {
			fk.auditf("[audit] sid=%d op=%d reason=cap target=power", me.Sid, op)
			return nil
		}
		if op == OpPowerReboot || op == OpPowerOff {
			if fk.OnPower != nil {
				fk.OnPower(op)
			}
			r := rep(28)
			Put32(r[24:], 0)
			return r
		}
		return nil
	}
	return nil
}

func popcount(v uint64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

// InjectKey queues a typed character for the focused session (tests).
func (b *Bus) InjectKey(cp uint16, kind, mods uint8) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.InputQ = append(b.InputQ, InputEvent{Kind: kind, Mods: mods, Codepoint: cp}.Encode())
}

// TypeString queues key_down records for each rune (tests).
func (b *Bus) TypeString(s string) {
	for _, r := range s {
		b.InjectKey(uint16(r), KeyDown, 0)
	}
}

// Enter queues a carriage return (shell line commit).
func (b *Bus) Enter() { b.InjectKey('\r', KeyDown, 0) }
