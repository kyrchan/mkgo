package main

import (
	lib "kernel.lane/guests/lib"
)

// login.wasm — well-known "login" service (abi/ABI.md §7 capability
// issuance). v1: static user table, stub password check (accept any),
// per-user capability sets issued by SPAWNing the user's shell session
// with exactly that mask (the kernel enforces never-more-than-caller),
// then moving focus to the fresh shell (§4). Real hashing arrives with
// Phase 10 (/etc/users on fs.wasm).

// User is one static-table entry.
type User struct {
	Name string
	UID  uint32
	Mask uint64
}

// DefaultUsers encodes the AGENTS.md policy: `admin` holds every bit;
// regular users hold none of bits 0–2 (kill/devman/power) plus a scoped
// remainder. Table-driven so Phase 10 can swap the source without
// touching protocol code.
var DefaultUsers = []User{
	{Name: "admin", UID: 0, Mask: lib.CapAll},
	{Name: "u1", UID: 1001, Mask: lib.CapFocus | lib.CapFSAdmin},
	{Name: "u2", UID: 1002, Mask: lib.CapFocus | lib.CapFSAdmin},
}

// LoginOptions tunes Serve (zero value = production behavior).
type LoginOptions struct {
	Users       []User
	ShellModule string // module spawned after auth; default "shell"
	Stop        <-chan struct{}
}

// AUTH op codes / reply layout (services/ABI-NOTES.md §5).
const (
	opAuth = uint16(1)

	statusOK  = int32(0)
	statusBad = int32(-1)
	spawnNone = uint32(0xFFFFFFFF)
)

func (o *LoginOptions) users() []User {
	if len(o.Users) > 0 {
		return o.Users
	}
	return DefaultUsers
}

func (o *LoginOptions) shell() string {
	if o.ShellModule != "" {
		return o.ShellModule
	}
	return lib.NameShell
}

// Serve owns-or-binds "login" and answers AUTH requests until Stop.
func Serve(k lib.Kernel, opts LoginOptions) {
	h := k.PortCreate(lib.NameLogin)
	for h == lib.InvalidHandle {
		h = k.PortBind(lib.NameLogin)
		if h != lib.InvalidHandle {
			break
		}
		if stopped(opts.Stop) {
			return
		}
		k.Yield()
	}

	reg, err := lib.BindRegistry(k)
	if err != nil {
		return // registry is a kernel endpoint: absence means shutdown
	}
	// bounded SPAWN budget: a cap-rejected SPAWN gets NO reply from the
	// kernel (§7 audit-only); don't let that wedge the AUTH reply.
	reg.SetBudget(5000)

	buf := make([]byte, lib.MaxMsg)
	replies := lib.NewReplyBook(k)
	for {
		n := k.PortRecv(h, buf)
		if n > 8 {
			kernAuth(k, reg, replies, buf[:int(n)], opts)
		}
		if stopped(opts.Stop) {
			return
		}
		if n == 0 {
			k.Yield()
		}
	}
}

// kernAuth processes one AUTH datagram end-to-end.
func kernAuth(k lib.Kernel, reg *lib.RegistryClient, replies *lib.ReplyBook, req []byte, opts LoginOptions) {
	op := lib.Get16(req[0:2])
	seq := lib.Get16(req[2:4])
	inboxName, off, ok := lib.LStr(req, 4)
	if !ok || op != opAuth {
		return // silent on junk, mirroring §7 convention
	}
	name, n2, _ := lbyte(req[off:])
	pass, _, _ := lbyte(req[n2:])

	rh, err := replies.Bind(inboxName)
	if err != nil {
		return
	}

	rep := make([]byte, 4, 4+16)
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)

	u, found := lookup(opts.users(), name)
	switch {
	case !found:
		rep = appendStatus(rep, statusBad, 0, spawnNone)
	case u.Mask == 0:
		rep = appendStatus(rep, statusBad, 0, spawnNone)
	default:
		sid := doSpawn(k, reg, opts.shell(), u)
		rep = appendStatus(rep, statusOK, u.Mask, sid)
		focusShell(k)
	}
	_ = pass // stub: any password accepted until Phase 10 hashes
	k.PortSend(rh, rep)
}

func doSpawn(k lib.Kernel, reg *lib.RegistryClient, shell string, u *User) uint32 {
	sid, err := reg.Spawn(shell, shell, u.Mask, u.Name)
	if err != nil {
		return spawnNone
	}
	return sid
}

// focusShell moves kernel focus to whoever owns the "shell" port name.
// Bounded retry: the fresh shell needs a moment to bind its name.
func focusShell(k lib.Kernel) {
	for i := 0; i < 5000; i++ {
		if sh := k.PortBind(lib.NameShell); sh != lib.InvalidHandle {
			k.FocusSet(sh)
			return
		}
		k.Yield()
	}
}

func lookup(users []User, name string) (*User, bool) {
	for i := range users {
		if users[i].Name == name {
			return &users[i], true
		}
	}
	return nil, false
}

// lbyte decodes {u8 len, bytes}.
func lbyte(b []byte) (string, int, bool) {
	if len(b) < 1 {
		return "", 0, false
	}
	n := int(b[0])
	if 1+n > len(b) {
		return "", 0, false
	}
	return string(b[1 : 1+n]), 1 + n, true
}

func appendStatus(rep []byte, status int32, mask uint64, sid uint32) []byte {
	var tail [16]byte
	lib.Put32(tail[0:], uint32(status))
	lib.Put64(tail[4:], mask)
	lib.Put32(tail[12:], sid)
	return append(rep, tail[:]...)
}

func stopped(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
