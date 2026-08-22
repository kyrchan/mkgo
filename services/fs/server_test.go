package main

import (
	"bytes"
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

	// mkdir /etc + write /etc/motd + read back
	if err := cli.Mkdir("/etc"); err != nil {
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
	if err != nil || len(ents) != 1 || ents[0].Name != "ETC" || !ents[0].IsDir() {
		t.Fatalf("list=%+v err=%v", ents, err)
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

	if err := cli.Mkdir("/home"); err != nil {
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
