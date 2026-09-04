// services/shell/phase19_test.go -- Phase 19 supervision & config surface.
//
// Covers the non-POSIX control plane that makes init + registry manageable:
//   - sysctl: read/write kernel knobs (registry ops 11/12, CAP_CONF-gated)
//   - initctl: restart/reload-conf/apply-knobs/respawn via the "init" port
//   - checkconf: validate /etc/init.conf + /etc/users + /etc/trusted +
//     /etc/kernel.conf before commit (services/shell/conf validators)
//   - caps/sessinfo: cap-source display (login vs chcaps vs init)
//go:build !wasip1

package main

import (
	"strconv"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// stubInit serves the initctl protocol like services/init handleInitctl:
// restart/reload/apply/policy succeed, unknown names get not_found.
func stubInit(t *testing.T, k *lib.FakeKernel) {
	t.Helper()
	h := k.PortCreate(lib.NameInit)
	if h == lib.InvalidHandle {
		t.Fatal("init port")
	}
	go func() {
		buf := make([]byte, lib.MaxMsg)
		book := lib.NewReplyBook(k)
		for {
			n := k.PortRecv(h, buf)
			if n <= 0 {
				k.Yield()
				continue
			}
			hdr, ok := lib.ParseHeader(buf[:n])
			if !ok || hdr.RNam == "" {
				continue
			}
			rh, err := book.Bind(hdr.RNam)
			if err != nil {
				continue
			}
			pl := buf[lib.CanonicalHeaderLen:n]
			var st uint32 = lib.InitOK
			detail := ""
			switch hdr.Op {
			case lib.InitSubRestart:
				if string(pl) == "nosuch" {
					st = lib.InitNotFound
					detail = "nosuch"
				} else {
					detail = "sid=9"
				}
			case lib.InitSubReload:
				detail = "2 services"
			case lib.InitSubApplyKnobs:
				detail = "knobs applied"
			case lib.InitSubPolicy:
				name, yes, ok := lib.SplitPolicyPayload(pl)
				if !ok {
					st = lib.InitBadName
				} else if yes {
					detail = "respawn=" + name + "=yes"
				} else {
					detail = "respawn=" + name + "=no"
				}
			default:
				st = lib.InitBadName
				detail = "unknown subop"
			}
			rep := make([]byte, lib.CanonicalHeaderLen+4+len(detail))
			lib.Put16(rep, hdr.Op)
			lib.Put16(rep[2:], hdr.Seq)
			lib.Put32(rep[lib.CanonicalHeaderLen:], st)
			copy(rep[lib.CanonicalHeaderLen+4:], detail)
			k.PortSend(rh, rep)
		}
	}()
}

func TestPhase19SysctlGet(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.KnobsByIdx[0] = 5000
	st.k.KnobsByIdx[1] = 1
	st.k.KnobsByIdx[2] = 255

	st.typeLine("sysctl quantum_us")
	waitFor(t, func() bool { return st.outputContains("quantum_us = 5000") }, "sysctl get missing")

	st.typeLine("sysctl")
	waitFor(t, func() bool {
		return st.outputContains("quantum_us = 5000") &&
			st.outputContains("log_level = 1") &&
			st.outputContains("audit_mask = 255")
	}, "sysctl list missing")
}

func TestPhase19SysctlSetDenied(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.KnobsByIdx[0] = 5000

	st.typeLine("sysctl quantum_ms=20")
	waitFor(t, func() bool { return st.outputContains("denied (need CAP_CONF)") }, "set denial missing")
	if st.k.KnobsByIdx[0] != 5000 {
		t.Fatalf("knob changed without CAP_CONF: %d", st.k.KnobsByIdx[0])
	}
}

func TestPhase19SysctlSetOK(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.k.Cur.Capmask |= lib.CapConf
	st.k.KnobsByIdx[0] = 5000

	st.typeLine("sysctl quantum_ms=20")
	waitFor(t, func() bool { return st.outputContains("quantum_us = 20000") }, "set ok missing")
	if st.k.KnobsByIdx[0] != 20000 {
		t.Fatalf("knob=%d want 20000", st.k.KnobsByIdx[0])
	}

	st.typeLine("sysctl log_level=2")
	waitFor(t, func() bool { return st.outputContains("log_level = 2") }, "log_level set missing")
}

func TestPhase19SysctlUnknown(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.typeLine("sysctl bogus")
	waitFor(t, func() bool { return st.outputContains("unknown key") }, "unknown key missing")
	st.typeLine("sysctl quantum_us=fast")
	waitFor(t, func() bool { return st.outputContains("bad value") }, "bad value missing")
}

func TestPhase19InitctlRestart(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	stubInit(t, st.k)

	st.typeLine("initctl restart fs")
	waitFor(t, func() bool { return st.outputContains("initctl: restart fs ok (sid=9)") }, "restart ok missing")

	st.typeLine("initctl restart nosuch")
	waitFor(t, func() bool { return st.outputContains("not found") }, "restart not-found missing")
}

func TestPhase19InitctlReloadApplyPolicy(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	stubInit(t, st.k)

	st.typeLine("initctl reload-conf")
	waitFor(t, func() bool { return st.outputContains("initctl: reload ok (2 services)") }, "reload missing")

	st.typeLine("initctl apply-knobs")
	waitFor(t, func() bool { return st.outputContains("initctl: apply-knobs ok") }, "apply-knobs missing")

	st.typeLine("initctl respawn fs no")
	waitFor(t, func() bool { return st.outputContains("respawn=fs=no") }, "respawn policy missing")

	st.typeLine("initctl bogus")
	waitFor(t, func() bool { return st.outputContains("unknown subcommand") }, "bad subcommand missing")
}

func TestPhase19InitctlNoServer(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	// no stubInit: "init" port does not exist
	st.typeLine("initctl restart fs")
	waitFor(t, func() bool { return st.outputContains("init not responding") }, "no-server missing")
}

func TestPhase19CheckconfOK(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/etc/init.conf"] = "console /boot/modules/console.wasm 0x1000\nfs fs.wasm 0x1018 respawn=no\n"
	st.fs.text["/etc/users"] = "u1:1001:salt$ab:0x18\n"
	st.fs.text["/etc/trusted"] = ""
	st.fs.text["/etc/kernel.conf"] = "quantum_us=5000\nlog_level=1\n"

	st.typeLine("checkconf")
	waitFor(t, func() bool { return st.outputContains("checkconf: OK") }, "checkconf OK missing")
}

func TestPhase19CheckconfBad(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.fs.text["/etc/init.conf"] = "onlyname\n"
	st.fs.text["/etc/users"] = "nocolons\n"
	st.fs.text["/etc/trusted"] = "nothex\n"
	st.fs.text["/etc/kernel.conf"] = "bogus_knob=1\n"

	st.typeLine("checkconf")
	waitFor(t, func() bool {
		return st.outputContains("/etc/init.conf:1:") &&
			st.outputContains("/etc/users:1:") &&
			st.outputContains("/etc/trusted:1:") &&
			st.outputContains("/etc/kernel.conf:1:") &&
			st.outputContains("error(s)")
	}, "checkconf errors missing")
}

func TestPhase19CheckconfMissing(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	// fresh ramdisk: none of the files exist — skipped, still OK
	st.typeLine("checkconf")
	waitFor(t, func() bool {
		return st.outputContains("not found (skipped)") && st.outputContains("checkconf: OK")
	}, "checkconf skip missing")
}

func TestPhase19CheckconfStdin(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	st.typeLine("echo onlyname | checkconf --stdin")
	waitFor(t, func() bool { return st.outputContains("error(s)") }, "stdin checkconf missing")
}

func TestPhase19CapsSource(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	// self: AddSession stamps CapSource=2 (init-issued)
	st.typeLine("caps")
	waitFor(t, func() bool { return st.outputContains("source=init") }, "self source missing")

	// target granted via chcaps stamps source=1
	st.k.Cur.Capmask |= lib.CapPower
	target := st.k.AddSession("target", 1002, 0)
	st.typeLine("chcaps " + strconv.Itoa(int(target.Sid)) + " +CAP_SPAWN")
	waitFor(t, func() bool { return target.Capmask&lib.CapSpawn != 0 }, "grant missing")
	st.typeLine("caps " + strconv.Itoa(int(target.Sid)))
	waitFor(t, func() bool { return st.outputContains("source=chcaps") }, "chcaps source missing")
}

func TestPhase19SessinfoSource(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	me := st.k.Cur
	st.typeLine("sessinfo " + strconv.Itoa(int(me.Sid)))
	waitFor(t, func() bool {
		return st.outputContains("source=init") && st.outputContains("caps=")
	}, "sessinfo source/caps missing")
}

// TestPhase19InitctlLatency bounds the initctl round trip (catches budget
// regressions that would stall the serial gate's typed commands).
func TestPhase19InitctlLatency(t *testing.T) {
	st := newShellTest(t, "/home/u1")
	stubInit(t, st.k)
	start := time.Now()
	st.typeLine("initctl restart fs")
	waitFor(t, func() bool { return st.outputContains("restart fs ok") }, "restart ok missing")
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("initctl took %v", d)
	}
}
