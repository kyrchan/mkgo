package main

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"kernel.lane/guests/lib"
)

// syncBuf is a goroutine-safe output sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startConsole(b *kern.Bus, stop chan struct{}) *syncBuf {
	out := &syncBuf{}
	go func() { Serve(b, out, Options{Stop: stop}) }()
	waitPort(b)
	return out
}

func waitPort(b *kern.Bus) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !b.HasPort(kern.NameConsole) {
		time.Sleep(time.Millisecond)
	}
}

func waitOutput(t *testing.T, out *syncBuf, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(out.String()), []byte(want)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("output missing %q; got:\n%s", want, out.String())
}

func TestRelayTaggedAndUntagged(t *testing.T) {
	b := kern.NewBus()
	stop := make(chan struct{})
	defer close(stop)
	out := startConsole(b, stop)

	if err := b.SendTo(kern.NameConsole, []byte("[login] auth service online")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "[login] auth service online\n")

	if err := b.SendTo(kern.NameConsole, []byte("raw line no tag")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "[console] raw line no tag\n")

	if err := b.SendTo(kern.NameConsole, []byte("[audit] sid=3 op=KILL reason=cap target=registry")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "reason=cap target=registry\n")
}

func TestFIFOOrderPreserved(t *testing.T) {
	b := kern.NewBus()
	stop := make(chan struct{})
	defer close(stop)
	out := startConsole(b, stop)

	const n = 20
	for i := 0; i < n; i++ {
		if err := b.SendTo(kern.NameConsole, []byte("[t] m"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	waitOutput(t, out, "[t] m"+string(rune('a'+n-1))+"\n")
	got := out.String()
	want := ""
	for i := 0; i < n; i++ {
		want += "[t] m" + string(rune('a'+i)) + "\n"
	}
	if got != want {
		t.Fatalf("FIFO broken:\n got %q\nwant %q", got, want)
	}
}

func TestBindFallbackWhenNameTaken(t *testing.T) {
	b := kern.NewBus()
	// someone else already owns "console" (e.g. stale instance): the
	// service must fall back to bind (fan-in alias), not die.
	owner := b.PortCreate(kern.NameConsole)
	if owner == kern.InvalidHandle {
		t.Fatal("setup")
	}
	waitPort(b)
	stop := make(chan struct{})
	done := make(chan struct{})
	out := &syncBuf{}
	go func() { defer close(done); Serve(b, out, Options{Stop: stop}) }()

	// message sent to the shared name must reach the bound relay
	if err := b.SendTo(kern.NameConsole, []byte("[shell] hi via alias")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "[shell] hi via alias\n")
	close(stop)
	<-done
}

func TestCRLFTrimmed(t *testing.T) {
	b := kern.NewBus()
	stop := make(chan struct{})
	defer close(stop)
	out := startConsole(b, stop)
	if err := b.SendTo(kern.NameConsole, []byte("[x] line\r")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, out, "[x] line\n")
	if bytes.Contains([]byte(out.String()), []byte("\r")) {
		t.Fatalf("CR leaked: %q", out.String())
	}
}
