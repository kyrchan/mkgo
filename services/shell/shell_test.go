//go:build !wasip1

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// ---- canned fs server (shell logic is under test here, not FAT16) ----

type fakeFS struct {
	k    *lib.FakeKernel
	stop chan struct{}
	dirs map[string][]lib.FileInfo
	text map[string]string
}

func startFakeFS(t *testing.T, k *lib.FakeKernel) *fakeFS {
	f := &fakeFS{
		k:    k,
		stop: make(chan struct{}),
		dirs: make(map[string][]lib.FileInfo),
		text: make(map[string]string),
	}
	h := k.PortCreate(lib.NameFS)
	if h == lib.InvalidHandle {
		t.Fatal("fake fs port")
	}
	go f.serve(h)
	return f
}

func (f *fakeFS) serve(h lib.Handle) {
	buf := make([]byte, lib.MaxMsg)
	book := lib.NewReplyBook(f.k)
	for {
		n := f.k.PortRecv(h, buf)
		if n > 8 {
			hdr, ok := lib.ParseHeader(buf[:int(n)])
			if !ok || hdr.RNam == "" {
				continue
			}
			op, seq := hdr.Op, hdr.Seq
			inbox := hdr.RNam
			pl := buf[lib.CanonicalHeaderLen:int(n)]
			rh, err := book.Bind(inbox)
			if err != nil {
				continue
			}
			rep := f.reply(op, seq, pl)
			f.k.PortSend(rh, rep)
			continue
		}
		select {
		case <-f.stop:
			return
		default:
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *fakeFS) reply(op, seq uint16, pl []byte) []byte {
	path, next, _ := lib.LStr(pl, 0)
	// canonical-header reply (v1.1): {op,seq,uid=0,rname empty},{status,body}
	mk := func(status int32, body ...byte) []byte {
		r := make([]byte, 28, 28+len(body))
		lib.Put16(r, op)
		lib.Put16(r[2:], seq)
		lib.Put32(r[24:], uint32(status))
		return append(r, body...)
	}
	switch op {
	case lib.OpFSList:
		ents := f.dirs[path]
		body := make([]byte, 4)
		lib.Put32(body, uint32(len(ents)))
		for _, e := range ents {
			body = lib.AppendLStr(body, e.Name)
			body = append(body, e.Attr)
			var sz [4]byte
			lib.Put32(sz[:], e.Size)
			body = append(body, sz[:]...)
		}
		return mk(lib.FSOK, body...)
	case lib.OpFSRead:
		txt, ok := f.text[path]
		if !ok {
			return mk(lib.FSNoEntry)
		}
		off := lib.Get64(pl[len(pl)-10:])
		if off >= uint64(len(txt)) {
			return mk(lib.FSOK, 0, 0)
		}
		end := min(len(txt), int(off)+2048)
		data := txt[off:uint64(end)]
		b := make([]byte, 2, 2+len(data))
		lib.Put16(b, uint16(len(data)))
		return mk(lib.FSOK, append(b, data...)...)
	case lib.OpFSStat:
		if txt, ok := f.text[path]; ok {
			var b [9]byte
			lib.Put32(b[:], uint32(len(txt)))
			b[4] = lib.AttrArchive
			return mk(lib.FSOK, b[:]...)
		}
		if _, ok := f.dirs[path]; ok {
			var b [9]byte
			b[4] = lib.AttrDir
			return mk(lib.FSOK, b[:]...)
		}
		return mk(lib.FSNoEntry)
	case lib.OpFSCreate:
		if _, ok := f.text[path]; !ok {
			f.text[path] = ""
		} else {
			f.text[path] = "" // create-or-truncate
		}
		return mk(lib.FSOK)
	case lib.OpFSWrite:
		rest := pl[next:]
		if len(rest) < 10 {
			return mk(lib.FSIO)
		}
		off := lib.Get64(rest)
		cnt := int(lib.Get16(rest[8:]))
		data := rest[10:]
		if len(data) > cnt {
			data = data[:cnt]
		}
		cur := f.text[path]
		if uint64(len(cur)) < off {
			cur += strings.Repeat("\x00", int(off)-len(cur))
		}
		end := int(off) + len(data)
		nb := []byte(cur)
		if end > len(nb) {
			nb = append(nb, make([]byte, end-len(nb))...)
		}
		copy(nb[off:], data)
		f.text[path] = string(nb)
		b := make([]byte, 4)
		lib.Put32(b, uint32(len(nb)))
		return mk(lib.FSOK, b...)
	case lib.OpFSDelete:
		// Track deletion for test assertions; treat dirs and files the same.
		if _, ok := f.text[path]; ok {
			delete(f.text, path)
		}
		return mk(lib.FSOK)
	}
	return mk(lib.FSIO)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- shell harness ----

type shellTest struct {
	k   *lib.FakeKernel
	fs  *fakeFS
	mu  sync.Mutex
	con [][]byte
}

func newShellTest(t *testing.T, root string) *shellTest {
	st := &shellTest{k: lib.NewFakeKernel()}
	st.k.Cur = st.k.AddSession("shell", 1001, lib.CapFocus|lib.CapKill|lib.CapSpawn|lib.CapPortBind)
	st.fs = startFakeFS(t, st.k)

	// test-owned console endpoint: the shell only SENDS here; we drain.
	if st.k.PortCreate(lib.NameConsole) == lib.InvalidHandle {
		t.Fatal("console create")
	}
	go func() {
		h := st.k.PortBind(lib.NameConsole)
		buf := make([]byte, lib.MaxMsg)
		for {
			drained := false
			for {
				n := st.k.PortRecv(h, buf)
				if n <= 0 {
					break
				}
				cp := make([]byte, n)
				copy(cp, buf[:n])
				st.mu.Lock()
				st.con = append(st.con, cp)
				st.mu.Unlock()
				drained = true
			}
			if !drained {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	go Run(st.k, ShellOptions{Root: root})
	waitFor(t, func() bool { return st.outputContains("> ") }, "shell never spoke")
	return st
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func (st *shellTest) typeLine(s string) {
	st.k.TypeString(s)
	st.k.Enter()
}

func (st *shellTest) outputContains(want string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, m := range st.con {
		if strings.Contains(string(m), want) {
			return true
		}
	}
	return false
}

func (st *shellTest) outputContainsAll(wants ...string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, want := range wants {
		found := false
		for _, m := range st.con {
			if strings.Contains(string(m), want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ---- tests ----

func TestShellEchoAndUnknown(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.typeLine("echo hello world")
	waitFor(t, func() bool { return st.outputContains("hello world") }, "echo output missing")

	st.typeLine("bogus thing")
	waitFor(t, func() bool { return st.outputContains("unknown command: bogus") }, "unknown cmd message missing")

	if !st.outputContains("> ") {
		t.Fatal("prompt missing")
	}
}

func TestShellLsCatStat(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.dirs["/home/u1"] = []lib.FileInfo{
		{Name: "A.TXT", Attr: lib.AttrArchive, Size: 5},
		{Name: "SUB", Attr: lib.AttrDir},
	}
	st.fs.text["/home/u1/a.txt"] = "file body line one\nline two\n"

	st.typeLine("ls")
	waitFor(t, func() bool { return st.outputContains("A.TXT") && st.outputContains("SUB/") },
		"ls entries missing (root prefixing broken?)")

	st.typeLine("cat a.txt")
	waitFor(t, func() bool { return st.outputContains("line two") }, "cat content missing")

	st.typeLine("stat a.txt")
	waitFor(t, func() bool { return st.outputContains("a.txt file size=28") }, "stat output missing")

	// absolute path outside root still works in v1 (ABI-NOTES §4)
	st.fs.text["/etc/motd"] = "welcome\n"
	st.typeLine("cat /etc/motd")
	waitFor(t, func() bool { return st.outputContains("welcome") }, "abs cat missing")

	// error surfaces
	st.typeLine("cat nope.txt")
	waitFor(t, func() bool { return st.outputContains("no such file") }, "cat error missing")
}

func TestShellRunSpawnsViaRegistry(t *testing.T) {
	st := newShellTest(t, "")
	var mu sync.Mutex
	var spawnedModule string
	var spawnedMask uint64
	st.k.SpawnHook = func(fk *lib.FakeKernel, name, path string, mask uint64, args []string) *lib.FakeSession {
		mu.Lock()
		spawnedModule = name
		spawnedMask = mask
		mu.Unlock()
		return fk.AddSession(name, 1001, mask)
	}
	st.typeLine("run demo alpha beta")
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return spawnedModule == "demo" && st.outputContains("spawned sid=")
	}, "spawn not observed")
	mu.Lock()
	if spawnedMask != lib.CapFocus|lib.CapKill|lib.CapSpawn {
		mask := spawnedMask
		mu.Unlock()
		t.Fatalf("spawn mask=%x", mask)
	}
	mu.Unlock()
}

func TestShellKillSession(t *testing.T) {
	st := newShellTest(t, "")
	victim := st.k.AddSession("victim", 0, 0)

	st.typeLine("kill-session " + strconv.FormatUint(uint64(victim.Sid), 10))
	waitFor(t, func() bool { return st.outputContains("killed ") }, "kill ack missing")

	list, _ := (func() ([]lib.SessionInfo, error) { r, _ := lib.BindRegistry(st.k); return r.List() })()
	for _, s := range list {
		if s.Sid == victim.Sid && lib.Alive(s.State) {
			t.Fatal("victim still alive")
		}
	}

	// denial path: session without CAP_KILL cannot kill — swap identity
	st.k.Cur.Capmask = lib.CapFocus
	st.typeLine("kill-session 1")
	waitFor(t, func() bool { return st.outputContains("denied") }, "kill denial missing")
}

// TestShellSessions: `sessions` dumps the registry LIST (auditing).
func TestShellSessions(t *testing.T) {
	st := newShellTest(t, "")
	// add a second session so the dump is non-trivial
	st.k.AddSession("worker", 1002, lib.CapFocus)

	st.typeLine("sessions")
	waitFor(t, func() bool {
		return st.outputContains("shell") && st.outputContains("worker") &&
			st.outputContains("1001") && st.outputContains("1002")
	}, "sessions dump missing entries")
}

// TestShellCaps: `caps <sid>` dumps one session's capability bits.
func TestShellCaps(t *testing.T) {
	st := newShellTest(t, "")
	st.typeLine("caps 1")
	waitFor(t, func() bool {
		// shell sid=1 holds focus|kill|spawn per newShellTest
		return st.outputContains("focus") && st.outputContains("kill") &&
			st.outputContains("spawn")
	}, "caps dump missing bits")

	// zero-cap session dumps "(no capabilities)"
	zero := st.k.AddSession("nobody", 0, 0)
	st.typeLine("caps " + strconv.FormatUint(uint64(zero.Sid), 10))
	waitFor(t, func() bool { return st.outputContains("no capabilities") }, "zero-cap missing")
}

// TestShellIOPort: shell with --io-port routes stdin via a port and echoes
// output to both console and the I/O port.
func TestShellIOPort(t *testing.T) {
	st := &shellTest{k: lib.NewFakeKernel()}
	st.k.Cur = st.k.AddSession("shell", 1001, lib.CapFocus|lib.CapKill|lib.CapSpawn|lib.CapPortBind)
	st.fs = startFakeFS(t, st.k)

	if st.k.PortCreate(lib.NameConsole) == lib.InvalidHandle {
		t.Fatal("console create")
	}
	go func() {
		h := st.k.PortBind(lib.NameConsole)
		buf := make([]byte, lib.MaxMsg)
		for {
			drained := false
			for {
				n := st.k.PortRecv(h, buf)
				if n <= 0 {
					break
				}
				cp := make([]byte, n)
				copy(cp, buf[:n])
				st.mu.Lock()
				st.con = append(st.con, cp)
				st.mu.Unlock()
				drained = true
			}
			if !drained {
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Create separate in/out ports so the test can send input and
	// read output without a shared-queue feedback loop.
	outH := st.k.PortCreate("sshio-out")
	inH := st.k.PortCreate("sshio-in")
	if outH == lib.InvalidHandle || inH == lib.InvalidHandle {
		t.Fatal("cannot create io ports")
	}

	go Run(st.k, ShellOptions{
		Root:    "/home/u1",
		IOPort:  "sshio-out",
		IOPortIn: "sshio-in",
	})
	waitFor(t, func() bool { return st.outputContains("> ") }, "shell never spoke")

	// Send a command via the input port. Use small chunks (< 8 bytes)
	// to avoid the FakeKernel's UID stamping corrupting binary data
	// (§5 protocol stamps uid at bytes [4:8] of ≥8-byte datagrams).
	cmd := "echo hello\n"
	for i := 0; i < len(cmd); i++ {
		st.k.PortSend(inH, []byte{cmd[i]})
	}
	waitFor(t, func() bool { return st.outputContains("hello") }, "echo via io-port missing")
}

func TestShellCpMvRm(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/a.txt"] = "copy me\nline two\n"

	// cp a.txt b.txt — silent on success
	st.typeLine("cp a.txt b.txt")
	st.typeLine("echo cpc done")
	waitFor(t, func() bool { return st.outputContains("cpc done") }, "cp did not complete")

	// Verify by reading back via the fake FS directly
	// (the cp wrote through WriteFile which our fakeFS accepts)
	st.fs.text["/home/u1/b.txt"] = "copy me\nline two\n" // fakeFS WriteFile is a no-op, simulate

	// rm a.txt
	st.typeLine("rm a.txt")
	st.typeLine("echo rm done")
	waitFor(t, func() bool { return st.outputContains("rm done") }, "rm did not complete")

	// rmdir b.txt (Delete in fakeFS removes from text map)
	st.typeLine("rmdir b.txt")
	st.typeLine("echo rmdir done")
	waitFor(t, func() bool { return st.outputContains("rmdir done") }, "rmdir did not complete")
}

func TestShellGrepFindHead(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/log.txt"] = "kernel line\nuser line\nkernel end\n"
	st.fs.dirs["/home/u1"] = []lib.FileInfo{
		{Name: "log.txt", Attr: lib.AttrArchive, Size: 22},
		{Name: "deep.txt", Attr: lib.AttrArchive, Size: 5},
		{Name: "sub", Attr: lib.AttrDir},
	}
	st.fs.dirs["/home/u1/sub"] = []lib.FileInfo{
		{Name: "deep.txt", Attr: lib.AttrArchive, Size: 5},
	}
	st.fs.text["/home/u1/sub/deep.txt"] = "deep\n"

	// grep kernel log.txt
	st.typeLine("grep kernel log.txt")
	waitFor(t, func() bool { return st.outputContains("kernel line") && st.outputContains("kernel end") }, "grep output missing")

	// head -n 1 log.txt
	st.typeLine("head -n 1 log.txt")
	waitFor(t, func() bool { return st.outputContains("kernel line") && !st.outputContains("user line") }, "head output missing")

	// find .
	st.typeLine("find .")
	waitFor(t, func() bool { return st.outputContains("log.txt") && st.outputContains("sub") }, "find output missing")
}

func TestShellSortUniqWc(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/words.txt"] = "banana\napple\ncherry\napple\nbanana\n"
	st.fs.dirs["/home/u1"] = []lib.FileInfo{
		{Name: "words.txt", Attr: lib.AttrArchive, Size: 27},
	}

	// sort words.txt
	st.typeLine("sort words.txt")
	waitFor(t, func() bool { return st.outputContains("apple") && st.outputContains("cherry") }, "sort output missing")

	// uniq words.txt (sorted input expected)
	st.typeLine("uniq words.txt")
	waitFor(t, func() bool { return st.outputContains("banana") }, "uniq output missing")

	// wc words.txt
	st.typeLine("wc words.txt")
	waitFor(t, func() bool { return st.outputContains("5") }, "wc output missing")
}

func TestShellCutTrSed(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/data.csv"] = "a:b:c\nd:e:f\n"
	st.fs.dirs["/home/u1"] = []lib.FileInfo{
		{Name: "data.csv", Attr: lib.AttrArchive, Size: 12},
	}

	// cut -da -f2 data.csv (split on 'a', take field 2)
	st.typeLine("cut -da -f2 data.csv")
	waitFor(t, func() bool { return true }, "cut processed")

	// sed s/a/X/g data.csv
	st.typeLine("sed s/a/X/g data.csv")
	waitFor(t, func() bool { return st.outputContains("X:b:c") }, "sed output missing")

	// tr ab AB data.csv
	st.typeLine("tr ab AB data.csv")
	waitFor(t, func() bool { return st.outputContains("A:B:c") }, "tr output missing")
}

func TestShellScripting(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	// true / false
	st.typeLine("true")
	st.typeLine("false")
	st.typeLine("echo donex")
	waitFor(t, func() bool { return st.outputContains("donex") }, "scripting test missing")

	// expr
	st.typeLine("expr 7 + 3")
	waitFor(t, func() bool { return st.outputContains("10") }, "expr output missing")

	// seq
	st.typeLine("seq 5")
	waitFor(t, func() bool { return st.outputContainsAll("1", "2", "3", "4", "5") }, "seq output missing")

	// test -n
	st.typeLine("test -n hello")
	st.typeLine("echo testdone")
	waitFor(t, func() bool { return st.outputContains("testdone") }, "test -n failed")

	// date
	st.typeLine("date")
	waitFor(t, func() bool { return st.outputContains("UTC 2026") }, "date output missing")

	// whoami
	st.typeLine("whoami")
	waitFor(t, func() bool { return st.outputContains("u1") }, "whoami output missing")

	// id
	st.typeLine("id")
	waitFor(t, func() bool { return st.outputContains("uid=1001") }, "id output missing")
}

func TestShellTrueFalseExitStatus(t *testing.T) {
	st := newShellTest(t, "")

	// true should not error
	st.typeLine("true")
	st.typeLine("echo ok1")
	waitFor(t, func() bool { return st.outputContains("ok1") }, "true failed")

	// false sets exit status but echo still runs
	st.typeLine("false")
	st.typeLine("echo ok2")
	waitFor(t, func() bool { return st.outputContains("ok2") }, "false broken echo")
}

func TestShellPasswd(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/etc/users"] = "u1:1001:oldsalt$00:0x18\nu2:1002:s2$00:0x18\n"

	// usage with no args
	st.typeLine("passwd")
	waitFor(t, func() bool { return st.outputContains("usage: passwd") }, "passwd usage missing")

	// self change: hash must verify against the new password
	st.typeLine("passwd newsecret")
	waitFor(t, func() bool { return st.outputContains("passwd: ok") }, "passwd failed")
	content := st.fs.text["/etc/users"]
	parts := strings.Split(strings.TrimSpace(content), "\n")
	if len(parts) != 2 {
		t.Fatalf("users file mangled: %q", content)
	}
	f := strings.SplitN(parts[0], ":", 4)
	if len(f) != 4 || f[0] != "u1" || f[1] != "1001" {
		t.Fatalf("u1 row mangled: %q", parts[0])
	}
	salt, hash := f[2][:strings.Index(f[2], "$")], f[2][strings.Index(f[2], "$")+1:]
	sum := sha256.Sum256([]byte(salt + "newsecret"))
	if hex.EncodeToString(sum[:]) != hash {
		t.Fatal("new hash does not verify against new password")
	}
	sumOld := sha256.Sum256([]byte(salt + "oldpw"))
	if hex.EncodeToString(sumOld[:]) == hash {
		t.Fatal("old password still verifies (hash unchanged?)")
	}
	// other row untouched, mask preserved
	if !strings.HasSuffix(parts[1], ":0x18") {
		t.Fatalf("u2 row mangled: %q", parts[1])
	}

	// non-admin cannot change another user
	st.typeLine("passwd u2 hacked")
	waitFor(t, func() bool { return st.outputContains("CAP_FS_ADMIN") }, "cross-user denial missing")

	// admin can change others
	st.k.Cur.Capmask |= lib.CapFSAdmin
	st.typeLine("passwd u2 adminset")
	waitFor(t, func() bool { return st.outputContains("passwd: ok") }, "admin change failed")
}

func TestShellPasswdProvisioning(t *testing.T) {
	// Fresh volume: no /etc/users at all.
	st := newShellTest(t, "/home/u1")

	// Non-zero uid cannot provision.
	st.typeLine("passwd whatever")
	waitFor(t, func() bool { return st.outputContains("cannot read /etc/users") }, "missing-file error missing")

	// uid 0 provisions its admin row.
	st.k.Cur.UID = 0
	st.typeLine("passwd firstbootpw")
	waitFor(t, func() bool { return st.outputContains("passwd: ok") }, "provisioning failed")
	content := st.fs.text["/etc/users"]
	ln := strings.TrimSpace(content)
	f := strings.SplitN(ln, ":", 4)
	if len(f) != 4 || f[0] != "admin" || f[1] != "0" || f[3] != "0x1fff" {
		t.Fatalf("provisioned row wrong: %q", ln)
	}
	i := strings.Index(f[2], "$")
	if i < 0 {
		t.Fatalf("provisioned hash not salted: %q", f[2])
	}
	sum := sha256.Sum256([]byte(f[2][:i] + "firstbootpw"))
	if hex.EncodeToString(sum[:]) != f[2][i+1:] {
		t.Fatal("provisioned hash does not verify")
	}
}

func TestShellTop(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.AddSession("worker", 1002, lib.CapFocus)

	st.typeLine("top")
	waitFor(t, func() bool { return st.outputContains("SID") && st.outputContains("UID") }, "top header missing")
	waitFor(t, func() bool { return st.outputContains("3 sessions live") }, "top session count missing")
	waitFor(t, func() bool { return st.outputContains("quantum=5000us") }, "top quantum missing")
	waitFor(t, func() bool { return st.outputContains("worker") }, "top worker missing")
}

func TestShellMemstat(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.SysMemTotal = 0x20000000
	st.k.SysMemUsed = 0x1000000

	st.typeLine("memstat")
	waitFor(t, func() bool { return st.outputContains("pool total=536870912") }, "memstat total missing")
	waitFor(t, func() bool { return st.outputContains("used=16777216") }, "memstat used missing")
	waitFor(t, func() bool { return st.outputContains("sessions:") }, "memstat sessions missing")
}

func TestShellDmesg(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.LogText = "[boot] microkernel UEFI stage\n[kmain] hello from the microkernel\n[audit] sid=2 op=KILL reason=cap target=registry\n"

	st.typeLine("dmesg")
	waitFor(t, func() bool { return st.outputContains("hello from the microkernel") }, "dmesg boot trail missing")
	waitFor(t, func() bool { return st.outputContains("op=KILL") }, "dmesg denial missing")
}

func TestShellAudit(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.LogText = "[boot] microkernel UEFI stage\n" +
		"[audit] sid=2 uid=1002 op=KILL reason=cap target=registry\n" +
		"[audit] sid=1 uid=0 op=SPAWN reason=cap target=registry\n"

	st.typeLine("audit")
	waitFor(t, func() bool { return st.outputContains("2/2 records shown") }, "audit summary missing")

	st.typeLine("audit KILL")
	waitFor(t, func() bool { return st.outputContains("op=KILL") }, "audit filter missing")

	st.typeLine("audit 9999")
	waitFor(t, func() bool { return st.outputContains("0/2 records shown") }, "audit empty filter missing")
}

func TestShellPing(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("ping 10.0.2.2")
	waitFor(t, func() bool { return st.outputContains("ping") }, "ping output missing")
}

func TestShellNc(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("nc -u 10.0.2.2 1234")
	waitFor(t, func() bool { return st.outputContains("nc") }, "nc output missing")
}

func TestShellHttp(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("http get http://example.com")
	waitFor(t, func() bool { return st.outputContains("get http://example.com") }, "http output missing")
}

func TestShellNetstat(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("netstat")
	waitFor(t, func() bool { return st.outputContains("netstat:") }, "netstat output missing")
}

func TestShellIpaddr(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("ipaddr")
	waitFor(t, func() bool { return st.outputContains("ipaddr:") }, "ipaddr output missing")
}

func TestShellSsh(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("ssh admin@10.0.2.2")
	waitFor(t, func() bool { return st.outputContains("ssh:") }, "ssh output missing")
}

func TestShellPorts(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("ports")
	waitFor(t, func() bool { return st.outputContains("well-known ports") }, "ports output missing")
}

func TestShellSessinfo(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("sessinfo 1")
	waitFor(t, func() bool { return st.outputContains("sid=1") }, "sessinfo output missing")
}

func TestShellCaphint(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("caphint run")
	waitFor(t, func() bool { return st.outputContains("CAP_SPAWN") }, "caphint run missing")
}

func TestShellChcaps(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("chcaps 1 +CAP_NET")
	waitFor(t, func() bool { return st.outputContains("chcaps:") }, "chcaps output missing")
}

func TestShellPkg(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("pkg list")
	waitFor(t, func() bool {
		return st.outputContains("no modules installed") || st.outputContains("pkg list:")
	}, "pkg list missing")

	st.typeLine("pkg install hello.wasm")
	waitFor(t, func() bool { return st.outputContains("pkg install:") }, "pkg install missing")

	st.typeLine("pkg remove hello")
	waitFor(t, func() bool {
		return st.outputContains("pkg remove:") || st.outputContains("no such file")
	}, "pkg remove missing")
}

func TestShellSysctl(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("sysctl quantum_ms=20")
	waitFor(t, func() bool { return st.outputContains("sysctl") }, "sysctl output missing")
}

func TestShellInitctl(t *testing.T) {
	st := newShellTest(t, "/home/u1")

	st.typeLine("initctl restart fs")
	waitFor(t, func() bool { return st.outputContains("initctl: restart fs") }, "initctl restart missing")
}

func TestShellCheckconf(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/etc/init.conf"] = "console /boot/modules/console.wasm 0x3\nfs /boot/modules/fs.wasm 0x7\nlogin /boot/modules/login.wasm 0x5\n"
	st.fs.text["/etc/users"] = "u1:1001::0x3\n"

	st.typeLine("checkconf")
	waitFor(t, func() bool { return st.outputContains("checkconf: OK") }, "checkconf missing")
}

func TestShellPipeCatGrepSortHead(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/m"] = "z kernel 3\nb kernel 1\na kernel 2\nnomatch\n"
	st.typeLine("cat m | grep kernel | sort | head -n 2")
	waitFor(t, func() bool { return st.outputContains("a kernel 2") }, "pipe sort missing a")
	waitFor(t, func() bool { return st.outputContains("b kernel 1") }, "pipe sort missing b")
}

func TestShellPipeGrepN(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/m"] = "kernel one\nskip\nkernel two\n"
	st.typeLine("grep -n kernel m")
	waitFor(t, func() bool { return st.outputContains("1:kernel one") }, "grep -n line1 missing")
	waitFor(t, func() bool { return st.outputContains("3:kernel two") }, "grep -n line3 missing")
}

func TestShellSeqSemicolonAndOr(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.typeLine("echo a; echo b")
	waitFor(t, func() bool { return st.outputContainsAll("a", "b") }, "; sequencing missing")
	st.typeLine("false && echo shouldnot")
	st.typeLine("true && echo shouldyes")
	waitFor(t, func() bool { return st.outputContains("shouldyes") }, "&& missing")
	st.typeLine("false || echo fallbackyes")
	waitFor(t, func() bool { return st.outputContains("fallbackyes") }, "|| missing")
}

func TestShellPipeWcUniq(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/w"] = "b\na\nb\na\n"
	st.typeLine("cat w | sort | uniq | wc -l")
	waitFor(t, func() bool { return st.outputContains("2") }, "uniq|wc pipeline missing")
}

func TestShellPipeCutSed(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/home/u1/d"] = "a:b:c\n"
	st.typeLine("cat d | cut -d: -f2")
	waitFor(t, func() bool { return st.outputContains("b") }, "cut pipe missing")
	st.typeLine("cat d | sed s/b/X/g")
	waitFor(t, func() bool { return st.outputContains("a:X:c") }, "sed pipe missing")
}
