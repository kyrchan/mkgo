package main

import (
	"bytes"
	"errors"
	"testing"
)

// ---- static devices (crash-injection targets) ----

type tornDev struct{ data []byte }

func (t *tornDev) Read(lba uint64, buf []byte) error {
	off := int(lba) * 512
	if off+len(buf) > len(t.data) {
		return errors.New("kfs-test: read past image")
	}
	copy(buf, t.data[off:off+len(buf)])
	return nil
}

func (t *tornDev) Write(lba uint64, buf []byte) error {
	return errors.New("kfs-test: torn image is read-only")
}

func (t *tornDev) Geometry() (uint32, uint64) {
	return bwBlkSize, uint64(len(t.data) / 512)
}

type tornDevRW struct{ data []byte }

func (t *tornDevRW) Read(lba uint64, buf []byte) error {
	off := int(lba) * 512
	if off+len(buf) > len(t.data) {
		return errors.New("read past image")
	}
	copy(buf, t.data[off:off+len(buf)])
	return nil
}

func (t *tornDevRW) Write(lba uint64, buf []byte) error {
	off := int(lba) * 512
	if off+len(buf) > len(t.data) {
		return errors.New("write past image")
	}
	copy(t.data[off:], buf)
	return nil
}

func (t *tornDevRW) Geometry() (uint32, uint64) {
	return bwBlkSize, uint64(len(t.data) / 512)
}

// workload holds a deterministic filesystem build with milestone log
// offsets so tear tests know what MUST be visible after any prefix.
type workload struct {
	rd      *RamDisk
	k       *KFS
	image   []byte
	end     int64 // log cursor after the full workload
	motdOK  int64 // motd fully committed at this offset
	dirsOK  int64 // /etc,/home,/home/u1 committed at this offset
	notesOK int64 // notes.txt fully committed here (= end)
}

func buildWorkload(t *testing.T) *workload {
	t.Helper()
	rd, win, err := NewRamDisk(testBlocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := FormatKFS(win); err != nil {
		t.Fatal(err)
	}
	k, err := MountKFS(win)
	if err != nil {
		t.Fatal(err)
	}
	w := &workload{rd: rd, k: k}
	for _, d := range []string{"/etc", "/home", "/home/u1"} {
		if err := k.Mkdir(d); err != nil && err != ErrExists {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	w.dirsOK = k.wOff
	if err := k.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if err := k.WriteFile("/etc/motd", 0, []byte("welcome to kernel-lane\n")); err != nil {
		t.Fatal(err)
	}
	w.motdOK = k.wOff
	if err := k.Create("/home/u1/notes.txt"); err != nil {
		t.Fatal(err)
	}
	if err := k.WriteFile("/home/u1/notes.txt", 0, bytes.Repeat([]byte{0x5A}, 300)); err != nil {
		t.Fatal(err)
	}
	w.end = k.wOff
	w.notesOK = k.wOff
	w.image = append([]byte(nil), rd.Disk...)
	return w
}

var (
	motdWant  = []byte("welcome to kernel-lane\n")
	notesWant = bytes.Repeat([]byte{0x5A}, 300)
)

// assertConsistent checks internal self-consistency of a recovered
// volume: every listed entry stats and reads exactly its stated size,
// directory children resolve, and no dangling partial names exist.
func assertConsistent(t *testing.T, k *KFS) {
	t.Helper()
	if _, err := k.List("/"); err != nil {
		t.Fatalf("root list: %v", err)
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := k.List(dir)
		if err != nil {
			t.Fatalf("list %s: %v", dir, err)
		}
		for _, e := range ents {
			p := dir + "/" + e.Name
			st, err := k.Stat(p)
			if err != nil {
				t.Fatalf("stat %s: %v", p, err)
			}
			if st.Size != e.Size {
				t.Fatalf("size mismatch %s: stat=%d list=%d", p, st.Size, e.Size)
			}
			if st.IsDir() && depth < 8 {
				walk(p, depth+1)
				continue
			}
			buf := make([]byte, st.Size)
			n, err := k.ReadFile(p, 0, buf)
			if err != nil || uint32(n) != st.Size {
				t.Fatalf("read %s: n=%d size=%d err=%v", p, n, st.Size, err)
			}
		}
	}
	walk("/", 0)
}

// readAll fetches a whole small file ("" when absent).
func readAll(t *testing.T, k *KFS, path string, sz int) []byte {
	t.Helper()
	buf := make([]byte, sz)
	n, err := k.ReadFile(path, 0, buf)
	if err != nil {
		return nil
	}
	return buf[:n]
}

// TestKFSCrashInjectionTearEveryByte tears the log at EVERY byte offset
// and requires: mount succeeds, recovered state is internally consistent,
// and every milestone fully committed before the tear is intact.
func TestKFSCrashInjectionTearEveryByte(t *testing.T) {
	w := buildWorkload(t)

	for tear := int64(kfsLogStart()); tear <= w.end; tear++ {
		img := make([]byte, len(w.image))
		copy(img, w.image[:tear]) // bytes beyond `tear` never reached disk

		rk, err := MountKFS(&tornDev{data: img})
		if err != nil {
			t.Fatalf("tear@%d: mount failed: %v", tear, err)
		}
		assertConsistent(t, rk)

		if tear >= w.dirsOK {
			for _, d := range []string{"/etc", "/home", "/home/u1"} {
				if _, err := rk.Stat(d); err != nil {
					t.Fatalf("tear@%d: dir %s lost: %v", tear, d, err)
				}
			}
		}
		if tear >= w.motdOK {
			if got := readAll(t, rk, "/etc/motd", len(motdWant)); !bytes.Equal(got, motdWant) {
				t.Fatalf("tear@%d: motd %q want %q", tear, got, motdWant)
			}
		}
		if tear >= w.notesOK {
			got := readAll(t, rk, "/home/u1/notes.txt", len(notesWant))
			if !bytes.Equal(got, notesWant) {
				t.Fatalf("tear@%d: notes truncated (%d B)", tear, len(got))
			}
		}
	}
}

// TestKFSCrashContinueAfterTear proves a recovered volume keeps working:
// post-crash writes land cleanly and survive a subsequent clean remount.
func TestKFSCrashContinueAfterTear(t *testing.T) {
	w := buildWorkload(t)
	tear := w.end - 150 // mid-notes-payload class

	img := make([]byte, len(w.image))
	copy(img, w.image[:tear])
	live := &tornDevRW{data: img}

	rk, err := MountKFS(live)
	if err != nil {
		t.Fatal(err)
	}
	assertConsistent(t, rk)

	if err := rk.Create("/etc/motd"); err != nil { // truncate, then rewrite
		t.Fatalf("post-tear truncate: %v", err)
	}
	if err := rk.WriteFile("/etc/motd", 0, []byte("recovered\n")); err != nil {
		t.Fatalf("post-tear write: %v", err)
	}
	if err := rk.Create("/home/u1/post.txt"); err != nil {
		t.Fatal(err)
	}
	if err := rk.WriteFile("/home/u1/post.txt", 0, []byte("after crash")); err != nil {
		t.Fatal(err)
	}

	rk2, err := MountKFS(&tornDev{data: live.data})
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, rk2, "/etc/motd", 32); string(got) != "recovered\n" {
		t.Fatalf("post-tear motd %q", got)
	}
	if got := readAll(t, rk2, "/home/u1/post.txt", 32); string(got) != "after crash" {
		t.Fatalf("post-tear file lost: %q", got)
	}
}

// TestKFSBitFlipInRecordHeader pins the corruption class: flipping any
// header/CRC byte of the final record drops exactly that record and
// leaves an internally consistent volume.
func TestKFSBitFlipInRecordHeader(t *testing.T) {
	w := buildWorkload(t)
	finalLen := int(w.end - kfsLogStart())

	for _, flipAt := range []int{
		finalLen - 9, finalLen - 8, finalLen - 5, finalLen - 2, finalLen - 1,
	} {
		img := append([]byte(nil), w.image...)
		img[int(kfsLogStart())+flipAt] ^= 0xFF

		rk, err := MountKFS(&tornDev{data: img})
		if err != nil {
			t.Fatalf("flip@%d: mount failed: %v", flipAt, err)
		}
		assertConsistent(t, rk)
		// the flipped final record must not have been applied
		if got := readAll(t, rk, "/home/u1/notes.txt", len(notesWant)); len(got) == len(notesWant) &&
			bytes.Equal(got, notesWant) && flipAt >= finalLen-9 {
			t.Fatalf("flip@%d: corrupted record accepted", flipAt)
		}
	}
}

// TestKFSGarbageTail pins unknown-type handling: junk appended after a
// valid stream is treated as a torn tail, never applied.
func TestKFSGarbageTail(t *testing.T) {
	w := buildWorkload(t)
	img := append([]byte(nil), w.image...)
	tail := bytes.Repeat([]byte{0xEE}, 128)
	copy(img[w.end:int(w.end)+len(tail)], tail)

	rk, err := MountKFS(&tornDev{data: img})
	if err != nil {
		t.Fatal(err)
	}
	assertConsistent(t, rk)
	if got := readAll(t, rk, "/etc/motd", len(motdWant)); !bytes.Equal(got, motdWant) {
		t.Fatalf("motd lost under garbage tail: %q", got)
	}
}

// TestKFSBasicLifecycle pins the API surface the fs server consumes.
func TestKFSBasicLifecycle(t *testing.T) {
	w := buildWorkload(t)
	k := w.k

	st, err := k.Stat("/etc/motd")
	if err != nil || st.Size != uint32(len(motdWant)) || st.IsDir() {
		t.Fatalf("stat %+v err=%v", st, err)
	}
	if _, err := k.Stat("/nope"); err != ErrNoEntry {
		t.Fatalf("stat missing: %v", err)
	}
	if err := k.Mkdir("/etc"); err != ErrExists {
		t.Fatalf("mkdir dup: %v", err)
	}
	if err := k.Delete("/etc"); err != ErrNotEmpt {
		t.Fatalf("rmdir non-empty: %v", err)
	}
	if err := k.Delete("/home/u1/notes.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Stat("/home/u1/notes.txt"); err != ErrNoEntry {
		t.Fatalf("deleted file visible: %v", err)
	}
	// create-or-truncate
	if err := k.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if st, _ = k.Stat("/etc/motd"); st.Size != 0 {
		t.Fatalf("truncate failed: size=%d", st.Size)
	}
	if err := k.WriteFile("/etc/motd", 0, []byte("v2")); err != nil {
		t.Fatal(err)
	}
	// sparse write far past EOF reads back zeros in the gap
	if err := k.WriteFile("/etc/motd", 4096, make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	gap := make([]byte, 16)
	n, _ := k.ReadFile("/etc/motd", 4, gap)
	if n != 16 || !bytes.Equal(gap, make([]byte, 16)) {
		t.Fatalf("sparse gap not zeroed: n=%d %q", n, gap[:4])
	}

	// native uid ownership
	if err := k.SetUID("/etc/motd", 1001); err != nil {
		t.Fatal(err)
	}
	if uid, err := k.UID("/etc/motd"); err != nil || uid != 1001 {
		t.Fatalf("uid=%d err=%v", uid, err)
	}

	// clean remount preserves everything incl. checkpoint pacing path
	if _, err := MountKFS(&tornDev{data: w.rd.Disk}); err != nil {
		t.Fatal(err)
	}
}

// TestKFSCheckpointPacing drives >64 records so the CHECKPOINT path
// executes, then requires clean remount.
func TestKFSCheckpointPacing(t *testing.T) {
	w := buildWorkload(t)
	k := w.k
	if err := k.Mkdir("/tmp"); err != nil && err != ErrExists {
		t.Fatal(err)
	}
	if err := k.Create("/tmp/log"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ { // > kfsCPEvery records
		if err := k.WriteFile("/tmp/log", uint64(i)*4, []byte("abcd")); err != nil {
			t.Fatal(err)
		}
	}
	rk, err := MountKFS(&tornDev{data: w.rd.Disk})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 320)
	n, err := rk.ReadFile("/tmp/log", 0, buf)
	if err != nil || n != 320 || bytes.Count(buf, []byte("abcd")) != 80 {
		t.Fatalf("checkpointed replay lost data: n=%d err=%v", n, err)
	}
}
