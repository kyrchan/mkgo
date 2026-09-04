//go:build !wasip1

package conf

import (
	"strings"
	"testing"
)

func TestInitConfValid(t *testing.T) {
	text := "# boot order\nconsole /boot/modules/console.wasm 0x1000\n" +
		"fs fs.wasm 1018 respawn=no\nlogin login.wasm 0x1008 respawn=yes\n"
	if errs := ValidateInitConf(text); len(errs) != 0 {
		t.Fatalf("valid init.conf rejected: %v", errs)
	}
	if errs := ValidateInitConf(""); len(errs) != 0 {
		t.Fatalf("empty init.conf rejected: %v", errs)
	}
}

func TestInitConfBad(t *testing.T) {
	cases := []struct{ line, want string }{
		{"onlyname", "want <name>"},
		{"a /p notahex", "bad capmask"},
		{"a /p 0x8 respawnyes", "bad policy"},
		{"a /p 0x8 respawn=maybe", "bad policy"},
		{"averylongservicenameX /p 0x8", "bad service name"},
		{"\x00a /p 0x8", "NUL byte"},
	}
	for _, c := range cases {
		errs := ValidateInitConf(c.line + "\n")
		if len(errs) != 1 {
			t.Errorf("%q: got %v, want 1 error", c.line, errs)
			continue
		}
		if !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%q: error %q lacks %q", c.line, errs[0].Error(), c.want)
		}
		if errs[0].Line != 1 || !strings.Contains(errs[0].Error(), FileInitConf+":1:") {
			t.Errorf("%q: bad location: %v", c.line, errs[0])
		}
	}
	// long lines are errors, not silent truncations
	long := "a /p 0x8 " + strings.Repeat("x", MaxLineLen)
	if errs := ValidateInitConf(long); len(errs) != 1 || !strings.Contains(errs[0].Msg, "too long") {
		t.Fatalf("long line: %v", errs)
	}
}

func TestKernelConfValid(t *testing.T) {
	text := "# knobs\nquantum_us=5000\nlog_level=1\naudit_mask=255\npreempt=1\nquantum_ms=20\n"
	if errs := ValidateKernelConf(text); len(errs) != 0 {
		t.Fatalf("valid kernel.conf rejected: %v", errs)
	}
}

func TestKernelConfBad(t *testing.T) {
	cases := []struct{ line, want string }{
		{"noequals", "want key=value"},
		{"=5", "want key=value"},
		{"bogus_knob=1", "unknown knob"},
		{"quantum_us=fast", "non-numeric"},
		{"quantum_us=5", "out of range"},
		{"quantum_us=99999999", "out of range"},
		{"quantum_ms=0", "out of range"},
		{"log_level=9", "out of range"},
		{"audit_mask=256", "out of range"},
		{"preempt=2", "out of range"},
	}
	for _, c := range cases {
		errs := ValidateKernelConf(c.line + "\n")
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.line, errs, c.want)
		}
	}
}

func TestUsersValid(t *testing.T) {
	text := "# users\nu1:1001:u1salt$abcdef:0x18\n" +
		"admin:0::0x1fff\n" + // provisioning row: empty hash is legal
		"u2:1002:s2$00:18\n" // bare-hex capmask is legal
	if errs := ValidateUsers(text); len(errs) != 0 {
		t.Fatalf("valid users rejected: %v", errs)
	}
}

func TestUsersBad(t *testing.T) {
	cases := []struct{ line, want string }{
		{"nocolons", "want name:uid"},
		{"a:b:c", "want name:uid"},
		{":1001:s$h:0x1", "bad user name"},
		{"bad name:1:s$h:0x1", "bad user name"},
		{"u1:notanum:s$h:0x1", "bad uid"},
		{"u1:1:nosaltseparator:0x1", "bad hash"},
		{"u1:1:$:0x1", "bad hash"},
		{"u1:1:s$:0x1", "bad hash"},
		{"u1:1:s$h:nothex", "bad capmask"},
		{"u1:1:s$h:", "bad capmask"},
	}
	for _, c := range cases {
		errs := ValidateUsers(c.line + "\n")
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.line, errs, c.want)
		}
	}
}

func TestTrustedValid(t *testing.T) {
	text := "# keys\n" + strings.Repeat("ab", 32) + "\n" + strings.Repeat("00", 32) + "\n"
	if errs := ValidateTrusted(text); len(errs) != 0 {
		t.Fatalf("valid trusted rejected: %v", errs)
	}
	if errs := ValidateTrusted(""); len(errs) != 0 {
		t.Fatalf("empty trusted rejected: %v", errs)
	}
}

func TestTrustedBad(t *testing.T) {
	cases := []struct{ line, want string }{
		{"nothex!!", "bad key"},
		{strings.Repeat("ab", 31), "bad key"}, // 31 bytes
		{strings.Repeat("ab", 33), "bad key"}, // 33 bytes
		{"", ""}, // blank is fine — filtered below
	}
	for _, c := range cases {
		if c.line == "" {
			continue
		}
		errs := ValidateTrusted(c.line + "\n")
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.line, errs, c.want)
		}
	}
}

func TestValidateAllCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("badline\n")
	}
	errs := ValidateAll(b.String(), b.String(), b.String(), b.String())
	if len(errs) != MaxErrors {
		t.Fatalf("got %d errors, want cap %d", len(errs), MaxErrors)
	}
	if errs := ValidateAll("", "", "", ""); len(errs) != 0 {
		t.Fatalf("empty all rejected: %v", errs)
	}
}

func TestKnobHelpers(t *testing.T) {
	if idx, ok := KnobIndex("quantum_us"); !ok || idx != 0 {
		t.Fatalf("KnobIndex quantum_us = %d,%v", idx, ok)
	}
	if _, ok := KnobIndex("bogus"); ok {
		t.Fatal("KnobIndex accepted bogus")
	}
	if c, ok := NormalizeKnob("quantum_ms"); !ok || c != "quantum_us" {
		t.Fatalf("NormalizeKnob quantum_ms = %q,%v", c, ok)
	}
	if _, ok := NormalizeKnob("bogus"); ok {
		t.Fatal("NormalizeKnob accepted bogus")
	}
}
