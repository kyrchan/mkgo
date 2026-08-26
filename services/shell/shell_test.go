//go:build !wasip1

package main

import (
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
	path, _, _ := lib.LStr(pl, 0)
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
	case lib.OpFSWrite:
		return mk(lib.FSOK, 0, 0, 0, 0)
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
	st.k.Cur = st.k.AddSession("shell", 1001, lib.CapFocus|lib.CapKill|lib.CapSpawn)
	st.fs = startFakeFS(t, st.k)

	// test-owned console endpoint: the shell only SENDS here; we drain.
	if st.k.PortCreate(lib.NameConsole) == lib.InvalidHandle {
		t.Fatal("console create")
	}
	go func() {
		h := st.k.PortBind(lib.NameConsole)
		buf := make([]byte, lib.MaxMsg)
		for {
			n := st.k.PortRecv(h, buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				st.mu.Lock()
				st.con = append(st.con, cp)
				st.mu.Unlock()
			}
			time.Sleep(time.Millisecond)
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
