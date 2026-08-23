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

	Sessions  []*FakeSession
	Cur       *FakeSession      // identity attributed to sends from the test
	Audit     []string          // captured audit lines
	Knobs     map[string]uint64 // applied via SETCONF (v1.1)
	SpawnHook SpawnFn
	OnPower   func(op uint16)

	nextSid uint32

	// ForceUID overrides sender-uid stamping when set (tests simulate
	// multiple sessions against one bus without goroutine juggling).
	ForceUID    uint32
	forceUIDSet bool
}

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
	s := &FakeSession{Sid: fk.nextSid, UID: uid, Name: name, Capmask: mask, State: StateRunning}
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
			r := rep(28 + 25*n)
			Put32(r[24:], uint32(n))
			off := 28
			for _, s := range fk.Sessions[:n] {
				Put32(r[off:], s.Sid)
				Put32(r[off+4:], s.UID)
				r[off+8] = s.State
				copy(r[off+9:off+25], pad16(s.Name))
				off += 25
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
