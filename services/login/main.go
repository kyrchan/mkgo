// login.wasm -- well-known "login" service (Phase 5 multiuser stub).
// Static user table (v1); validates credentials arriving as framed
// datagrams and grants identity+capability sets via registry LOGIN op.
// Only the owner of "login" may call LOGIN -- kernel-enforced (ABI v1.1).
package main

import (
	"os"

	lib "kernel.services/lib"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func port_send(h int32, buf *byte, len uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h int32, buf *byte, cap uint32) int32

//go:wasmimport kernel kern_focus_set
func kern_focus_set(h int32) int32


type user struct {
	name string
	pw   string
	uid  uint32
	caps uint32
}

var users = []user{
	{"admin", "admin", 0, 0x7F},
	{"u1", "u1", 1, 0},
	{"u2", "u2", 2, 0},
}

func cstr(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

/* client request: {u16 op=1, char user[16], char pw[16], char rname[16]} */
func handleAuth(req []byte) []byte {
	if len(req) < 50 {
		return []byte{0xFF, 0xFF} // errno -1
	}
	un := cstrz(req[2:18])
	pw := cstrz(req[18:34])
	rname := cstrz(req[34:50])
	for _, u := range users {
		if u.name == un && u.pw == pw {
			// grant identity to the CALLER'S SESSION (rname) with the
			// matched user's uid + capability mask
			var verdict []byte
			if doLogin(rname, u.uid, u.caps) {
				os.Stdout.WriteString("[login] '")
				os.Stdout.WriteString(un)
				os.Stdout.WriteString("' authenticated -> uid=")
				os.Stdout.WriteString(itoa(int(u.uid)))
				os.Stdout.WriteString("\n")
				verdict = []byte{0, 0}
				// hand keyboard focus to the shell
				if sh := port_bind(&cstr("shell")[0], 5); sh >= 0 {
					kern_focus_set(sh)
				}
			} else {
				verdict = []byte{0xFE, 0xFF} // registry refused
			}
			replyTo(rname, verdict)
			return nil
		}
	}
	os.Stdout.WriteString("[login] auth denied for '")
	os.Stdout.WriteString(un)
	os.Stdout.WriteString("'\n")
	replyTo(rname, []byte{0x10, 0x00})
	return nil
}

var replyH = map[string]int32{}

func replyTo(rname string, resp []byte) {
	if rname == "" {
		return
	}
	h, ok := replyH[rname]
	if !ok {
		h = port_bind(&cstr(rname)[0], uint32(len(rname)))
		if h < 0 {
			return
		}
		replyH[rname] = h
	}
	port_send(h, &resp[0], uint32(len(resp)))
}

func cstrz(b []byte) string {
	end := 0
	for end < len(b) && b[end] != 0 {
		end++
	}
	return string(b[:end])
}

func doLogin(name string, uid, caps uint32) bool {
	rg := port_bind(&cstr("registry")[0], 8)
	if rg < 0 {
		os.Stdout.WriteString("[login] bind registry failed\n")
		return false
	}
	/* v1.1 framing: {u16 op,u16 seq,u32 uid,char rname[16],payload} */
	req := make([]byte, 24+24)
	req[0] = 5 // LOGIN
	req[3] = 7 // seq
	copy(req[8:24], "login") // reply channel = our own well-known queue
	copy(req[24:40], name)
	req[40] = byte(uid)
	req[41] = byte(uid >> 8)
	req[42] = byte(uid >> 16)
	req[43] = byte(uid >> 24)
	req[44] = byte(caps)
	req[45] = byte(caps >> 8)
	req[46] = byte(caps >> 16)
	req[47] = byte(caps >> 24)
	rc := port_send(rg, &req[0], uint32(len(req)))
	_ = rc
	// reply lands on our own well-known queue (rname="login")
	mq := port_bind(&cstr("login")[0], 5)
	if mq < 0 {
		return false
	}
	defer func() { _ = mq }()
	out := make([]byte, 64)
	var strays [][]byte
	ok := false
	for i := 0; i < 10000; i++ {
		n := port_recv(mq, &out[0], uint32(len(out)))
		if n > 0 && n >= 8 && out[0] == 5 { // LOGIN verdict (op echo)
			st := uint32(out[4]) | uint32(out[5])<<8 |
				uint32(out[6])<<16 | uint32(out[7])<<24
			ok = st == 0
			break
		}
		if n > 0 { // not ours (e.g. another client's creds): requeue later
			cp := make([]byte, n)
			copy(cp, out[:n])
			strays = append(strays, cp)
		}
		sched_yield()
	}
	for _, smsg := range strays { // put non-matching messages back
		port_send(mq, &smsg[0], uint32(len(smsg)))
	}
	return ok
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func main() {
	os.Stdout.WriteString("[login] ready\n")
	port_create(&cstr("login")[0], 5)

	// input-driven authentication: type user + password at the console
	users := map[string]string{
		"admin": "admin",
		"u1":    "u1",
		"u2":    "u2",
	}
	uids := map[string]uint32{"admin": 0, "u1": 1, "u2": 2}
	capsm := map[string]uint32{"admin": 0x7F}

	state := 0 // 0=user, 1=password
	user, pw := "", ""
	port_create(&cstr("login")[0], 5)
	h := port_bind(&cstr("login")[0], 5)
	buf := make([]byte, 512)
	idle := 0
	lib.ConsoleOut("\nlogin: ")
	line := make([]byte, 0, 64)
	for i := 0; i < 1200000; i++ {
		/* legacy port-based creds (testers) alongside typed input */
		if n := port_recv(h, &buf[0], uint32(len(buf))); n > 0 {
			idle = 0
			if resp := handleAuth(buf[:n]); len(resp) > 0 {
				port_send(h, &resp[0], uint32(len(resp)))
			}
			continue
		}
		recs := lib.RecvInput(16)
		if len(recs) == 0 {
			idle++
			lib.SchedYield()
			continue
		}
		for _, r := range recs {
			if r.Kind != 1 {
				continue
			}
			switch r.CP {
			case '\n':
				lib.ConsoleOut("\n")
				if state == 0 {
					user = string(line)
					state = 1
					pw = ""
					lib.ConsoleOut("password: ")
					line = line[:0]
				} else {
					pw = string(line)
					if pwt, ok := users[user]; ok && pwt == pw {
						os.Stdout.WriteString("[login] '" + user +
							"' authenticated\n")
						doLogin("shell", uids[user], capsm[user])
						if sh := port_bind(&cstr("shell")[0], 5); sh >= 0 {
							kern_focus_set(sh)
						}
						os.Stdout.WriteString("[login] session for " +
							user + "\n")
						return
					}
					os.Stdout.WriteString("[login] denied user=" +
						user + " pwlen=" + itoa(len(pw)) + "\n")
					state = 0
					user = ""
					lib.ConsoleOut("login: ")
				}
				line = line[:0]
			case 8, 127:
				if len(line) > 0 {
					line = line[:len(line)-1]
				}
			default:
				if r.CP >= 32 && r.CP < 127 && len(line) < 60 {
					line = append(line, byte(r.CP))
					if state == 0 {
						lib.ConsoleOut(string(byte(r.CP)))
					}
				}
			}
		}
	}
	os.Stdout.WriteString("[login] done\n")
}

