package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const testBlocks = 16384 // 8 MiB, the AGENTS.md disk size

func newFS(t *testing.T) (*RamDisk, *FAT) {
	t.Helper()
	rd, win, err := NewRamDisk(testBlocks)
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
	return rd, fat
}

func TestFormatMountGeometry(t *testing.T) {
	_, f := newFS(t)
	if f.fatSz == 0 || f.clusters < 0xFF5 {
		t.Fatalf("fatSz=%d clusters=%d", f.fatSz, f.clusters)
	}
	t.Logf("8MiB: fatSz=%d sectors/FAT clusters=%d dataLba=%d", f.fatSz, f.clusters, f.dataLba)

	// bad geometry rejected
	if err := Format(&fakeDev{bs: 1024}, ""); err == nil {
		t.Fatal("non-512 sector format accepted")
	}
}

type fakeDev struct{ bs uint32 }

func (d *fakeDev) Read(uint64, []byte) error  { return nil }
func (d *fakeDev) Write(uint64, []byte) error { return nil }
func (d *fakeDev) Geometry() (uint32, uint64) { return d.bs, 16384 }

func TestMkdirWriteReadDelete(t *testing.T) {
	_, f := newFS(t)

	for _, d := range []string{"/etc", "/home", "/home/u1", "/boot"} {
		if err := f.Mkdir(d); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// duplicate rejected
	if err := f.Mkdir("/etc"); err != ErrExists {
		t.Fatalf("dup mkdir err=%v", err)
	}
	// missing parent
	if err := f.Mkdir("/nope/child"); err != ErrNoEntry {
		t.Fatalf("orphan mkdir err=%v", err)
	}

	msg := []byte("welcome to kernel-lane\n")
	if err := f.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/etc/motd", 0, msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg)+16)
	n, err := f.ReadFile("/etc/motd", 0, buf)
	if err != nil || n != len(msg) || !bytes.Equal(buf[:n], msg) {
		t.Fatalf("read n=%d err=%v %q", n, err, buf[:n])
	}

	// stat
	st, err := f.Stat("/etc/motd")
	if err != nil || st.Size != uint32(len(msg)) || st.IsDir() {
		t.Fatalf("stat=%+v err=%v", st, err)
	}
	if st.Name != "MOTD" {
		t.Fatalf("name=%q", st.Name)
	}

	// delete then ENOENT
	if err := f.Delete("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat("/etc/motd"); err != ErrNoEntry {
		t.Fatalf("post-delete stat err=%v", err)
	}
}

func TestLargeFileMultiCluster(t *testing.T) {
	_, f := newFS(t)
	if err := f.Mkdir("/home"); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 300*1000) // ~293 KiB ≈ 293 clusters of 1KiB
	rand.Read(big)
	if err := f.Create("/home/blob.bin"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/home/blob.bin", 0, big); err != nil {
		t.Fatal(err)
	}
	back := make([]byte, len(big))
	n, err := f.ReadFile("/home/blob.bin", 0, back)
	if err != nil || n != len(big) || !bytes.Equal(back, big) {
		t.Fatalf("big roundtrip n=%d err=%v equal=%v", n, err, bytes.Equal(back, big))
	}
	st, _ := f.Stat("/home/blob.bin")
	if st.Size != uint32(len(big)) {
		t.Fatalf("size=%d want %d", st.Size, len(big))
	}
}

// TestSparseGapZeroedAcrossRealloc pins the zero-on-alloc rule: recycled
// clusters must never surface stale bytes through a sparse write gap
// (cross-file stale-data disclosure class).
func TestSparseGapZeroedAcrossRealloc(t *testing.T) {
	_, f := newFS(t)
	if err := f.Mkdir("/tmp"); err != nil {
		t.Fatal(err)
	}

	// victim file: recognizable pattern across many clusters, then delete
	if err := f.Create("/tmp/victim.dat"); err != nil {
		t.Fatal(err)
	}
	pattern := bytes.Repeat([]byte{0xA7}, 5*2048) // 10 sectors of poison
	if err := f.WriteFile("/tmp/victim.dat", 0, pattern); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete("/tmp/victim.dat"); err != nil {
		t.Fatal(err)
	}

	// new file: single byte written far past EOF — everything before it
	// is a gap that POSIX/FAT semantics require to read as zeros
	if err := f.Create("/tmp/gap.dat"); err != nil {
		t.Fatal(err)
	}
	const off = 9 * 1024 // inside the 10th cluster; needs the recycled chain
	if err := f.WriteFile("/tmp/gap.dat", off, []byte{0x42}); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, off+1)
	n, err := f.ReadFile("/tmp/gap.dat", 0, got)
	if err != nil || n != len(got) {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	for i, b := range got {
		if i == off {
			if b != 0x42 {
				t.Fatalf("payload byte wrong at %d: %x", i, b)
			}
			continue
		}
		if b != 0x00 {
			t.Fatalf("stale byte %x leaked into sparse gap at %d (victim pattern was 0xA7)", b, i)
		}
	}
}

func TestOffsetWritesAndReads(t *testing.T) {
	_, f := newFS(t)
	if err := f.Mkdir("/etc"); err != nil {
		t.Fatal(err)
	}
	if err := f.Create("/etc/kernel.cfg"); err != nil {
		t.Fatal(err)
	}
	// holey write pattern across cluster + sector boundaries
	if err := f.WriteFile("/etc/kernel.cfg", 1500, []byte("quantum=50ms")); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/etc/kernel.cfg", 3, []byte("log=debug")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := f.ReadFile("/etc/kernel.cfg", 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	got := buf[:n]
	if !bytes.HasPrefix(got, []byte{0, 0, 0}) {
		t.Fatalf("prefix not zeroed: %q", got[:10])
	}
	if !bytes.Equal(got[3:12], []byte("log=debug")) || !bytes.Equal(got[1500:1512], []byte("quantum=50ms")) {
		t.Fatalf("patched content wrong: %q %q", got[0:20], got[1495:1520])
	}
	st, _ := f.Stat("/etc/kernel.cfg")
	if st.Size != 1512 {
		t.Fatalf("size=%d want 1512", st.Size)
	}
	// read past EOF → 0 bytes
	n2, _ := f.ReadFile("/etc/kernel.cfg", 99999, buf)
	if n2 != 0 {
		t.Fatalf("past-EOF read n=%d", n2)
	}
}

func TestDirectoryListingAndNesting(t *testing.T) {
	_, f := newFS(t)
	for _, p := range []string{"/home", "/home/u1", "/home/u2"} {
		if err := f.Mkdir(p); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"/home/u1/a.txt", "/home/u1/b.log"}
	for _, fp := range files {
		if err := f.Create(fp); err != nil {
			t.Fatal(err)
		}
		if err := f.WriteFile(fp, 0, []byte(fp)); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := f.List("/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name)
		if !e.IsDir() {
			t.Fatalf("root entry %q not dir", e.Name)
		}
	}
	if len(names) != 1 || names[0] != "HOME" {
		t.Fatalf("root=%v", names)
	}
	u1, err := f.List("/home/u1")
	if err != nil || len(u1) != 2 {
		t.Fatalf("u1 list=%+v err=%v", u1, err)
	}
	if u1[0].Name != "A.TXT" || u1[0].Size != uint32(len("/home/u1/a.txt")) {
		t.Fatalf("u1[0]=%+v", u1[0])
	}
	// ls on a file → ErrNotDir
	if _, err := f.List(files[0]); err != ErrNotDir {
		t.Fatalf("ls-file err=%v", err)
	}
	// nested delete requires empty dir
	if err := f.Delete("/home"); err != ErrNotEmpt {
		t.Fatalf("rm non-empty dir err=%v", err)
	}
	for _, fp := range files {
		if err := f.Delete(fp); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Delete("/home/u1"); err != nil {
		t.Fatalf("rm empty dir: %v", err)
	}
	if _, err := f.List("/home/u1"); err != ErrNoEntry {
		t.Fatalf("gone dir err=%v", err)
	}
}

func TestNameValidation(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"readme.txt", "README.TXT", true},
		{"a", "A", true},
		{"autoexec.bat", "AUTOEXEC.BAT", true},
		{"12345678.123", "12345678.123", true},
		{"UPPER.C", "UPPER.C", true},
		{"toolongname.txt", "", false},
		{"ext.toolong", "", false},
		{"con", "", false},
		{"sp ace.txt", "", false},
		{".hidden", "", false},
	}
	for _, c := range cases {
		got, err := validate83(c.in)
		if !c.ok {
			if err == nil {
				t.Errorf("%q accepted", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q rejected: %v", c.in, err)
			continue
		}
		if dotted := dotted11(got); dotted != c.want {
			t.Errorf("%q → %q want %q", c.in, dotted, c.want)
		}
	}
}

// dotted11 renders an 11-byte short name in dotted form.
func dotted11(b [11]byte) string {
	base := string(b[0:8])
	ext := string(b[8:11])
	for len(base) > 0 && base[len(base)-1] == ' ' {
		base = base[:len(base)-1]
	}
	for len(ext) > 0 && ext[len(ext)-1] == ' ' {
		ext = ext[:len(ext)-1]
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// TestBlockWindowProtocol exercises the §3 mailbox itself: magic check,
// request-id incrementing, completion matching, chunked >8-sector IO.
func TestBlockWindowProtocol(t *testing.T) {
	rd, win, err := NewRamDisk(2048) // 1 MiB
	if err != nil {
		t.Fatal(err)
	}
	bs, nb := win.Geometry()
	if bs != 512 || nb != 2048 {
		t.Fatalf("geometry %d/%d", bs, nb)
	}
	pattern := make([]byte, 4096)
	rand.Read(pattern)
	if err := win.Write(100, pattern); err != nil { // spans two requests
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	if err := win.Read(100, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, pattern) {
		t.Fatal("window roundtrip mismatch")
	}
	if err := win.Write(3000, make([]byte, 512)); err == nil {
		t.Fatal("out-of-range lba accepted")
	}
	rd.Mu.Lock()
	defer rd.Mu.Unlock()
	if !bytes.Equal(rd.Disk[100*512:101*512], pattern[:512]) {
		t.Fatal("disk content mismatch at lba 100")
	}

	// header validation
	if _, err := NewBlockWindow(make([]byte, bwWindowMin)); err != ErrBlockWindow {
		t.Fatalf("bogus window accepted: %v", err)
	}
}

// TestMtoolsCompat cross-validates our FAT16 against mtools when
// available (~/.local/osdev-root). Skipped silently otherwise.
func TestMtoolsCompat(t *testing.T) {
	mdir := filepath.Join(os.Getenv("HOME"), ".local", "osdev-root", "usr", "bin", "mdir")
	if _, err := os.Stat(mdir); err != nil {
		t.Skip("mtools not installed")
	}
	img := filepath.Join(t.TempDir(), "disk.img")

	// plain host file as BlockDev (no window involved — pure FAT test)
	dev := &fileDev{}
	if err := dev.create(img, testBlocks*512); err != nil {
		t.Fatal(err)
	}
	if err := Format(dev, "SYSDISK"); err != nil {
		t.Fatal(err)
	}
	f, err := Mount(dev)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir("/etc"); err != nil {
		t.Fatal(err)
	}
	if err := f.Create("/etc/motd"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("/etc/motd", 0, []byte("hello from lane-services\n")); err != nil {
		t.Fatal(err)
	}
	if err := dev.flush(); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(mdir, "-i", img, "::/")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mdir failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("ETC")) {
		t.Fatalf("mdir output missing ETC:\n%s", out)
	}
	cmd = exec.Command(filepath.Join(filepath.Dir(mdir), "mtype"), "-i", img, "::/etc/motd")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mtype failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("hello from lane-services")) {
		t.Fatalf("mtype got:\n%s", out)
	}
}

// fileDev is a raw file-backed BlockDev for mtools interop tests.
type fileDev struct {
	f    *os.File
	data []byte
}

func (d *fileDev) create(path string, size int) error {
	data := make([]byte, size)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		return err
	}
	d.f, d.data = f, data
	return nil
}

func (d *fileDev) Read(lba uint64, buf []byte) error {
	copy(buf, d.data[int(lba)*512:int(lba)*512+len(buf)])
	return nil
}

func (d *fileDev) Write(lba uint64, buf []byte) error {
	copy(d.data[int(lba)*512:], buf)
	return nil
}

func (d *fileDev) Geometry() (uint32, uint64) { return 512, uint64(len(d.data)) / 512 }

func (d *fileDev) flush() error {
	_, err := d.f.WriteAt(d.data, 0)
	if err != nil {
		return err
	}
	return d.f.Sync()
}
