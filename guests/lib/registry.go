package kern

import "errors"

// §7 request ops (abi/ABI.md; core/kernsvc.cc is the reference server).
const (
	OpRegistryList      uint16 = 1
	OpRegistryCaps      uint16 = 2
	OpRegistryKill      uint16 = 3
	OpRegistrySpawn     uint16 = 4
	OpRegistryLogin     uint16 = 5 // v1.1: issue uid+capmask to a named session
	OpRegistrySetconf   uint16 = 6 // v1.1: kernel knobs {key[16], u64 value}
	OpRegistryAssignPCI uint16 = 7 // v2.0: assign PCI device to session
	OpRegistrySysstat   uint16 = 8 // v2.1: mem + scheduler observability
	OpRegistryLogdump   uint16 = 9 // v2.1: v1 syslog readback
	OpDevmanEnum        uint16 = 1
	OpPowerReboot       uint16 = 1
	OpPowerOff          uint16 = 2
)

// SPAWN wire layout (v1, per core/kernsvc.cc): name[16], path[64],
// u32 capmask @80, u16 argc @84, args bytes @86.. — path ignored in v1,
// the module resolves from /boot/modules by name.
const (
	spawnNameOff = 0
	spawnPathOff = 16
	spawnMaskOff = 80
	spawnArgcOff = 84
	spawnArgsOff = 86
	SpawnHdrLen  = spawnArgsOff
)

// RegistryClient speaks to the kernel-owned "registry" endpoint.
type RegistryClient struct {
	c  *Client
	rg Handle
}

// SetBudget bounds the reply poll (yields) for every registry call.
func (r *RegistryClient) SetBudget(n int) { r.c.Budget = n }

// Handle exposes the underlying registry bind handle (raw ops).
func (r *RegistryClient) Handle() Handle { return r.rg }

// Request performs one raw framed registry request (extension ops).
func (r *RegistryClient) Request(h Handle, op uint16, payload []byte) ([]byte, error) {
	return r.c.Request(h, op, payload)
}

func BindRegistry(k Kernel) (*RegistryClient, error) {
	h := k.PortBind(NameRegistry)
	if h == InvalidHandle {
		return nil, ErrBadHandle // kernel endpoint always exists once up
	}
	return &RegistryClient{c: NewDirectClient(k, h), rg: h}, nil
}

// List dumps all sessions.
func (r *RegistryClient) List() ([]SessionInfo, error) {
	rep, err := r.c.Request(r.rg, OpRegistryList, nil)
	if err != nil {
		return nil, err
	}
	if len(rep) < 28 {
		return nil, ErrShort
	}
	n := int(Get32(rep[24:28]))
	out := make([]SessionInfo, 0, n)
	off := 28
	for i := 0; i < n; i++ {
		if off+25 > len(rep) {
			break
		}
		si := SessionInfo{
			Sid:   Get32(rep[off:]),
			UID:   Get32(rep[off+4:]),
			State: rep[off+8],
			Name:  cstr16(rep[off+9 : off+25]),
		}
		out = append(out, si)
		off += 25
	}
	return out, nil
}

// CapEntry is one CAPS record.
type CapEntry struct {
	ID     uint32
	Rights uint64
}

// Caps lists sid's capability bits (records {u32 cap_id, u64 rights}).
func (r *RegistryClient) Caps(sid uint32) ([]CapEntry, error) {
	pl := make([]byte, 4)
	Put32(pl, sid)
	rep, err := r.c.Request(r.rg, OpRegistryCaps, pl)
	if err != nil {
		return nil, err
	}
	if len(rep) < 28 {
		return nil, ErrShort
	}
	n := int(Get32(rep[24:28]))
	out := make([]CapEntry, 0, n)
	off := 28
	for i := 0; i < n; i++ {
		if off+12 > len(rep) {
			break
		}
		out = append(out, CapEntry{ID: Get32(rep[off:]), Rights: Get64(rep[off+4:])})
		off += 12
	}
	return out, nil
}

// Kill requests termination of sid. Returns the kernel status (0 ok).
func (r *RegistryClient) Kill(sid uint32) (int32, error) {
	pl := make([]byte, 4)
	Put32(pl, sid)
	rep, err := r.c.Request(r.rg, OpRegistryKill, pl)
	if err != nil {
		return -1, err
	}
	if len(rep) < 28 {
		return -1, ErrShort
	}
	return int32(Get32(rep[24:28])), nil
}

// Chcaps grants or revokes capability bits on a live session (Phase 17,
// op 10). Caller must hold CAP_ADMIN. Returns 0 on success.
func (r *RegistryClient) Chcaps(targetSid uint32, clear, set uint64) (int32, error) {
	pl := make([]byte, 12)
	Put32(pl, targetSid)
	Put32(pl[4:], uint32(clear))
	Put32(pl[8:], uint32(set))
	rep, err := r.c.Request(r.rg, 10, pl)
	if err != nil {
		return -1, err
	}
	if len(rep) < 28 {
		return -1, ErrShort
	}
	return int32(Get32(rep[24:28])), nil
}

// Login issues uid+capmask to the session named name (ABI v1.1 op 5).
// Callable only by the session that owns the "login" well-known port.
func (r *RegistryClient) Login(name string, uid uint32, capmask uint64) error {
	pl := make([]byte, 24)
	copy(pl[0:16], padName(name))
	Put32(pl[16:], uid)
	Put32(pl[20:], uint32(capmask))
	rep, err := r.c.Request(r.rg, OpRegistryLogin, pl)
	if err != nil {
		return err
	}
	if len(rep) < 28 {
		return ErrShort
	}
	if st := int32(Get32(rep[24:28])); st != 0 {
		return ErrRejected
	}
	return nil
}

// SetConf applies a kernel knob via §7 op 6 ({key[16], u64 value});
// needs CapConf on the caller (intended: init).
func (r *RegistryClient) SetConf(key string, value uint64) error {
	pl := make([]byte, 24)
	copy(pl[0:16], padName(key))
	Put64(pl[16:], value)
	rep, err := r.c.Request(r.rg, OpRegistrySetconf, pl)
	if err != nil {
		return err
	}
	if len(rep) < 28 {
		return ErrShort
	}
	if st := int32(Get32(rep[24:28])); st != 0 {
		return ErrRejected
	}
	return nil
}

// AssignPCI assigns a PCI device (bus, dev, fn) to a target session.
// Requires CapDevman on the caller (intended: init.wasm).
func (r *RegistryClient) AssignPCI(targetSid uint32, bus, dev, fn uint8) error {
	pl := make([]byte, 7)
	pl[0] = bus
	pl[1] = dev
	pl[2] = fn
	pl[3] = uint8(targetSid)
	pl[4] = uint8(targetSid >> 8)
	pl[5] = uint8(targetSid >> 16)
	pl[6] = uint8(targetSid >> 24)
	rep, err := r.c.Request(r.rg, OpRegistryAssignPCI, pl)
	if err != nil {
		return err
	}
	if len(rep) < 28 {
		return ErrShort
	}
	if st := int32(Get32(rep[24:28])); st != 0 {
		return ErrRejected
	}
	return nil
}

// SysStat is one SYSSTAT reading (op 8): bump-allocator accounting and
// scheduler config. Body: mem_total u64, mem_used u64, quantum_us u32,
// preempt_on u8, ncpus u32.
type SysStat struct {
	MemTotal  uint64
	MemUsed   uint64
	QuantumUs uint32
	PreemptOn bool
	NCPUs     uint32
}

func (r *RegistryClient) SysStat() (SysStat, error) {
	var st SysStat
	rep, err := r.c.Request(r.rg, OpRegistrySysstat, nil)
	if err != nil {
		return st, err
	}
	if len(rep) < 24+4+16+4+1+4 {
		return st, ErrShort
	}
	if s := int32(Get32(rep[24:28])); s != 0 {
		return st, ErrRejected
	}
	st.MemTotal = Get64(rep[28:36])
	st.MemUsed = Get64(rep[36:44])
	st.QuantumUs = Get32(rep[44:48])
	st.PreemptOn = rep[48] != 0
	st.NCPUs = Get32(rep[49:53])
	return st, nil
}

// LogChunk is one LOGDUMP reply (op 9): Total is the ever-growing stream
// length, Begin the oldest retained offset, Data the bytes [Begin..].
type LogChunk struct {
	Total uint64
	Begin uint64
	Data  []byte
}

func (r *RegistryClient) LogDump(off uint64) (LogChunk, error) {
	var pl [8]byte
	Put64(pl[:], off)
	rep, err := r.c.Request(r.rg, OpRegistryLogdump, pl[:])
	if err != nil {
		return LogChunk{}, err
	}
	if len(rep) < 24+4+16 {
		return LogChunk{}, ErrShort
	}
	if s := int32(Get32(rep[24:28])); s != 0 {
		return LogChunk{}, ErrRejected
	}
	return LogChunk{
		Total: Get64(rep[28:36]),
		Begin: Get64(rep[36:44]),
		Data:  append([]byte(nil), rep[44:]...),
	}, nil
}

// LogAll fetches the full retained log from off to Total.
func (r *RegistryClient) LogAll() ([]byte, error) {
	var out []byte
	var off uint64
	for {
		ch, err := r.LogDump(off)
		if err != nil {
			return out, err
		}
		if len(ch.Data) == 0 {
			return out, nil
		}
		out = append(out, ch.Data...)
		off = ch.Begin + uint64(len(ch.Data))
		if off >= ch.Total {
			return out, nil
		}
	}
}

// ErrSpawnDenied marks a capmask/privilege rejection (kernel returns
// sid=0xFFFFFFFF; unknown ops get no reply at all → ErrNoReply).
var ErrSpawnDenied = errors.New("kern: spawn denied or failed")

// Spawn asks the kernel to instantiate module `name` from /boot/modules
// as a fresh session with exactly capmask (never more than the caller's
// own rights). args become the new session's argv tail (v1 kernel may
// ignore them).
func (r *RegistryClient) Spawn(name, path string, capmask uint64, args ...string) (uint32, error) {
	var argvBuf []byte
	for _, a := range args {
		argvBuf = append(argvBuf, a...)
		argvBuf = append(argvBuf, 0)
	}
	pl := make([]byte, SpawnHdrLen+len(argvBuf))
	copy(pl[spawnNameOff:], pad16(name))
	copy(pl[spawnPathOff:], pad64(path))
	Put32(pl[spawnMaskOff:], uint32(capmask))
	Put16(pl[spawnArgcOff:], uint16(len(args)))
	copy(pl[spawnArgsOff:], argvBuf)

	rep, err := r.c.Request(r.rg, OpRegistrySpawn, pl)
	if err != nil {
		return 0, err
	}
	if len(rep) < 8 {
		return 0, ErrShort
	}
	if len(rep) < 28 {
		return 0, ErrShort
	}
	sid := Get32(rep[24:28])
	if sid == 0xFFFFFFFF {
		return 0, ErrSpawnDenied
	}
	return sid, nil
}

// PowerClient speaks to the "power" endpoint (needs CapPower).
type PowerClient struct {
	c *Client
	p Handle
}

func BindPower(k Kernel) (*PowerClient, error) {
	h := k.PortBind(NamePower)
	if h == InvalidHandle {
		return nil, ErrBadHandle
	}
	return &PowerClient{c: NewDirectClient(k, h), p: h}, nil
}

// Reboot requests a system reboot; the kernel halts after replying.
func (p *PowerClient) Reboot() error { return p.send(OpPowerReboot) }

// Off powers the machine off; the kernel halts after replying.
func (p *PowerClient) Off() error { return p.send(OpPowerOff) }

func (p *PowerClient) send(op uint16) error {
	_, err := p.c.Request(p.p, op, nil)
	return err
}

// DevmanClient speaks to "devman" (needs CapDevman).
type DevmanClient struct {
	c *Client
	d Handle
}

// DeviceRec is one ENUM record {u32 class, u32 inst, u64 win_off}.
type DeviceRec struct {
	Class  uint32
	Inst   uint32
	WinOff uint64
}

// Device classes (ABI §7 devman ENUM).
const (
	ClassBlock   uint32 = 1
	ClassNet     uint32 = 2
	ClassInput   uint32 = 3
	ClassTimer   uint32 = 4
	ClassConsole uint32 = 5

	// ClassFramebuffer is the v1.2 §9.FB device class (ABI.md §9.FB).
	ClassFramebuffer uint32 = 9

	// ClassPCI is the v2.0 §12 VFIO passthrough device class.
	ClassPCI uint32 = 10
)

func BindDevman(k Kernel) (*DevmanClient, error) {
	h := k.PortBind(NameDevman)
	if h == InvalidHandle {
		return nil, ErrBadHandle
	}
	return &DevmanClient{c: NewDirectClient(k, h), d: h}, nil
}

func (d *DevmanClient) Enum() ([]DeviceRec, error) {
	rep, err := d.c.Request(d.d, OpDevmanEnum, nil)
	if err != nil {
		return nil, err
	}
	if len(rep) < 28 {
		return nil, ErrShort
	}
	n := int(Get32(rep[24:28]))
	if n < 0 || n > 64 {
		return nil, ErrShort // denial/NACK parsed as absurd count
	}
	out := make([]DeviceRec, 0, n)
	off := 28
	for i := 0; i < n; i++ {
		if off+16 > len(rep) {
			break
		}
		out = append(out, DeviceRec{
			Class:  Get32(rep[off:]),
			Inst:   Get32(rep[off+4:]),
			WinOff: Get64(rep[off+8:]),
		})
		off += 16
	}
	return out, nil
}

func cstr16(b []byte) string {
	z := 0
	for z < len(b) && b[z] != 0 {
		z++
	}
	return string(b[:z])
}

func pad16(s string) []byte {
	b := make([]byte, 16)
	copy(b, s)
	return b
}

func pad64(s string) []byte {
	b := make([]byte, 64)
	copy(b, s)
	return b
}
