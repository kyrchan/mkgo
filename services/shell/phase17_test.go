// services/shell/phase17_test.go -- Phase 17 capability & port introspection.
//
// Tests the non-POSIX tools that expose the capability/port model:
//   - ports: list well-known names + owning sid + uid
//   - sessinfo: show session details (sid, uid, name, state, caps)
//   - caphint: which capability does an action need?
//   - chcaps: grant/revoke cap bits on a live session (CAP_POWER only)
//go:build !wasip1

package main

import (
	"strconv"
	"testing"

	lib "kernel.lane/guests/lib"
)

// TestPhase17PortsList verifies the ports built-in lists well-known names.
func TestPhase17PortsList(t *testing.T) {
	st := newShellTest(t, "")
	// Add sessions with well-known names; the ports command lists sessions
	// whose name is non-empty.
	st.k.AddSession("console", 0, 0)
	st.k.AddSession("login", 0, 0)
	st.k.AddSession("fs", 0, 0)
	st.k.AddSession("net", 0, 0)
	st.typeLine("ports")
	waitFor(t, func() bool {
		return st.outputContains("console") && st.outputContains("login") &&
			st.outputContains("fs") && st.outputContains("net")
	}, "ports output missing well-known names")
}

// TestPhase17SessinfoSelf verifies sessinfo of the shell's own session.
func TestPhase17SessinfoSelf(t *testing.T) {
	st := newShellTest(t, "")
	me := st.k.Cur
	st.typeLine("sessinfo " + strconv.Itoa(int(me.Sid)))
	waitFor(t, func() bool {
		return st.outputContains("sid=") && st.outputContains("name=shell")
	}, "sessinfo self")
}

// TestPhase17SessinfoMissing verifies sessinfo of a nonexistent sid errors.
func TestPhase17SessinfoMissing(t *testing.T) {
	st := newShellTest(t, "")
	st.typeLine("sessinfo 9999")
	waitFor(t, func() bool {
		return st.outputContains("not found")
	}, "sessinfo missing")
}

// TestPhase17Caphint verifies the capability hint table for known actions.
func TestPhase17Caphint(t *testing.T) {
	cases := []struct{ action, want string }{
		{"run", "CAP_SPAWN"},
		{"reboot", "CAP_ADMIN"},
		{"kill-session", "CAP_KILL"},
		{"devices", "CAP_PCI"},
		{"passwd", "CAP_AUTH"},
		{"top", "CAP_ADMIN"},
		{"dmesg", "CAP_ADMIN"},
		{"memstat", "CAP_ADMIN"},
		{"audit", "CAP_ADMIN"},
		{"mount", "CAP_FS_ADMIN"},
	}
	for _, c := range cases {
		st := newShellTest(t, "")
		st.typeLine("caphint " + c.action)
		waitFor(t, func() bool { return st.outputContains(c.want) },
			"caphint "+c.action+" -> "+c.want)
	}
}

// TestPhase17CaphintUnknown verifies unknown actions produce an error.
func TestPhase17CaphintUnknown(t *testing.T) {
	st := newShellTest(t, "")
	st.typeLine("caphint no-such-action")
	waitFor(t, func() bool {
		return st.outputContains("unknown")
	}, "caphint unknown")
}

// TestPhase17ChcapsGrant verifies a CAP_POWER caller can grant caps and
// the change is immediately visible.
func TestPhase17ChcapsGrant(t *testing.T) {
	st := newShellTest(t, "")
	me := st.k.Cur
	me.Capmask = lib.CapPower
	target := st.k.AddSession("target", 1002, 0)
	st.typeLine("chcaps " + strconv.Itoa(int(target.Sid)) + " +CAP_SPAWN")
	waitFor(t, func() bool {
		return target.Capmask&lib.CapSpawn != 0
	}, "target should have CAP_SPAWN after grant")
}

// TestPhase17ChcapsRevoke verifies revocation.
func TestPhase17ChcapsRevoke(t *testing.T) {
	st := newShellTest(t, "")
	me := st.k.Cur
	me.Capmask = lib.CapPower
	target := st.k.AddSession("target", 1002, lib.CapSpawn)
	st.typeLine("chcaps " + strconv.Itoa(int(target.Sid)) + " -CAP_SPAWN")
	waitFor(t, func() bool {
		return target.Capmask&lib.CapSpawn == 0
	}, "target should not have CAP_SPAWN after revoke")
}

// TestPhase17ChcapsDenied verifies a non-admin caller is denied.
func TestPhase17ChcapsDenied(t *testing.T) {
	st := newShellTest(t, "")
	me := st.k.Cur
	me.Capmask = 0
	target := st.k.AddSession("target", 1002, 0)
	st.typeLine("chcaps " + strconv.Itoa(int(target.Sid)) + " +CAP_SPAWN")
	waitFor(t, func() bool {
		return target.Capmask&lib.CapSpawn == 0 && st.outputContains("denied")
	}, "chcaps without CAP_POWER should be denied")
}

// TestPhase17ChcapsBadArgs verifies the usage/pathology branches.
func TestPhase17ChcapsBadArgs(t *testing.T) {
	st := newShellTest(t, "")
	me := st.k.Cur
	me.Capmask = lib.CapPower
	st.typeLine("chcaps")
	waitFor(t, func() bool { return st.outputContains("usage") }, "usage")
	st.typeLine("chcaps 1 +badcap")
	waitFor(t, func() bool { return st.outputContains("unknown cap") }, "unknown cap")
	st.typeLine("chcaps 1 XCAP_KILL")
	waitFor(t, func() bool { return st.outputContains("bad prefix") }, "bad prefix")
}
