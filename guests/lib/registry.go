package kern

import "errors"

// §7 request ops (abi/ABI.md; core/kernsvc.cc is the reference server).
const (
	OpRegistryList  uint16 = 1
	OpRegistryCaps  uint16 = 2
	OpRegistryKill  uint16 = 3
	OpRegistrySpawn uint16 = 4
	OpDevmanEnum    uint16 = 1
	OpPowerReboot   uint16 = 1
	OpPowerOff      uint16 = 2
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
	if len(rep) < 8 {
		return nil, ErrShort
	}
	n := int(Get32(rep[4:8]))
	out := make([]SessionInfo, 0, n)
	off := 8
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
	if len(rep) < 8 {
		return nil, ErrShort
	}
	n := int(Get32(rep[4:8]))
	out := make([]CapEntry, 0, n)
	off := 8
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
	if len(rep) < 8 {
		return -1, ErrShort
	}
	return int32(Get32(rep[4:8])), nil
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
	sid := Get32(rep[4:8])
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
	if len(rep) < 8 {
		return nil, ErrShort
	}
	n := int(Get32(rep[4:8]))
	out := make([]DeviceRec, 0, n)
	off := 8
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
