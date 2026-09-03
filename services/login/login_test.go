//go:build !wasip1

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// mkEtcUsers builds an /etc/users file from (name,uid,salt,pw,mask) tuples.
func mkEtcUsers(entries [][5]string) string {
	var b strings.Builder
	for _, e := range entries {
		name, uid, salt, pw, mask := e[0], e[1], e[2], e[3], e[4]
		sum := sha256.Sum256([]byte(salt + pw))
		b.WriteString(name + ":" + uid + ":" + salt + "$" + hex.EncodeToString(sum[:]) + ":" + mask + "\n")
	}
	return b.String()
}

func startLogin(t *testing.T, k *lib.FakeKernel, stop chan struct{}) {
	t.Helper()
	go Serve(k, LoginOptions{Stop: stop})
	waitLoginPort(k)
}

func waitLoginPort(k *lib.FakeKernel) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !k.HasPort(lib.NameLogin) {
		time.Sleep(time.Millisecond)
	}
	if !k.HasPort(lib.NameLogin) {
		panic("login port never appeared")
	}
}

// authClient speaks the login AUTH protocol from a test session.
type authClient struct {
	k   *lib.FakeKernel
	c   *lib.Client
	h   lib.Handle
	seq uint16
}

func newAuthClient(t *testing.T, k *lib.FakeKernel) *authClient {
	t.Helper()
	c, err := lib.NewInboxClient(k, "login")
	if err != nil {
		t.Fatal(err)
	}
	c.Budget = 20000
	h := k.PortBind(lib.NameLogin)
	if h == lib.InvalidHandle {
		t.Fatal("bind login failed")
	}
	return &authClient{k: k, c: c, h: h}
}

func (a *authClient) auth(user, pass string) (int32, uint64, uint32, error) {
	a.seq++
	payload := make([]byte, 0, 1+len(user)+1+len(pass))
	payload = append(payload, byte(len(user)))
	payload = append(payload, user...)
	payload = append(payload, byte(len(pass)))
	payload = append(payload, pass...)
	rep, err := a.c.InboxRequest(a.h, opAuth, payload)
	if err != nil {
		return 0, 0, 0, err
	}
	if len(rep) < 20 {
		return 0, 0, 0, lib.ErrShort
	}
	// layout: {canonical header}{i32 status, u64 capmask, u32 sid}
	return int32(lib.Get32(rep[24:])), lib.Get64(rep[28:]), lib.Get32(rep[36:]), nil
}

func TestAuthIssuesPerUserCapsets(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)

	// the boot-preloaded login session itself needs SPAWN + the masks
	// it hands out (kernel never-more-than-caller rule); it is also the
	// fake kernel's send-identity for the whole scenario
	k.Cur = k.AddSession("login", 0, lib.CapAll)
	startLogin(t, k, stop)

	cli := newAuthClient(t, k)

	st, mask, sid, err := cli.auth("admin", "whatever")
	if err != nil || st != statusOK {
		t.Fatalf("admin auth st=%d err=%v", st, err)
	}
	if mask != lib.CapAll {
		t.Fatalf("admin mask=%x want %x", mask, lib.CapAll)
	}
	reg, _ := lib.BindRegistry(k)
	list, _ := reg.List()
	var shell *lib.SessionInfo
	for i := range list {
		if list[i].Sid == sid {
			shell = &list[i]
		}
	}
	if shell == nil || shell.Name != "shell" {
		t.Fatalf("spawned shell missing sid=%d in %+v", sid, list)
	}

	// regular users get their scoped set only
	st2, mask2, sid2, err := cli.auth("u1", "x")
	if err != nil || st2 != statusOK {
		t.Fatalf("u1 auth st=%d err=%v", st2, err)
	}
	want := lib.CapFocus | lib.CapFSAdmin
	if mask2 != want {
		t.Fatalf("u1 mask=%x want %x", mask2, want)
	}
	caps, _ := reg.Caps(sid2)
	if len(caps) != 2 {
		t.Fatalf("u1 caps records=%+v", caps)
	}
	// u2 must not inherit anything from u1's login
	st3, mask3, _, err := cli.auth("u2", "y")
	if err != nil || st3 != statusOK || mask3 != want {
		t.Fatalf("u2 auth st=%d mask=%x err=%v", st3, mask3, err)
	}
}

func TestAuthUnknownUserRejectedNoSpawn(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	k.Cur = k.AddSession("login", 0, lib.CapAll)
	startLogin(t, k, stop)
	cli := newAuthClient(t, k)

	before := len(k.Sessions)
	st, mask, sid, err := cli.auth("mallory", "z")
	if err != nil {
		t.Fatal(err)
	}
	if st != statusBad || mask != 0 || sid != spawnNone {
		t.Fatalf("st=%d mask=%x sid=%d", st, mask, sid)
	}
	if len(k.Sessions) != before {
		t.Fatal("rejected auth spawned something")
	}
}

func TestAuthSpawnDeniedStillAnswers(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	// login WITHOUT CAP_SPAWN: kernel rejects the SPAWN (audited, no reply),
	// so our client sees ErrNoReply → service reports spawnNone but keeps up.
	k.Cur = k.AddSession("login", 0, lib.CapFocus|lib.CapFSAdmin)
	startLogin(t, k, stop)
	cli := newAuthClient(t, k)

	st, mask, sid, err := cli.auth("u1", "pw")
	if err != nil {
		t.Fatalf("service died on spawn denial: %v", err)
	}
	if st != statusOK || mask != lib.CapFocus|lib.CapFSAdmin || sid != spawnNone {
		t.Fatalf("st=%d mask=%x sid=%d", st, mask, sid)
	}
}

func TestFocusMovesToShell(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	k.Cur = k.AddSession("login", 0, lib.CapAll)
	startLogin(t, k, stop)
	cli := newAuthClient(t, k)
	if _, _, _, err := cli.auth("u1", "pw"); err != nil {
		t.Fatal(err)
	}
	if k.Focused != lib.NameShell {
		t.Fatalf("focused=%q want shell", k.Focused)
	}
}

// TestAuthRegistersUserWithFS: successful AUTH must feed fs.wasm's
// uid→(name,capmask) table (REGISTER op 8) so per-user rooting engages.
func TestAuthRegistersUserWithFS(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	k.Cur = k.AddSession("login", 0, lib.CapAll)
	startLogin(t, k, stop)

	// fake fs server owning the well-known name; records REGISTERs
	k.AddSession("fs", 0, 0)
	fsH := k.PortBind(lib.NameFS)
	if fsH == lib.InvalidHandle {
		t.Fatal("bind fs failed")
	}
	type regRec struct{ uid uint32; name string; mask uint64 }
	regc := make(chan regRec, 4)
	go func() {
		buf := make([]byte, lib.MaxMsg)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n := k.PortRecv(fsH, buf)
			if n >= lib.CanonicalHeaderLen+14 && lib.Get16(buf[0:]) == 8 {
				pl := buf[lib.CanonicalHeaderLen:n]
				name, off, ok := lib.LStr(pl, 4)
				if !ok || len(pl) < off+8 {
					continue
				}
				regc <- regRec{lib.Get32(pl[0:]), name, lib.Get64(pl[off:])}
				continue
			}
			time.Sleep(time.Millisecond)
		}
	}()

	cli := newAuthClient(t, k)
	st, _, _, err := cli.auth("u1", "pw")
	if err != nil || st != statusOK {
		t.Fatalf("auth st=%d err=%v", st, err)
	}

	select {
	case r := <-regc:
		if r.uid != 1001 || r.name != "u1" {
			t.Fatalf("register uid=%d name=%q want u1/1001", r.uid, r.name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fs never received REGISTER after successful auth")
	}
}

// TestParseEtcUsers verifies the name:uid:salt$hash:capmask parser.
func TestParseEtcUsers(t *testing.T) {
	txt := "# comment\nadmin:0:adm$s3cr3t:0xff\nu1:1001:u1$x:0x18\n\n"
	users, err := parseEtcUsers(txt)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("want 2 users, got %d", len(users))
	}
	if users[0].Name != "admin" || users[0].UID != 0 || users[0].Mask != 0xff {
		t.Fatalf("admin entry %+v", users[0])
	}
	if users[0].Salt != "adm" || users[0].Hash != "s3cr3t" {
		t.Fatalf("admin salt/hash %q/%q", users[0].Salt, users[0].Hash)
	}
	if users[1].Name != "u1" || users[1].UID != 1001 || users[1].Mask != 0x18 {
		t.Fatalf("u1 entry %+v", users[1])
	}
}

// TestVerifyPassword checks salted SHA-256 verification.
func TestVerifyPassword(t *testing.T) {
	sum := sha256.Sum256([]byte("mysalt" + "hunter2"))
	u := User{Name: "u1", UID: 1001, Mask: 0x18, Salt: "mysalt", Hash: hex.EncodeToString(sum[:])}

	if !verifyPassword(u, "hunter2") {
		t.Fatal("correct password must verify")
	}
	if verifyPassword(u, "wrong") {
		t.Fatal("wrong password must not verify")
	}
	// empty salt => accept any (DefaultUsers fallback)
	if !verifyPassword(User{Name: "admin"}, "anything") {
		t.Fatal("empty-salt user must accept any password")
	}
}

// TestAuthWrongPasswordRejected: with /etc/users loaded, an incorrect
// password must be rejected (statusBad) — the Phase 10 regression this
// feature exists to prevent.
func TestAuthWrongPasswordRejected(t *testing.T) {
	users := []User{
		{Name: "u1", UID: 1001, Mask: lib.CapFocus | lib.CapFSAdmin, Salt: "s1",
			Hash: hashPw("s1", "secret")},
	}
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	k.Cur = k.AddSession("login", 0, lib.CapAll)
	go Serve(k, LoginOptions{Users: users, Stop: stop})
	waitLoginPort(k)

	cli := newAuthClient(t, k)

	// wrong password => rejected
	st, mask, sid, err := cli.auth("u1", "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if st != statusBad || mask != 0 || sid != spawnNone {
		t.Fatalf("wrong pw: st=%d mask=%x sid=%d", st, mask, sid)
	}

	// correct password => OK
	st2, mask2, _, err := cli.auth("u1", "secret")
	if err != nil || st2 != statusOK {
		t.Fatalf("right pw: st=%d err=%v", st2, err)
	}
	if mask2 != lib.CapFocus|lib.CapFSAdmin {
		t.Fatalf("right pw mask=%x", mask2)
	}
}

// TestAuthHonorsChangedPassword proves the Phase 15 gate slice: login
// re-reads /etc/users on every AUTH, so a passwd-changed hash takes
// effect immediately — old password rejected, new accepted.
func TestAuthHonorsChangedPassword(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	k.Cur = k.AddSession("login", 0, lib.CapAll)

	var mu sync.Mutex
	usersText := mkEtcUsers([][5]string{{"u1", "1001", "salt1", "oldpw", "0x18"}})

	// Minimal fs READ server for /etc/users with mutable content.
	k.AddSession("fs", 0, 0)
	fsH := k.PortBind(lib.NameFS)
	if fsH == lib.InvalidHandle {
		t.Fatal("bind fs failed")
	}
	book := lib.NewReplyBook(k)
	go func() {
		buf := make([]byte, lib.MaxMsg)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n := k.PortRecv(fsH, buf)
			if n <= 8 {
				time.Sleep(time.Millisecond)
				continue
			}
			hdr, ok := lib.ParseHeader(buf[:n])
			if !ok || hdr.RNam == "" {
				continue
			}
			pl := buf[lib.CanonicalHeaderLen:n]
			rh, err := book.Bind(hdr.RNam)
			if err != nil {
				continue
			}
			mk := func(status int32, body ...byte) []byte {
				r := make([]byte, 28, 28+len(body))
				lib.Put16(r, hdr.Op)
				lib.Put16(r[2:], hdr.Seq)
				lib.Put32(r[24:], uint32(status))
				return append(r, body...)
			}
			if hdr.Op != lib.OpFSRead {
				k.PortSend(rh, mk(lib.FSIO))
				continue
			}
			mu.Lock()
			txt := usersText
			mu.Unlock()
			off := lib.Get64(pl[len(pl)-10:])
			if off >= uint64(len(txt)) {
				k.PortSend(rh, mk(lib.FSOK, 0, 0))
				continue
			}
			data := txt[off:]
			b := make([]byte, 2, 2+len(data))
			lib.Put16(b, uint16(len(data)))
			k.PortSend(rh, mk(lib.FSOK, append(b, data...)...))
		}
	}()

	go Serve(k, LoginOptions{Stop: stop})
	waitLoginPort(k)
	cli := newAuthClient(t, k)

	st, _, _, err := cli.auth("u1", "oldpw")
	if err != nil || st != statusOK {
		t.Fatalf("initial auth st=%d err=%v", st, err)
	}

	// passwd change: new salt + new password.
	mu.Lock()
	usersText = mkEtcUsers([][5]string{{"u1", "1001", "salt2", "newpw", "0x18"}})
	mu.Unlock()

	st, _, _, err = cli.auth("u1", "oldpw")
	if err != nil {
		t.Fatal(err)
	}
	if st != statusBad {
		t.Fatalf("old password still accepted after change (st=%d)", st)
	}
	st, _, _, err = cli.auth("u1", "newpw")
	if err != nil || st != statusOK {
		t.Fatalf("new password rejected after change (st=%d err=%v)", st, err)
	}
}

func hashPw(salt, pw string) string {
	sum := sha256.Sum256([]byte(salt + pw))
	return hex.EncodeToString(sum[:])
}
