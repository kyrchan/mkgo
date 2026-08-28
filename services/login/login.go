package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"

	lib "kernel.lane/guests/lib"
)

// login.wasm — well-known "login" service (abi/ABI.md §7 capability
// issuance). Phase 10: reads /etc/users from fs.wasm
// (name:uid:salted-hash:capmask), verifies salted SHA-256 passwords,
// issues per-user capability sets. Falls back to DefaultUsers when
// /etc/users is unavailable (tests, fresh volumes).

// User is one /etc/users entry.
type User struct {
	Name string
	UID  uint32
	Mask uint64
	Salt string
	Hash string // hex(sha256(salt + password))
}

// DefaultUsers is the fallback table when /etc/users cannot be read.
// Empty Salt => accept any password (backward compat for tests/fallback).
var DefaultUsers = []User{
	{Name: "admin", UID: 0, Mask: lib.CapAll},
	{Name: "u1", UID: 1001, Mask: lib.CapFocus | lib.CapFSAdmin},
	{Name: "u2", UID: 1002, Mask: lib.CapFocus | lib.CapFSAdmin},
}

// LoginOptions tunes Serve (zero value = production behavior).
type LoginOptions struct {
	Users       []User
	ShellModule string
	Stop        <-chan struct{}
}

const (
	opAuth = uint16(1)

	statusOK  = int32(0)
	statusBad = int32(-1)
	spawnNone = uint32(0xFFFFFFFF)
)

const opFSRegister = uint16(8)

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
		return
	}
	reg.SetBudget(5000)

	// Phase 10: prefer /etc/users; fall back to opts.DefaultUsers.
	users := opts.Users
	if len(users) == 0 {
		if loaded, err := loadUsersFromFS(k); err == nil && len(loaded) > 0 {
			users = loaded
			os.Stdout.WriteString("[login] loaded " + strconv.Itoa(len(loaded)) + " users from /etc/users\n")
		} else {
			users = DefaultUsers
			os.Stdout.WriteString("[login] using default user table\n")
		}
	}

	buf := make([]byte, lib.MaxMsg)
	replies := lib.NewReplyBook(k)
	for {
		n := k.PortRecv(h, buf)
		if n > 8 {
			kernAuth(k, reg, replies, buf[:int(n)], users, opts.shell())
		}
		if stopped(opts.Stop) {
			return
		}
		if n == 0 {
			k.Yield()
		}
	}
}

// loadUsersFromFS reads /etc/users from the fs service with bounded
// retries (fs may not be up yet at login startup).
func loadUsersFromFS(k lib.Kernel) ([]User, error) {
	var fsc *lib.FSClient
	var err error
	for i := 0; i < 300 && fsc == nil; i++ {
		fsc, err = lib.BindFS(k, "login")
		if err != nil {
			fsc = nil
			k.Yield()
		}
	}
	if fsc == nil {
		return nil, err
	}
	fsc.SetBudget(5000)
	buf := make([]byte, 8192)
	n, err := fsc.ReadFile("/etc/users", 0, buf)
	if err != nil {
		return nil, err
	}
	return parseEtcUsers(string(buf[:n]))
}

// parseEtcUsers parses the /etc/users file format:
//
//	name:uid:salted-hash:capmask
//	salted-hash = salt$hex(sha256(salt + password))
//	capmask = hex (e.g. 0x18)
//
// Lines starting with # are comments. Blank lines skipped.
func parseEtcUsers(text string) ([]User, error) {
	var users []User
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) != 4 {
			continue
		}
		name := parts[0]
		uid, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			continue
		}
		saltedHash := parts[2]
		mask, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(parts[3]), "0x"), 16, 64)
		if err != nil {
			continue
		}
		salt, hash := splitSalted(saltedHash)
		users = append(users, User{
			Name: name,
			UID:  uint32(uid),
			Mask: mask,
			Salt: salt,
			Hash: hash,
		})
	}
	return users, nil
}

func splitSalted(s string) (salt, hash string) {
	i := strings.IndexByte(s, '$')
	if i < 0 {
		return "", s
	}
	return s[:i], s[i+1:]
}

// verifyPassword checks password against the user's salted hash.
// Empty salt => accept any (DefaultUsers fallback behavior).
func verifyPassword(u User, password string) bool {
	if u.Salt == "" {
		return true
	}
	sum := sha256.Sum256([]byte(u.Salt + password))
	return hex.EncodeToString(sum[:]) == u.Hash
}

func kernAuth(k lib.Kernel, reg *lib.RegistryClient, replies *lib.ReplyBook, req []byte, users []User, shell string) {
	hdr, ok := lib.ParseHeader(req)
	if !ok || hdr.Op != opAuth || hdr.RNam == "" {
		return
	}
	payload := req[lib.CanonicalHeaderLen:]
	name, n2, _ := lbyte(payload)
	pass, _, _ := lbyte(payload[n2:])

	rh, err := replies.Bind(hdr.RNam)
	if err != nil {
		return
	}

	rep := make([]byte, lib.CanonicalHeaderLen, lib.CanonicalHeaderLen+16)
	lib.Put16(rep, hdr.Op)
	lib.Put16(rep[2:], hdr.Seq)

	u, found := lookup(users, name)
	switch {
	case !found:
		rep = appendStatus(rep, statusBad, 0, spawnNone)
	case u.Mask == 0:
		rep = appendStatus(rep, statusBad, 0, spawnNone)
	case !verifyPassword(*u, pass):
		rep = appendStatus(rep, statusBad, 0, spawnNone)
	default:
		sid := doSpawn(k, reg, shell, u)
		_ = reg.Login(hdr.RNam, u.UID, u.Mask)
		rep = appendStatus(rep, statusOK, u.Mask, sid)
		focusShell(k)
		k.PortSend(rh, rep)
		registerFS(k, u)
		return
	}
	k.PortSend(rh, rep)
}

func registerFS(k lib.Kernel, u *User) {
	fh := lib.InvalidHandle
	for i := 0; i < 200 && fh == lib.InvalidHandle; i++ {
		fh = k.PortBind(lib.NameFS)
		if fh == lib.InvalidHandle {
			k.Yield()
		}
	}
	if fh == lib.InvalidHandle {
		return
	}
	c, err := lib.NewInboxClient(k, "fsreg")
	if err != nil {
		return
	}
	c.Budget = 2000
	pl := make([]byte, 0, 14+len(u.Name))
	var head [4]byte
	lib.Put32(head[:], u.UID)
	pl = append(pl, head[:]...)
	pl = lib.AppendLStr(pl, u.Name)
	var m [8]byte
	lib.Put64(m[:], u.Mask)
	pl = append(pl, m[:]...)
	if _, err := c.InboxRequest(fh, opFSRegister, pl); err != nil {
		os.Stdout.WriteString("[login] fsreg failed: " + err.Error() + "\n")
	}
}

func doSpawn(k lib.Kernel, reg *lib.RegistryClient, shell string, u *User) uint32 {
	sid, err := reg.Spawn(shell, shell, 0, u.Name)
	if err != nil {
		return spawnNone
	}
	_ = reg.Login(shell, u.UID, u.Mask)
	return sid
}

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
