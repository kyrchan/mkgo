// FSServer: the well-known "fs" service (AGENTS.md Phase 5). Binds
// "fs", answers FS_PORT_PROTOCOL v0 requests (see guests/lib/fsclient.go
// and services/ABI-NOTES.md) against a mounted FAT16 volume fed by a
// §3 block window.
package main

import (
	"errors"

	lib "kernel.lane/guests/lib"
)

// ServerOptions tunes ServeFS (zero value = production behavior).
type ServerOptions struct {
	// Name defaults to lib.NameFS.
	Name string
	// Stop closes to end the serve loop (tests); nil runs forever.
	Stop <-chan struct{}
}

// ServeFS mounts fat and serves port requests until Stop. It never
// returns in production; tests stop it via opts.Stop.
func ServeFS(k lib.Kernel, fat *FAT, opts ServerOptions) {
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

	buf := make([]byte, lib.MaxMsg)
	replies := lib.NewReplyBook(k)
	for {
		n := k.PortRecv(h, buf)
		if n > 0 && int(n) >= 8 {
			if rep, inbox, ok := dispatch(fat, replies, buf[:int(n)]); ok {
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

// dispatch parses one request datagram and renders the reply.
func dispatch(fat *FAT, replies *lib.ReplyBook, req []byte) (rep []byte, inbox string, ok bool) {
	op := lib.Get16(req[0:2])
	seq := lib.Get16(req[2:4])
	inboxName, off, ok2 := lib.LStr(req, 4)
	if !ok2 || off+2 > len(req) {
		return nil, "", false
	}
	payload := req[off:]

	var status int32
	body := []byte{}

	switch op {
	case lib.OpFSStat:
		path, _, ok3 := lib.LStr(payload, 0)
		if !ok3 {
			return nil, "", false
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
		path, _, ok3 := lib.LStr(payload, 0)
		if !ok3 {
			return nil, "", false
		}
		ents, err := fat.List(path)
		if err != nil {
			status = fsStatus(err)
		} else {
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
		path, poff, ok3 := lib.LStr(payload, 0)
		if !ok3 || poff+10 > len(payload) {
			return nil, "", false
		}
		roff := lib.Get64(payload[poff:])
		rcnt := int(lib.Get16(payload[poff+8:]))
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
		path, poff, ok3 := lib.LStr(payload, 0)
		if !ok3 || poff+10 > len(payload) {
			return nil, "", false
		}
		woff := lib.Get64(payload[poff:])
		wcnt := int(lib.Get16(payload[poff+8:]))
		if poff+10+wcnt > len(payload) {
			return nil, "", false
		}
		err := fat.WriteFile(path, woff, payload[poff+10:poff+10+wcnt])
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

	case lib.OpFSCreate:
		status = simpleOp(fat.Create, payload)
	case lib.OpFSMkdir:
		status = simpleOp(fat.Mkdir, payload)
	case lib.OpFSDelete:
		status = simpleOp(fat.Delete, payload)
	default:
		return nil, "", false // unknown op: silent per §7 convention
	}

	rep = make([]byte, 8, 8+len(body))
	lib.Put16(rep, op)
	lib.Put16(rep[2:], seq)
	lib.Put32(rep[4:], uint32(status))
	rep = append(rep, body...)
	return rep, inboxName, true
}

func simpleOp(fn func(string) error, payload []byte) int32 {
	path, _, ok := lib.LStr(payload, 0)
	if !ok {
		return lib.FSBadName
	}
	return fsStatus(fn(path))
}

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
