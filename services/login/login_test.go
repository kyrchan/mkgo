//go:build !wasip1

package main

import (
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

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
