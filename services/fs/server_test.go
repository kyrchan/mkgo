package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// startServer mounts a fresh RAM-disk FAT16 behind a live §3 window and
// serves "fs" on the bus until stop closes.
func startServer(t *testing.T, k *lib.FakeKernel, stop chan struct{}) {
	t.Helper()
	_, win, err := NewRamDisk(testBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := Format(win, "SYSDISK"); err != nil {
		t.Fatal(err)
	}
	fat, err := Mount(win)
	if err != nil {
		t.Fatal(err)
	}
	go ServeFS(k, fat, ServerOptions{Stop: stop})
	waitPortFS(k)
}

func waitPortFS(k lib.Kernel) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h := k.PortBind(lib.NameFS); h != lib.InvalidHandle {
			return // name exists (we leak this probe bind; bus-only)
		}
		time.Sleep(time.Millisecond)
	}
	panic("fs port never appeared")
}

func newClient(t *testing.T, k *lib.FakeKernel) *lib.FSClient {
	t.Helper()
	c, err := lib.BindFS(k, "shell")
	if err != nil {
		t.Fatal(err)
	}
	c.SetBudget(20000)
	return c
}

func TestFSServerEndToEnd(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	cli := newClient(t, k)

	// mkdir /etc (may be pre-provisioned) + write /etc/motd + read back
	if err := cli.Mkdir("/etc"); err != nil && err != lib.ErrFSExists {
		t.Fatal(err)
	}
	if err := cli.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello from the fs server\n")
	if n, err := cli.WriteFile("/etc/motd", 0, msg); err != nil || n != len(msg) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	buf := make([]byte, 128)
	n, err := cli.ReadFile("/etc/motd", 0, buf)
	if err != nil || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("read n=%d err=%v %q", n, err, buf[:n])
	}

	st, err := cli.Stat("/etc/motd")
	if err != nil || st.Size != uint32(len(msg)) || st.IsDir() {
		t.Fatalf("stat=%+v err=%v", st, err)
	}

	ents, err := cli.List("/")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range ents {
		names[e.Name] = e.IsDir()
	}
	for _, want := range []string{"ETC", "HOME", "TMP", "BOOT"} {
		if !names[want] {
			t.Fatalf("root missing %s: %+v", want, ents)
		}
	}

	// error mapping across the wire
	if _, err := cli.Stat("/nope"); err != lib.ErrFSNoEntry {
		t.Fatalf("stat missing: %v", err)
	}
	if err := cli.Delete("/etc"); err != lib.ErrFSNotEmpty {
		t.Fatalf("rmdir non-empty: %v", err)
	}

	if err := cli.Delete("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Stat("/etc/motd"); err != lib.ErrFSNoEntry {
		t.Fatalf("post-delete: %v", err)
	}
}

func TestFSServerLargeTransferChunking(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)
	cli := newClient(t, k)

	if err := cli.Mkdir("/home"); err != nil && err != lib.ErrFSExists {
		t.Fatal(err)
	}
	if err := cli.Create("/home/big.bin"); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 12000) // >2× maxReadChunk → several WRITEs
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if n, err := cli.WriteFile("/home/big.bin", 0, payload); err != nil || n != len(payload) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	back := make([]byte, len(payload))
	if n, err := cli.ReadFile("/home/big.bin", 0, back); err != nil || n != len(payload) {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	if !bytes.Equal(back, payload) {
		t.Fatal("chunked roundtrip mismatch")
	}
	// offset read mid-file
	mid := make([]byte, 10)
	if n, _ := cli.ReadFile("/home/big.bin", 6000, mid); n != 10 || !bytes.Equal(mid, payload[6000:6010]) {
		t.Fatalf("mid read %q", mid)
	}
}

func TestFSServerMultiClientReplyRouting(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	a := newClient(t, k)
	b, err := lib.BindFS(k, "other")
	if err != nil {
		t.Fatal(err)
	}
	b.SetBudget(20000)

	if err := a.Create("/only-a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := b.Create("/b-file.txt"); err != nil {
		t.Fatal(err)
	}
	// replies must have landed on each caller's own inbox
	if _, err := a.Stat("/only-a.txt"); err != nil {
		t.Fatalf("a lost its reply channel: %v", err)
	}
	if _, err := b.Stat("/b-file.txt"); err != nil {
		t.Fatalf("b lost its reply channel: %v", err)
	}
}

// ---- multiuser policy (AGENTS.md Phase 5 gate) ----

func TestMultiuserRootingAndDenials(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	restoreAdmin := k.As(0) // admin = uid 0
	admin := newUidClient(t, k, "admin0", 0)

	// seed: admin creates /etc, homes, motd
	for _, d := range []string{"/etc", "/home", "/home/u1", "/home/u2", "/boot", "/boot/modules"} {
		if err := admin.Mkdir(d); err != nil && err != ErrExists && err != lib.ErrFSExists {
			t.Fatalf("admin mkdir %s: %v", d, err)
		}
	}
	if err := admin.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if n, err := admin.WriteFile("/etc/motd", 0, []byte("welcome\n")); err != nil || n != 8 {
		t.Fatalf("admin motd write %d %v", n, err)
	}
	restoreAdmin()

	u1 := newUidClient(t, k, "u1sess", uint32(1001), "u1", lib.CapFocus)
	u2 := newUidClient(t, k, "u2sess", uint32(1002), "u2", lib.CapFocus)

	// u1 writes into OWN home via relative path (rooted at /home/u1)
	restoreU1 := k.As(1001)
	if err := u1.Create("secret.txt"); err != nil {
		t.Fatal(err)
	}
	if n, err := u1.WriteFile("secret.txt", 0, []byte("u1 data")); err != nil || n != 7 {
		t.Fatalf("u1 write %d %v", n, err)
	}
	restoreU1()

	// u2 CANNOT see or touch u1's file — hidden as ENOENT
	scopeU2 := k.As(1002)
	if _, err := u2.Stat("/home/u1/secret.txt"); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 stat u1 file: %v", err)
	}
	if _, err := u2.Stat("/home/u1"); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 stat u1 home: %v", err)
	}
	buf := make([]byte, 32)
	if _, err := u2.ReadFile("/home/u1/secret.txt", 0, buf); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 read u1 file: %v", err)
	}
	if err := u2.Delete("/home/u1/secret.txt"); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 delete u1 file: %v", err)
	}

	// u2 CAN write /tmp (world-writable); u1 sees it
	if err := u2.Create("/tmp/shared.txt"); err != nil {
		t.Fatal(err)
	}
	if n, err := u2.WriteFile("/tmp/shared.txt", 0, []byte("shared")); err != nil || n != 6 {
		t.Fatalf("u2 tmp write %d %v", n, err)
	}

	// u2's home is writable by u2 but invisible to u1
	if err := u2.Create("mine.txt"); err != nil {
		t.Fatal(err)
	}
	scopeU2()
	restoreU1 = k.As(1001)
	if _, err := u1.Stat("/home/u2/mine.txt"); err != lib.ErrFSNoEntry {
		t.Fatalf("u1 sees u2 file: %v", err)
	}
	restoreU1()

	// /etc writes denied without CAP_FS_ADMIN; allowed with it
	scopeU2 = k.As(1002)
	if err := u2.Create("/etc/hax"); err != lib.ErrFSAccess {
		t.Fatalf("u2 /etc create: %v", err)
	}
	if _, err := u2.WriteFile("/etc/motd", 0, []byte("pwned")); err != lib.ErrFSAccess {
		t.Fatalf("u2 /etc write: %v", err)
	}
	scopeU2()
	restoreU3 := k.As(1003)
	u3 := newUidClient(t, k, "u3sess", uint32(1003), "u3", lib.CapFocus|lib.CapFSAdmin)
	if err := u3.Create("/etc/allowed.cfg"); err != nil {
		t.Fatalf("fs-admin user /etc create: %v", err)
	}

	// /boot read-only for users even with FS_ADMIN
	if err := u3.Create("/boot/modules/evil"); err != lib.ErrFSAccess {
		t.Fatalf("u3 /boot create: %v", err)
	}
	if ents, err := u3.List("/boot/modules"); err != nil || len(ents) != 0 {
		t.Fatalf("u3 /boot list=%+v err=%v", ents, err)
	}
	restoreU3()

	// unregistered uid = guest: reads OK, relative paths refused
	guestScope := k.As(5555)
	guest := newUidClient(t, k, "guestx", uint32(5555))
	if _, err := guest.Stat("/etc/motd"); err != nil {
		t.Fatalf("guest /etc read: %v", err)
	}
	if err := guest.Create("anything"); err != lib.ErrFSAccess {
		t.Fatalf("guest relative create: %v", err)
	}
	guestScope()

	// admin still absolute
	restoreAdmin()
	if st, err := admin.Stat("/home/u1/secret.txt"); err != nil || st.Size != 7 {
		t.Fatalf("admin cross-home stat=%+v err=%v", st, err)
	}
	if err := admin.Create("/rootfile"); err != nil {
		t.Fatal(err)
	}
}

// TestHomeListingIsolation pins the /home listing leak fix: a registered
// user listing /home must see only their OWN home — other users' homes
// are invisible (existence hidden), and a guest cannot list /home at all.
func TestHomeListingIsolation(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	restoreAdmin := k.As(0)
	admin := newUidClient(t, k, "admin0", 0)
	for _, d := range []string{"/home/u1", "/home/u2"} {
		if err := admin.Mkdir(d); err != nil && err != ErrExists && err != lib.ErrFSExists {
			t.Fatalf("admin mkdir %s: %v", d, err)
		}
	}
	if err := admin.Create("/home/u1/secret.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.WriteFile("/home/u1/secret.txt", 0, []byte("u1")); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create("/home/u2/notes.txt"); err != nil {
		t.Fatal(err)
	}
	restoreAdmin()

	// u1 lists /home → sees only u1 (their own), NOT u2
	u1 := newUidClient(t, k, "u1sess", uint32(1001), "u1", lib.CapFocus)
	scopeU1 := k.As(1001)
	defer scopeU1()
	ents, err := u1.List("/home")
	if err != nil {
		t.Fatalf("u1 list /home: %v", err)
	}
	if len(ents) != 1 || !strings.EqualFold(ents[0].Name, "u1") {
		t.Fatalf("u1 /home listing leaked: %+v", ents)
	}
	// u1 CAN list their own home contents
	sub, err := u1.List("/home/u1")
	if err != nil || len(sub) != 1 || !strings.EqualFold(sub[0].Name, "SECRET.TXT") {
		t.Fatalf("u1 own listing wrong: %+v err=%v", sub, err)
	}

	// u2 lists /home → sees only u2, NOT u1
	u2 := newUidClient(t, k, "u2sess", uint32(1002), "u2", lib.CapFocus)
	scopeU2 := k.As(1002)
	defer scopeU2()
	ents2, err := u2.List("/home")
	if err != nil {
		t.Fatalf("u2 list /home: %v", err)
	}
	if len(ents2) != 1 || !strings.EqualFold(ents2[0].Name, "u2") {
		t.Fatalf("u2 /home listing leaked: %+v", ents2)
	}

	// guest cannot list /home at all
	guestScope := k.As(7777)
	guest := newUidClient(t, k, "guestx", uint32(7777))
	if _, err := guest.List("/home"); err != lib.ErrFSNoEntry {
		t.Fatalf("guest list /home: %v", err)
	}
	guestScope()

	// admin sees everything
	restoreAdmin()
	admEnts, err := admin.List("/home")
	if err != nil || len(admEnts) != 2 {
		t.Fatalf("admin /home listing wrong: %+v err=%v", admEnts, err)
	}
}

// TestDotDotEscapeDenied pins the AGENTS.md traversal rule: no `..`
// component may resolve a non-admin caller out of their root, relative
// or absolute; the denial is policy-level (FSAccess), pre-storage.
func TestDotDotEscapeDenied(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	restoreAdmin := k.As(0)
	admin := newUidClient(t, k, "admin0", 0)
	for _, d := range []string{"/home/u1", "/home/u2"} {
		if err := admin.Mkdir(d); err != nil && err != ErrExists && err != lib.ErrFSExists {
			t.Fatalf("admin mkdir %s: %v", d, err)
		}
	}
	if err := admin.Create("/home/u1/secret.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.WriteFile("/home/u1/secret.txt", 0, []byte("u1 data")); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create("/tmp/pub.txt"); err != nil {
		t.Fatal(err)
	}
	restoreAdmin()

	u2 := newUidClient(t, k, "u2sess", uint32(1002), "u2", lib.CapFocus)
	scopeU2 := k.As(1002)
	defer scopeU2()

	// rooted-relative climb: secret.txt sits at /home/u2/../u1/...
	if _, err := u2.Stat("../u1/secret.txt"); err != lib.ErrFSAccess {
		t.Fatalf("relative .. stat: %v", err)
	}
	if _, err := u2.ReadFile("../u1/secret.txt", 0, make([]byte, 16)); err != lib.ErrFSAccess {
		t.Fatalf("relative .. read: %v", err)
	}
	if err := u2.Create("../u1/planted.txt"); err != lib.ErrFSAccess {
		t.Fatalf("relative .. create: %v", err)
	}

	// absolute paths with an embedded .. component
	if _, err := u2.Stat("/tmp/../home/u1/secret.txt"); err != lib.ErrFSAccess {
		t.Fatalf("absolute embedded ..: %v", err)
	}
	if err := u2.Delete("/tmp/../home/u1/secret.txt"); err != lib.ErrFSAccess {
		t.Fatalf("absolute .. delete: %v", err)
	}

	// sanity: normal tmp usage through the same session still works
	if _, err := u2.Stat("/tmp/pub.txt"); err != nil {
		t.Fatalf("legit /tmp stat broken: %v", err)
	}
}

// TestRegisterIssuerGate pins the REGISTER authority boundary: only the
// privileged session (kernel-stamped uid 0 = login/init) may feed the
// uid→(name,capmask) table. A guest self-registering with another user's
// NAME would otherwise inherit its /home/<name> root (names are the
// rooting key).
func TestRegisterIssuerGate(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startServer(t, k, stop)

	restoreAdmin := k.As(0)
	admin := newUidClient(t, k, "admin0", 0)
	if err := admin.Mkdir("/home/u1"); err != nil && err != ErrExists && err != lib.ErrFSExists {
		t.Fatalf("admin mkdir: %v", err)
	}
	if err := admin.Mkdir("/home/u3"); err != nil && err != ErrExists && err != lib.ErrFSExists {
		t.Fatalf("admin mkdir u3: %v", err)
	}
	if err := admin.Create("/home/u1/secret.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.WriteFile("/home/u1/secret.txt", 0, []byte("top secret")); err != nil {
		t.Fatal(err)
	}
	restoreAdmin()

	// hostile guest claims u1's identity via self-registration
	guest := newUidClient(t, k, "evil5555")
	scopeGuest := k.As(5555)
	defer scopeGuest()
	if err := guest.Register(5555, "u1", lib.CapAll); err != lib.ErrFSAccess {
		t.Fatalf("guest self-register as 'u1': %v", err)
	}
	// registration must NOT have taken effect: guest stays unregistered —
	// relative paths refused AND rooted view of "u1" not granted
	if err := guest.Create("anything"); err != lib.ErrFSAccess {
		t.Fatalf("guest relative create after denied register: %v", err)
	}
	if _, err := guest.Stat("secret.txt"); err != lib.ErrFSAccess {
		t.Fatalf("guest rooted stat after denied register: %v", err)
	}
	// and the absolute cross-user path is still hidden (guest class)
	if _, err := guest.Stat("/home/u1/secret.txt"); err == nil {
		t.Fatal("guest sees u1 file despite gate")
	}

	// a registered non-admin user cannot register anyone either
	u2 := newUidClient(t, k, "u2sess", uint32(1002), "u2", lib.CapFocus)
	scopeU2 := k.As(1002)
	defer scopeU2()
	if err := u2.Register(uint32(1003), "u3", lib.CapAll); err != lib.ErrFSAccess {
		t.Fatalf("user registering others: %v", err)
	}

	// the privileged issuer still works (regression for the gate itself)
	u3 := newUidClient(t, k, "u3sess", uint32(1003), "u3", lib.CapFocus)
	scopeU3 := k.As(1003)
	defer scopeU3()
	if err := u3.Create("mine.txt"); err != nil {
		t.Fatalf("legit admin-issued registration broken: %v", err)
	}
}

// newUidClient binds an fs client; when uid>0 it registers the session
// under that uid. Per the issuer gate, registration is performed by the
// privileged admin session (uid 0) — the production issuer is login/init.
func newUidClient(t *testing.T, k *lib.FakeKernel, role string, args ...interface{}) *lib.FSClient {
	t.Helper()
	c, err := lib.BindFS(k, role)
	if err != nil {
		t.Fatal(err)
	}
	c.SetBudget(20000)
	if len(args) >= 3 {
		uid := args[0].(uint32)
		name := args[1].(string)
		mask := args[2].(uint64)
		regScope := k.As(0) // privileged issuer scope
		if err := c.Register(uid, name, mask); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		regScope()
	}
	return c
}

// startKFSServer is startServer with the KFS log-structured backend —
// proves the §-port protocol is identical either way (clients never
// notice the format swap).
func startKFSServer(t *testing.T, k *lib.FakeKernel, stop chan struct{}) {
	t.Helper()
	_, win, err := NewRamDisk(testBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := FormatKFS(win); err != nil {
		t.Fatal(err)
	}
	store, err := MountKFS(win)
	if err != nil {
		t.Fatal(err)
	}
	go ServeFS(k, store, ServerOptions{Stop: stop})
	waitPortFS(k)
}

// TestKFSServerProtocolParity drives the same client flows the FAT16
// server tests cover, against KFS: end-to-end file ops + multiuser
// rooting denials must behave identically.
func TestKFSServerProtocolParity(t *testing.T) {
	k := lib.NewFakeKernel()
	stop := make(chan struct{})
	defer close(stop)
	startKFSServer(t, k, stop)

	restoreAdmin := k.As(0)
	admin := newUidClient(t, k, "admin0", 0)
	for _, d := range []string{"/etc", "/home", "/home/u1", "/home/u2", "/tmp"} {
		if err := admin.Mkdir(d); err != nil && err != ErrExists && err != lib.ErrFSExists {
			t.Fatalf("admin mkdir %s: %v", d, err)
		}
	}
	if err := admin.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if n, err := admin.WriteFile("/etc/motd", 0, []byte("kfs motd\n")); err != nil || n != 9 {
		t.Fatalf("admin write %d %v", n, err)
	}
	restoreAdmin()

	u1 := newUidClient(t, k, "u1sess", uint32(1001), "u1", lib.CapFocus)
	scopeU1 := k.As(1001)
	if err := u1.Create("secret.txt"); err != nil {
		t.Fatal(err)
	}
	if n, err := u1.WriteFile("secret.txt", 0, []byte("u1 on kfs")); err != nil || n != 9 {
		t.Fatalf("u1 write %d %v", n, err)
	}
	buf := make([]byte, 32)
	if n, err := u1.ReadFile("secret.txt", 0, buf); err != nil || n != 9 ||
		string(buf[:n]) != "u1 on kfs" {
		t.Fatalf("u1 read n=%d err=%v", n, err)
	}
	if _, err := u1.List("/"); err == nil {
		t.Fatal("u1 root list should be denied (policy parity with fat16)")
	}
	scopeU1()

	u2 := newUidClient(t, k, "u2sess", uint32(1002), "u2", lib.CapFocus)
	scopeU2 := k.As(1002)
	defer scopeU2()
	if _, err := u2.Stat("/home/u1/secret.txt"); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 cross-user stat: %v", err)
	}
	if _, err := u2.ReadFile("/home/u1/secret.txt", 0, buf); err != lib.ErrFSNoEntry {
		t.Fatalf("u2 cross-user read: %v", err)
	}
	if err := u2.Create("/tmp/shared.kfs"); err != nil {
		t.Fatalf("u2 /tmp create: %v", err)
	}
	if _, err := u2.Stat("/etc/motd"); err != nil {
		t.Fatalf("u2 /etc read: %v", err)
	}
	if err := u2.Create("/etc/hax"); err != lib.ErrFSAccess {
		t.Fatalf("u2 /etc create: %v", err)
	}
}
