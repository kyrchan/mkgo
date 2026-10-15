//go:build !wasip1

package conf

import (
	"testing"
)

// Fuzz targets per AGENTS.md practice #4: every wire/config parser gets a
// fuzz target. Validators must be total — no panic, no OOM, sane line
// numbers — on arbitrary input.

func FuzzInitConf(f *testing.F) {
	f.Add("console /boot/modules/console.wasm 0x1000\n")
	f.Add("onlyname\n")
	f.Add("a /p notahex\n")
	f.Add("# comment\n\n")
	f.Add("\x00\xff\xfe\n")
	for _, err := range ValidateInitConf("console c.wasm 0x1 respawn=yes\n") {
		_ = err
	}
	f.Fuzz(func(t *testing.T, s string) {
		errs := ValidateInitConf(s)
		for _, e := range errs {
			if e.Line < 1 {
				t.Fatalf("bad line number %d for %q", e.Line, s)
			}
			if e.File == "" || e.Msg == "" {
				t.Fatalf("empty file/msg for %q", s)
			}
		}
	})
}

func FuzzKernelConf(f *testing.F) {
	f.Add("quantum_us=5000\n")
	f.Add("bogus\n")
	f.Add("a=b=c\n")
	f.Add("\x00=1\n")
	f.Fuzz(func(t *testing.T, s string) {
		errs := ValidateKernelConf(s)
		for _, e := range errs {
			if e.Line < 1 {
				t.Fatalf("bad line number %d for %q", e.Line, s)
			}
		}
	})
}

func FuzzUsersConf(f *testing.F) {
	f.Add("u1:1001:salt$hash:0x18\n")
	f.Add("nocolons\n")
	f.Add("a:b:c:d:e:f\n")
	f.Add(":::\n")
	f.Fuzz(func(t *testing.T, s string) {
		errs := ValidateUsers(s)
		for _, e := range errs {
			if e.Line < 1 {
				t.Fatalf("bad line number %d for %q", e.Line, s)
			}
		}
	})
}

func FuzzTrustedConf(f *testing.F) {
	f.Add("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n")
	f.Add("nothex\n")
	f.Add("# comment\n")
	f.Fuzz(func(t *testing.T, s string) {
		errs := ValidateTrusted(s)
		for _, e := range errs {
			if e.Line < 1 {
				t.Fatalf("bad line number %d for %q", e.Line, s)
			}
		}
	})
}
