package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"kernel.lane/guests/lib"
)

// raceSafeMirror wraps a buffer for concurrent Write/String.
type raceSafeMirror struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *raceSafeMirror) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *raceSafeMirror) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

func TestMirrorFace(t *testing.T) {
	b := kern.NewBus()
	stop := make(chan struct{})
	defer close(stop)
	var mirror raceSafeMirror
	out := &syncBuf{}
	go Serve(b, out, Options{Stop: stop, Mirror: &mirror})
	waitPort(b)

	if err := b.SendTo(kern.NameConsole, []byte("[net] link up")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(mirror.String(), "[net] link up") {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(mirror.String(), "[net] link up\n") {
		t.Fatalf("mirror missing line: %q", mirror.String())
	}
	if !strings.Contains(out.String(), "[net] link up") {
		t.Fatal("primary output lost")
	}
}
