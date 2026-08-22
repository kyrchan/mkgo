package main

import (
	"strings"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

const confText = `# boot order matters
console /boot/modules/console.wasm 0x8
login   /boot/modules/login.wasm 0x48 respawn=yes
fs      /boot/modules/fs.wasm 0x2
`

func TestParseConf(t *testing.T) {
	svcs, err := ParseConf(confText)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 3 {
		t.Fatalf("n=%d", len(svcs))
	}
	if svcs[0].Name != "console" || svcs[0].Capmask != 0x8 || !svcs[0].Respawn {
		t.Fatalf("console=%+v", svcs[0])
	}
	if svcs[1].Capmask != 0x48 {
		t.Fatalf("login mask=%x", svcs[1].Capmask)
	}
	if !svcs[2].Respawn {
		t.Fatal("respawn must default to yes")
	}

	bad := []string{
		"onlyname",
		"a /p notahex",
		"a /p 0x8 respawnyes",
	}
	for _, b := range bad {
		if _, err := ParseConf(b); err == nil {
			t.Errorf("accepted %q", b)
		}
	}
}

func TestParseKnobs(t *testing.T) {
	k := ParseKnobs("# c\nquantum_ms = 50\n\nlog=debug\n")
	if len(k) != 2 || k["quantum_ms"] != "50" || k["log"] != "debug" {
		t.Fatalf("knobs=%v", k)
	}
}

type recorder struct{ mu, lines []string } // simple; tests are single-goroutine readers

func (r *recorder) log(s string) { r.lines = append(r.lines, s) }
func (r *recorder) has(sub string) bool {
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestBootOrderAndKnobs(t *testing.T) {
	k := lib.NewFakeKernel()
	k.Cur = k.AddSession("init", 0, lib.CapAll)

	var order []string
	k.SpawnHook = func(fk *lib.FakeKernel, name, path string, mask uint64, args []string) *lib.FakeSession {
		order = append(order, name)
		return fk.AddSession(name, 0, mask)
	}

	rec := &recorder{}
	stop := make(chan struct{})
	go Run(k, InitOptions{
		Services: mustConf(t, confText),
		Knobs:    "quantum_ms=50",
		Log:      rec.log,
		Stop:     stop,
	})
	wait(t, func() bool { return len(order) == 3 }, "spawns incomplete")
	if strings.Join(order, ",") != "console,login,fs" {
		t.Fatalf("boot order %v", order)
	}
	wait(t, func() bool { return rec.has("spawned fs sid=") }, "fs spawn log missing")
	wait(t, func() bool { return rec.has("rejected (registry lacks SETCONF)") },
		"knob rejection not tolerated/logged")
	close(stop)
}

func TestSupervisionRespawnsAndRespectsPolicy(t *testing.T) {
	k := lib.NewFakeKernel()
	k.Cur = k.AddSession("init", 0, lib.CapAll)

	conf := mustConf(t, "a /m/a.wasm 0x8 respawn=yes\nb /m/b.wasm 0x8 respawn=no\n")
	rec := &recorder{}
	opts := InitOptions{Services: conf, Log: rec.log, PollEvery: 200}
	done := make(chan struct{})
	go func() { Run(k, opts); close(done) }()
	wait(t, func() bool { return rec.has("spawned b sid=") }, "initial spawns missing")

	reg, _ := lib.BindRegistry(k)
	list, _ := reg.List()
	var aSid uint32
	for _, s := range list {
		if s.Name == "a" {
			aSid = s.Sid
		}
	}

	// kill both (admin identity)
	fkKill := func(sid uint32) { reg.Kill(sid) }
	fkKill(aSid)
	var bSid uint32
	for _, s := range list {
		if s.Name == "b" {
			bSid = s.Sid
		}
	}
	fkKill(bSid)

	wait(t, func() bool {
		return strings.Count(rec.joined(), "respawning") >= 1 && rec.has("spawned a sid=")
	}, "a never respawned")

	// b stays dead (respawn=no): count b spawns — must remain exactly 1
	time.Sleep(150 * time.Millisecond)
	bSpawns := strings.Count(rec.joined(), "spawned b sid=")
	if bSpawns != 1 {
		t.Fatalf("b spawned %d times despite respawn=no", bSpawns)
	}
}

// joined flattens the log for counting.
func (r *recorder) joined() string { return strings.Join(r.lines, "\n") }

func TestSpawnGiveUpAfterMaxFails(t *testing.T) {
	k := lib.NewFakeKernel()
	k.Cur = k.AddSession("init", 0, lib.CapAll)
	k.SpawnHook = func(fk *lib.FakeKernel, name, path string, mask uint64, args []string) *lib.FakeSession {
		return nil // spawn always refused (sid=0xFFFFFFFF)
	}
	rec := &recorder{}
	go Run(k, InitOptions{
		Services:  mustConf(t, "doomed /m/d.wasm 0x0 respawn=yes"),
		Log:       rec.log,
		PollEvery: 100,
	})
	wait(t, func() bool { return rec.has("giving up on doomed") }, "never gave up")
	if n := strings.Count(rec.joined(), "failed ("); n < maxSpawnFails {
		t.Fatalf("only %d attempts logged", n)
	}
}

func mustConf(t *testing.T, text string) []Service {
	t.Helper()
	svcs, err := ParseConf(text)
	if err != nil {
		t.Fatal(err)
	}
	return svcs
}

func wait(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}
