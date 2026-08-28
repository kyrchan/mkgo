// FSServer: the well-known "fs" service (AGENTS.md Phase 5). Binds
// "fs", answers FS_PORT_PROTOCOL requests (see guests/lib/fsclient.go
// and services/ABI-NOTES.md §3) against a mounted FAT16 volume fed by a
// §3 block window (raw guests) or the v1.1 kern_blk_* imports (Go).
//
// Multiuser model (AGENTS.md Phase 5, ABI v1.1 kernel-stamped uid):
// every datagram carries the sender's registry uid. The server keeps a
// uid→(name,capmask) table fed by REGISTER (op 8; issued by login/init
// after auth — lane-local op, ABI-NOTES.md §4) and enforces:
//
//	uid 0 (admin)    unrestricted absolute access
//	registered user  rooted at /home/<name>; /tmp world-writable;
//	                 /etc + /boot writes need CAP_FS_ADMIN;
//	                 other users' homes invisible (FSNoEntry)
//	unregistered     guest: reads on /etc,/boot/modules,/tmp; /tmp writes
package main

import (
	"errors"
	"strings"

	lib "kernel.lane/guests/lib"
)

const (
	OpFSRegister uint16 = 8 // lane-local (ABI-NOTES.md §4)

	FSAccess = int32(-10) // permission denied
)

type fsSession struct {
	name string
	mask uint64
}

// ServerOptions tunes ServeFS (zero value = production behavior).
type ServerOptions struct {
	// Name defaults to lib.NameFS.
	Name string
	// Stop closes to end the serve loop (tests); nil runs forever.
	Stop <-chan struct{}
}

// ServeFS mounts fat and serves port requests until Stop.
// store is the on-disk backend contract consumed by the port server.
// KFS (log-structured, default) and FAT16 (import/export filter) both
// satisfy it; the §-port protocol above is identical either way.
type store interface {
	Create(path string) error
	Mkdir(path string) error
	Delete(path string) error
	Stat(path string) (StatInfo, error)
	List(path string) ([]StatInfo, error)
	ReadFile(path string, off uint64, buf []byte) (int, error)
	WriteFile(path string, off uint64, data []byte) error
}

func ServeFS(k lib.Kernel, fat store, opts ServerOptions) {
	name := opts.Name
	if name == "" {
		name = lib.NameFS
	}
	h := k.PortCreate(name)
	for h == lib.InvalidHandle {
		h = k.PortBind(name)
		if h != lib.InvalidHandle {
			break
		}
		if stopped(opts.Stop) {
			return
		}
		k.Yield()
	}

	ensureStandardDirs(fat)

	sessions := make(map[uint32]*fsSession)
	buf := make([]byte, lib.MaxMsg)
	replies := lib.NewReplyBook(k)
	for {
		n := k.PortRecv(h, buf)
		if n > 0 && int(n) >= lib.CanonicalHeaderLen {
			if rep, inbox, ok := dispatch(fat, sessions, buf[:int(n)], replies); ok {
				if rh, err := replies.Bind(inbox); err == nil {
					k.PortSend(rh, rep) // -2/-1: client queue full/lost; drop (v1)
				}
			}
		}
		if stopped(opts.Stop) {
			return
		}
		if n == 0 {
			k.Yield()
		}
	}
}

var errMalformed = errors.New("fs: malformed request")

// ensureStandardDirs provisions the AGENTS.md tree skeleton on a fresh
// volume (/etc, /home, /tmp, /boot/modules); existing dirs are left as-is.
// /etc/motd gets a default banner so first boot has one (Phase-7 demo file).
func ensureStandardDirs(fat store) {
	for _, d := range []string{"/etc", "/home", "/tmp", "/boot", "/boot/modules"} {
		if _, err := fat.Stat(d); err == ErrNoEntry {
			_ = fat.Mkdir(d) // best effort; log-free in v1
		}
	}
	if _, err := fat.Stat("/etc/motd"); err == ErrNoEntry {
		_ = fat.Create("/etc/motd") // WriteFile does not create implicitly
		_ = fat.WriteFile("/etc/motd", 0,
			[]byte("Welcome to the capability microkernel\n"))
	}
}

// registerSession handles REGISTER {u32 uid, u16 nLen, name, u64 capmask}.
func registerSession(sessions map[uint32]*fsSession, payload []byte) int32 {
	if len(payload) < 14 {
		return lib.FSBadName
	}
	uid := lib.Get32(payload[0:4])
	name, off, ok := lib.LStr(payload, 4)
	if !ok || len(payload) < off+8 {
		return lib.FSBadName
	}
	mask := lib.Get64(payload[off:])
	if name == "" || len(name) > 32 {
		return lib.FSBadName
	}
	sessions[uid] = &fsSession{name: strings.ToLower(name), mask: mask}
	return lib.FSOK
}

// view is the per-request authorization verdict.
type view struct {
	root  string // prefix for relative paths ("/home/<u>" or "")
	admin bool
	fsAdm bool
	user  string // "" for admin/guest
	regd  bool
}

// authorize maps (uid, rawPath) to the effective filesystem path,
// applying the multiuser policy. write=true gates mutation ops.
func authorize(sessions map[uint32]*fsSession, uid uint32, raw string, write bool) (string, int32) {
	if uid != 0 && hasDotDot(raw) {
		// AGENTS.md multiuser model: no path may traverse out of the
		// caller's root. Denied by POLICY here (storage layer rejects
		// independently — defense in depth).
		return "", lib.FSAccess
	}
	if uid == 0 {
		p := absPath(raw)
		if p == "" {
			return "", lib.FSBadName
		}
		return p, lib.FSOK
	}
	sess, registered := sessions[uid]

	clean := strings.TrimPrefix(raw, "/")
	isRel := !strings.HasPrefix(raw, "/")

	switch {
	case registered && isRel:
		// rooted at the user's home
		return "/home/" + sess.name + "/" + clean, lib.FSOK
	case registered:
		return gateAbsolute(sessions, uid, sess, clean, write)
	case isRel:
		return "", lib.FSAccess // guests have no home
	default:
		return gateAbsolute(sessions, uid, nil, clean, write)
	}
}

// gateAbsolute applies policy to an absolute path for non-admin uid.
func gateAbsolute(sessions map[uint32]*fsSession, uid uint32, sess *fsSession, clean string, write bool) (string, int32) {
	top := clean
	if i := strings.IndexByte(clean, '/'); i >= 0 {
		top = clean[:i]
	}
	switch top {
	case "tmp":
		return "/" + clean, lib.FSOK // world-writable scratch
	case "etc":
		if write && (sess == nil || sess.mask&lib.CapFSAdmin == 0) {
			return "", FSAccess
		}
		return "/" + clean, lib.FSOK
	case "boot":
		if write {
			return "", FSAccess
		}
		return "/" + clean, lib.FSOK
	case "home":
		// Normalize: "home" or "home/u/..." -> rest is "" or "u/..."
		var rest string
		if clean == "home" {
			rest = ""
		} else {
			rest = strings.TrimPrefix(clean, "home/")
		}
		owner := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			owner = rest[:i]
		}
		if owner == "" {
			// /home itself: a registered user sees only their OWN home
			// (listing /home must not leak other users' existence);
			// guests have no home — hide /home entirely.
			if write && uid != 0 && (sess == nil || sess.mask&lib.CapFSAdmin == 0) {
				return "", FSAccess
			}
			if sess != nil {
				return "/home/" + sess.name, lib.FSOK
			}
			return "", lib.FSNoEntry
		}
		if sess != nil && strings.EqualFold(owner, sess.name) {
			return "/home/" + rest, lib.FSOK // own subtree
		}
		// someone else's home: hide existence entirely
		return "", lib.FSNoEntry
	default:
		return "", lib.FSNoEntry // unknown top-level dir: invisible
	}
}

func absPath(raw string) string {
	if !strings.HasPrefix(raw, "/") {
		return ""
	}
	return raw
}

// hasDotDot reports whether any path component is "..".
func hasDotDot(raw string) bool {
	for _, p := range strings.Split(raw, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

// dispatch parses one canonical-header request and renders the reply.
func dispatch(fat store, sessions map[uint32]*fsSession, req []byte, replies *lib.ReplyBook) (rep []byte, inbox string, ok bool) {
	hdr, okH := lib.ParseHeader(req)
	if !okH || hdr.RNam == "" {
		return nil, "", false
	}
	op, seq := hdr.Op, hdr.Seq
	payload := req[lib.CanonicalHeaderLen:]

	if op == OpFSRegister {
		// Issuer gate: only the privileged session (kernel-stamped uid 0
		// = login/init) may feed the uid→(name,capmask) table. Without
		// this, any guest could self-register claiming another user's
		// NAME and inherit its /home/<name> root — names are the rooting
		// key, so registration authority IS the security boundary.
		// Contract: ABI-NOTES.md §4; AGENTS.md "caps granted ONLY at login".
		if hdr.UID != 0 {
			return statusOnly(op, seq, lib.FSAccess), hdr.RNam, true
		}
		st := registerSession(sessions, payload)
		if st == lib.FSOK {
			// provision the user's home root (idempotent); a registered
			// user whose /home/<name> is missing would fail every op
			if sess := sessions[lib.Get32(payload[0:4])]; sess != nil {
				if _, err := fat.Stat("/home/" + sess.name); err == ErrNoEntry {
					_ = fat.Mkdir("/home/" + sess.name)
				}
			}
		}
		return statusOnly(op, seq, st), hdr.RNam, true
	}

	// classify op for policy: mutating vs read-only
	writeOp := op == lib.OpFSWrite || op == lib.OpFSCreate ||
		op == lib.OpFSMkdir || op == lib.OpFSDelete

	var status int32
	var body []byte

	switch op {
	case lib.OpFSStat:
		path, pst := authPath(sessions, hdr.UID, payload, false)
		if pst != lib.FSOK {
			status = pst
			break
		}
		st, err := fat.Stat(path)
		if err != nil {
			status = fsStatus(err)
		} else {
			status = lib.FSOK
			body = make([]byte, 9)
			lib.Put32(body, st.Size)
			body[4] = st.Attr
			lib.Put32(body[5:], st.Cluster)
		}

	case lib.OpFSList:
		path, pst := authPath(sessions, hdr.UID, payload, false)
		if pst != lib.FSOK {
			status = pst
			break
		}
		ents, err := fat.List(path)
		if err != nil {
			status = fsStatus(err)
		} else {
			// Filter /home listing for non-admin to hide other users (AGENTS.md
			// namespace-rooted isolation). Admin (uid 0) sees everything.
			if hdr.UID != 0 && path == "/home" {
				if sess, ok := sessions[hdr.UID]; ok && sess != nil {
					var filtered []StatInfo
					for _, e := range ents {
						if strings.EqualFold(e.Name, sess.name) {
							filtered = append(filtered, e)
						}
					}
					ents = filtered
				} else {
					// guest already denied at authorize, but be safe
					ents = nil
				}
			}
			status = lib.FSOK
			body = make([]byte, 4, 4+len(ents)*40)
			lib.Put32(body, uint32(len(ents)))
			for _, e := range ents {
				if len(body)+2+len(e.Name)+5 > lib.MaxMsg-16 {
					break // §1 datagram cap: truncate huge dirs (v1)
				}
				body = lib.AppendLStr(body, e.Name)
				body = append(body, e.Attr)
				var sz [4]byte
				lib.Put32(sz[:], e.Size)
				body = append(body, sz[:]...)
			}
		}

	case lib.OpFSRead:
		rawPath, plen, ok3 := lstrPath(payload)
		if !ok3 || plen+10 > len(payload) {
			return nil, "", false
		}
		path, pst := resolveFor(sessions, hdr.UID, rawPath, false)
		if pst != lib.FSOK {
			status = pst
			break
		}
		roff := lib.Get64(payload[plen:])
		rcnt := int(lib.Get16(payload[plen+8:]))
		if rcnt > lib.MaxMsg-16 {
			rcnt = lib.MaxMsg - 16
		}
		rbuf := make([]byte, rcnt)
		nr, err := fat.ReadFile(path, roff, rbuf)
		if err != nil {
			status = fsStatus(err)
		} else {
			status = lib.FSOK
			body = make([]byte, 2, 2+nr)
			lib.Put16(body, uint16(nr))
			body = append(body, rbuf[:nr]...)
		}

	case lib.OpFSWrite:
		rawPath, plen, ok3 := lstrPath(payload)
		if !ok3 || plen+10 > len(payload) {
			return nil, "", false
		}
		path, pst := resolveFor(sessions, hdr.UID, rawPath, true)
		if pst != lib.FSOK {
			status = pst
			break
		}
		woff := lib.Get64(payload[plen:])
		wcnt := int(lib.Get16(payload[plen+8:]))
		if plen+10+wcnt > len(payload) {
			return nil, "", false
		}
		err := fat.WriteFile(path, woff, payload[plen+10:plen+10+wcnt])
		if err != nil {
			status = fsStatus(err)
		} else {
			st, serr := fat.Stat(path)
			if serr != nil {
				status = fsStatus(serr)
			} else {
				status = lib.FSOK
				body = make([]byte, 4)
				lib.Put32(body, st.Size)
			}
		}

	case lib.OpFSCreate, lib.OpFSMkdir, lib.OpFSDelete:
		rawPath, _, ok3 := lib.LStr(payload, 0)
		if !ok3 {
			return nil, "", false
		}
		path, pst := resolveFor(sessions, hdr.UID, rawPath, writeOp)
		if pst != lib.FSOK {
			status = pst
			break
		}
		var fn func(string) error
		switch op {
		case lib.OpFSCreate:
			fn = fat.Create
		case lib.OpFSMkdir:
			fn = fat.Mkdir
		default:
			fn = fat.Delete
		}
		status = fsStatus(fn(path))

	default:
		return nil, "", false // unknown op: silent per §7 convention
	}

	rep = make([]byte, 28, 28+len(body))
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)
	lib.Put32(rep[24:], uint32(status))
	rep = append(rep, body...)
	return rep, hdr.RNam, true
}

// authPath extracts + authorizes the {u16 pLen, path} leading field.
func authPath(sessions map[uint32]*fsSession, uid uint32, payload []byte, write bool) (string, int32) {
	path, _, ok := lib.LStr(payload, 0)
	if !ok {
		return "", lib.FSBadName
	}
	return resolveFor(sessions, uid, path, write)
}

// lstrPath decodes the leading {u16 len, bytes} returning path+len.
func lstrPath(payload []byte) (string, int, bool) { return lib.LStr(payload, 0) }

func statusOnly(op, seq uint16, status int32) []byte {
	rep := make([]byte, 28)
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)
	lib.Put32(rep[24:], uint32(status))
	return rep
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

// resolveFor maps a client-supplied path through the multiuser policy
// to its effective filesystem path.
func resolveFor(sessions map[uint32]*fsSession, uid uint32, path string, write bool) (string, int32) {
	return authorize(sessions, uid, path, write)
}

// fsStatus converts a fat16 error to its wire status.
func fsStatus(err error) int32 {
	switch err {
	case nil:
		return lib.FSOK
	case ErrNoEntry:
		return lib.FSNoEntry
	case ErrExists:
		return lib.FSExists
	case ErrNotDir:
		return lib.FSNotDir
	case ErrIsDir:
		return lib.FSIsDir
	case ErrNoSpace:
		return lib.FSNoSpace
	case ErrBadName:
		return lib.FSBadName
	case ErrNotEmpt:
		return lib.FSNotEmpty
	case ErrRange:
		return lib.FSRange
	default:
		return lib.FSIO
	}
}
