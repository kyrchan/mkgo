package kern

import (
	"bytes"
	"strings"
	"testing"
)

func TestBusCreateBindSemantics(t *testing.T) {
	b := NewBus()
	if h := b.PortCreate("console"); h == InvalidHandle {
		t.Fatal("create console failed")
	}
	if h := b.PortCreate("console"); h != InvalidHandle {
		t.Fatal("second owner allowed")
	}
	if h := b.PortBind("nope"); h != InvalidHandle {
		t.Fatal("bind to missing name succeeded")
	}
	if h := b.PortBind("console"); h == InvalidHandle {
		t.Fatal("bind failed")
	}
	for _, n := range []string{"", "way-too-long-name-x"} {
		if h := b.PortCreate(n); h != InvalidHandle {
			t.Fatalf("created invalid name %q", n)
		}
	}
}

func TestBusSendRecvSemantics(t *testing.T) {
	b := NewBus()
	own := b.PortCreate("q")
	peer := b.PortBind("q")
	if own == InvalidHandle || peer == InvalidHandle {
		t.Fatal("setup")
	}
	// empty payload → -1 exactly like the kernel
	if rc := b.PortSend(own, nil); rc != StatusErr {
		t.Fatalf("empty send rc=%d want -1", rc)
	}
	// oversize → -1
	if rc := b.PortSend(own, make([]byte, MaxMsg+1)); rc != StatusErr {
		t.Fatalf("oversize send rc=%d", rc)
	}
	msg := []byte("hello ports")
	if rc := b.PortSend(peer, msg); rc != StatusOK {
		t.Fatalf("send rc=%d", rc)
	}
	buf := make([]byte, 4) // truncating recv: kernel drops the message
	// after copying min(len, cap), so the tail is lost — exact ABI v1.
	n := b.PortRecv(own, buf)
	if n != 4 || !bytes.Equal(buf, msg[:4]) {
		t.Fatalf("trunc recv n=%d %q", n, buf)
	}
	if n := b.PortRecv(own, make([]byte, MaxMsg)); n != 0 {
		t.Fatalf("tail should be gone (kernel drops msg), got n=%d", n)
	}
	if n := b.PortRecv(own, make([]byte, 8)); n != 0 {
		t.Fatalf("empty recv n=%d want 0", n)
	}
	if rc := b.PortSend(Handle(999), msg); rc != StatusErr {
		t.Fatal("bad handle send accepted")
	}
	// queue depth 32 → would-block
	for i := 0; i < 32; i++ {
		if rc := b.PortSend(peer, []byte("x")); rc != StatusOK {
			t.Fatalf("fill i=%d rc=%d", i, rc)
		}
	}
	if rc := b.PortSend(peer, []byte("x")); rc != StatusWouldBlock {
		t.Fatalf("overflow rc=%d want -2", rc)
	}
}

func TestInputAndFocus(t *testing.T) {
	b := NewBus()
	h := b.PortCreate("shell")
	b.TypeString("hi")
	b.Enter()
	got := ""
	for i := 0; i < 3; i++ {
		ev, ok := PollInput(b)
		if !ok {
			t.Fatalf("input %d missing", i)
		}
		if ev.Kind != KeyDown {
			t.Fatalf("kind=%d", ev.Kind)
		}
		if ev.Codepoint == '\r' {
			got += "\n"
		} else {
			got += string(rune(ev.Codepoint))
		}
	}
	if got != "hi\n" {
		t.Fatalf("typed %q", got)
	}
	if _, ok := PollInput(b); ok {
		t.Fatal("extra event")
	}
	b.FocusSet(h)
	if b.Focused != "shell" {
		t.Fatalf("focused=%q", b.Focused)
	}
}

func TestRegistryDirectRPC(t *testing.T) {
	fk := NewFakeKernel()
	admin := fk.AddSession("admin", 0, CapAll)

	rc, err := BindRegistry(fk)
	if err != nil {
		t.Fatal(err)
	}
	list, err := rc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "kernel" || list[1].Name != "admin" {
		t.Fatalf("list=%+v", list)
	}

	caps, err := rc.Caps(admin.Sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 8 || caps[0].Rights != CapKill {
		t.Fatalf("caps=%+v", caps)
	}

	// spawn within rights
	fk.Cur = admin
	sid, err := rc.Spawn("shell", "shell", CapFocus|CapSpawn, "u1")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := rc.List()
	if err != nil {
		t.Fatal(err)
	}
	var found *SessionInfo
	for i := range sess {
		if sess[i].Sid == sid {
			found = &sess[i]
		}
	}
	if found == nil || found.Name != "shell" {
		t.Fatalf("spawned session missing: %+v", sess)
	}

	// escalation denied → ErrSpawnDenied: a session with SPAWN but not
	// every bit may not mint itself admin rights
	dev := fk.AddSession("dev", 100, CapSpawn|CapFocus)
	fk.Cur = dev
	c3, _ := BindRegistry(fk)
	if _, err := c3.Spawn("x", "x", CapAll); err != ErrSpawnDenied {
		t.Fatalf("escalation err=%v", err)
	}
	// no SPAWN bit → no reply at all → ErrNoReply
	plain := fk.AddSession("plain", 1000, 0)
	fk.Cur = plain
	c2, _ := BindRegistry(fk)
	c2.c.Budget = 200 // keep the test fast
	if _, err := c2.Spawn("y", "y", 0); err != ErrNoReply {
		t.Fatalf("capless spawn err=%v", err)
	}

	// KILL without bit replies -1 and audits
	if st, err := c2.Kill(sid); err != nil || st != -1 {
		t.Fatalf("kill st=%d err=%v", st, err)
	}
	if len(fk.Audit) == 0 || !strings.Contains(fk.Audit[len(fk.Audit)-1], "reason=cap") {
		t.Fatalf("audit=%v", fk.Audit)
	}
	// KILL with bit zeroes to zombie
	fk.Cur = admin
	if st, err := rc.Kill(sid); err != nil || st != 0 {
		t.Fatalf("admin kill st=%d err=%v", st, err)
	}
	list, _ = rc.List()
	for _, s := range list {
		if s.Sid == sid && Alive(s.State) {
			t.Fatalf("sid %d still alive after kill", sid)
		}
	}
	// unknown op → audited, silent
	c2.c.Budget = 200
	fk.Cur = admin
	if _, err := c2.c.Request(rc.rg, 99, nil); err != ErrNoReply {
		t.Fatalf("unknown op err=%v", err)
	}
	if !strings.Contains(strings.Join(fk.Audit, "\n"), "reason=op") {
		t.Fatalf("missing reason=op audit: %v", fk.Audit)
	}
}

func TestInboxClientRoundTrip(t *testing.T) {
	b := NewBus()
	srvPort := b.PortCreate(NameFS)

	cli, err := NewInboxClient(b, "fs")
	if err != nil {
		t.Fatal(err)
	}
	// server side: ReplyBook caches one alias per client
	book := NewReplyBook(b)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, MaxMsg)
		for {
			n := b.PortRecv(srvPort, buf)
			if n >= CanonicalHeaderLen {
				hdr, _ := ParseHeader(buf[:int(n)])
				if hdr.RNam == "" {
					continue
				}
				rh, err := book.Bind(hdr.RNam)
				if err != nil {
					t.Error(err)
					return
				}
				// replies carry the canonical header too; uid left 0
				rep := FrameCanonical(hdr.Op, hdr.Seq, "", []byte("pong"))
				if rc := b.PortSend(rh, rep); rc != StatusOK {
					t.Errorf("reply rc=%d", rc)
				}
				return
			}
			b.Yield()
		}
	}()

	rep, err := cli.InboxRequest(srvPort, OpFSStat, pathPayload("/etc/motd"))
	if err != nil {
		t.Fatal(err)
	}
	// inbox-mode reply layout: {canonical header}{payload} — "pong" @24
	if string(rep[24:]) != "pong" {
		t.Fatalf("rep=%q", rep[24:])
	}
	<-done

	// seq mismatch filtering: a stale reply for another seq is ignored
	cli.seq++
	stale := FrameCanonical(OpFSStat, cli.seq+77, "", nil)
	if rc := b.PortSend(cli.Inbox(), stale); rc != StatusOK {
		t.Fatal("stale inject failed")
	}
	go func() {
		// fake server answers only the NEW request
		buf := make([]byte, MaxMsg)
		for {
			n := b.PortRecv(srvPort, buf)
			if n >= CanonicalHeaderLen && Get16(buf[0:2]) == OpFSList {
				hdr, _ := ParseHeader(buf[:int(n)])
				rh, _ := book.Bind(hdr.RNam)
				b.PortSend(rh, FrameCanonical(OpFSList, hdr.Seq, "", []byte{1, 0}))
				return
			}
			b.Yield()
		}
	}()
	if _, err := cli.InboxRequest(srvPort, OpFSList, pathPayload("/")); err != nil {
		t.Fatalf("stale reply broke matching: %v", err)
	}
}

func TestReplyBookCachesHandles(t *testing.T) {
	b := NewBus()
	b.PortCreate("in.a") // inbox ports are owned by the client side
	h1, err := NewReplyBook(b).Bind("in.a")
	if err != nil {
		t.Fatal(err)
	}
	rb := NewReplyBook(b)
	h2, err := rb.Bind("in.a")
	if err != nil || h2 == InvalidHandle {
		t.Fatal(err)
	}
	if h3, err := rb.Bind("in.a"); err != nil || h3 != h2 {
		t.Fatal("alias not cached")
	}
	if h1 == h2 {
		t.Fatal("distinct books shared handle")
	}
	if _, err := rb.Bind("missing"); err == nil {
		t.Fatal("bound missing name")
	}
}
