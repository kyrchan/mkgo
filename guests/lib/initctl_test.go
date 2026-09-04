//go:build !wasip1

package kern

import "testing"

// TestInitctlRoundTrip pins the shared shell↔init framing: BindInit dials the
// "init" port, call round-trips status+detail through a stub responder that
// mirrors services/init handleInitctl reply shape.
func TestInitctlRoundTrip(t *testing.T) {
	fk := NewFakeKernel()
	fk.Cur = fk.AddSession("shell", 1001, CapFocus|CapPortBind)
	ih := fk.PortCreate(NameInit)
	if ih == InvalidHandle {
		t.Fatal("init port")
	}
	go func() {
		buf := make([]byte, MaxMsg)
		book := NewReplyBook(fk)
		for {
			n := fk.PortRecv(ih, buf)
			if n <= 0 {
				fk.Yield()
				continue
			}
			hdr, ok := ParseHeader(buf[:n])
			if !ok || hdr.RNam == "" {
				continue
			}
			rh, err := book.Bind(hdr.RNam)
			if err != nil {
				continue
			}
			rep := make([]byte, CanonicalHeaderLen+4+len("sid=7"))
			Put16(rep, hdr.Op)
			Put16(rep[2:], hdr.Seq)
			Put32(rep[CanonicalHeaderLen:], InitOK)
			copy(rep[CanonicalHeaderLen+4:], "sid=7")
			fk.PortSend(rh, rep)
			return
		}
	}()

	ic, err := BindInit(fk)
	if err != nil {
		t.Fatal(err)
	}
	ic.SetBudget(20000)
	st, detail, err := ic.Restart("fs")
	if err != nil {
		t.Fatal(err)
	}
	if st != InitOK || detail != "sid=7" {
		t.Fatalf("st=%d detail=%q", st, detail)
	}
	if got := InitStatusText(InitNotFound); got != "not found" {
		t.Fatalf("status text %q", got)
	}
}

// TestBindInitNoServer verifies the "init not responding" path.
func TestBindInitNoServer(t *testing.T) {
	fk := NewFakeKernel()
	fk.Cur = fk.AddSession("shell", 1001, CapFocus)
	if _, err := BindInit(fk); err != ErrBadHandle {
		t.Fatalf("err=%v", err)
	}
}

// TestSplitPolicyPayload covers the subop-4 framing edge cases.
func TestSplitPolicyPayload(t *testing.T) {
	name, yes, ok := SplitPolicyPayload([]byte("fs\x001"))
	if !ok || name != "fs" || !yes {
		t.Fatalf("got %q,%v,%v", name, yes, ok)
	}
	if _, _, ok := SplitPolicyPayload([]byte("fs")); ok {
		t.Fatal("bare name accepted")
	}
	if _, _, ok := SplitPolicyPayload([]byte("fs\x00x")); ok {
		t.Fatal("bad flag accepted")
	}
	if _, _, ok := SplitPolicyPayload([]byte("fs\x001extra")); ok {
		t.Fatal("trailing bytes accepted")
	}
	if _, _, ok := SplitPolicyPayload([]byte("\x001")); !ok {
		t.Fatal("empty name rejected") // empty name parses; init rejects it
	}
}
