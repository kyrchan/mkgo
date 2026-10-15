// Package conf implements the Phase 19 config-file parsers and validators
// (AGENTS.md Phase 19): /etc/init.conf, /etc/kernel.conf, /etc/users and
// /etc/trusted.
//
// The kernel never parses these files — init.wasm owns init.conf/kernel.conf
// application and the shell's checkconf built-in owns validation. Every
// validator here is total over arbitrary input ( overlong lines, NUL bytes,
// unbalanced delimiters are errors, never panics); see conf_fuzz_test.go.
//
// init.conf grammar mirrors services/init ParseConf exactly (one service per
// line: <name> <path> <capmask-hex> [respawn=yes|no]) so checkconf accepts
// everything init accepts — no false positives on working configs.
package conf

import (
	"encoding/hex"
	"strconv"
	"strings"
)

// MaxLineLen caps a single config line; longer lines are reported, not
// truncated (prevents a 4 KiB paste from becoming a valid-looking prefix).
const MaxLineLen = 256

// MaxErrors caps ValidateAll output (shell prints at most this many).
const MaxErrors = 10

// Error is one config syntax violation.
type Error struct {
	File string // e.g. "/etc/init.conf"
	Line int    // 1-based
	Msg  string
}

func (e Error) Error() string {
	return e.File + ":" + strconv.Itoa(e.Line) + ": " + e.Msg
}

// File names as seen from the shell (fs paths).
const (
	FileInitConf   = "/etc/init.conf"
	FileKernelConf = "/etc/kernel.conf"
	FileUsers      = "/etc/users"
	FileTrusted    = "/etc/trusted"
)

// ValidateAll runs every validator in boot order and returns at most
// MaxErrors entries. Missing files are the caller's concern (shell reports
// "not found" itself); empty text validates as clean.
func ValidateAll(initConf, users, trusted, kernelConf string) []Error {
	var out []Error
	push := func(errs []Error) {
		for _, e := range errs {
			if len(out) >= MaxErrors {
				return
			}
			out = append(out, e)
		}
	}
	push(ValidateInitConf(initConf))
	push(ValidateUsers(users))
	push(ValidateTrusted(trusted))
	push(ValidateKernelConf(kernelConf))
	return out
}

// ValidateInitConf checks init.conf text: blank lines and #-comments are
// skipped; every other line must be
//
//	<name> <path> <capmask-hex> [respawn=yes|no]
//
// with capmask in 0x-prefixed or bare hex (mirrors init.ParseConf).
func ValidateInitConf(text string) []Error {
	return ValidateInitConfFile(FileInitConf, text)
}

// ValidateInitConfFile is ValidateInitConf with an overridable file label.
func ValidateInitConfFile(file, text string) []Error {
	var out []Error
	for i, raw := range strings.Split(text, "\n") {
		ln := i + 1
		if len(raw) > MaxLineLen {
			out = append(out, Error{file, ln, "line too long"})
			continue
		}
		if strings.IndexByte(raw, 0) >= 0 {
			out = append(out, Error{file, ln, "NUL byte"})
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			out = append(out, Error{file, ln, "want <name> <path> <capmask> [respawn]"})
			continue
		}
		if len(fields[0]) == 0 || len(fields[0]) > 15 {
			out = append(out, Error{file, ln, "bad service name"})
			continue
		}
		if len(fields[1]) == 0 {
			out = append(out, Error{file, ln, "bad module path"})
			continue
		}
		if _, err := strconv.ParseUint(strings.TrimPrefix(fields[2], "0x"), 16, 64); err != nil {
			out = append(out, Error{file, ln, "bad capmask " + strconv.Quote(fields[2])})
			continue
		}
		if len(fields) >= 4 && fields[3] != "respawn=yes" && fields[3] != "respawn=no" {
			out = append(out, Error{file, ln, "bad policy " + strconv.Quote(fields[3])})
			continue
		}
	}
	return out
}

// KnownKnobs is the fixed /etc/kernel.conf key list (AGENTS.md Phase 19:
// free-form keys are rejected so typos fail loudly at checkconf time).
// Values are registry knob indexes (ABI §7 ops 11/12).
var KnownKnobs = map[string]uint8{
	"quantum_us": 0,
	"log_level":  1,
	"audit_mask": 2,
}

// KnobIndex maps a canonical kernel.conf key to its registry knob index.
func KnobIndex(key string) (uint8, bool) {
	idx, ok := KnownKnobs[key]
	return idx, ok
}

// NormalizeKnob maps user-facing aliases to canonical keys:
// quantum_ms and quantum are millisecond spellings of quantum_us.
func NormalizeKnob(key string) (string, bool) {
	switch key {
	case "quantum_us", "log_level", "audit_mask", "preempt":
		return key, true
	case "quantum_ms", "quantum":
		return "quantum_us", true
	}
	return "", false
}

// ValidateKernelConf checks kernel.conf text: key=value per line,
// #-comments allowed. Keys must be known; values must be numeric and in
// range (mirrors the kernel clamps in core/preempt.cc + knob store).
func ValidateKernelConf(text string) []Error {
	return ValidateKernelConfFile(FileKernelConf, text)
}

// ValidateKernelConfFile is ValidateKernelConf with an overridable label.
func ValidateKernelConfFile(file, text string) []Error {
	var out []Error
	for i, raw := range strings.Split(text, "\n") {
		ln := i + 1
		if len(raw) > MaxLineLen {
			out = append(out, Error{file, ln, "line too long"})
			continue
		}
		if strings.IndexByte(raw, 0) >= 0 {
			out = append(out, Error{file, ln, "NUL byte"})
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			out = append(out, Error{file, ln, "want key=value"})
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		canon, ok := NormalizeKnob(key)
		if !ok {
			out = append(out, Error{file, ln, "unknown knob " + strconv.Quote(key)})
			continue
		}
		num, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			out = append(out, Error{file, ln, "non-numeric value for " + key})
			continue
		}
		if msg := checkKnobRange(canon, key, num); msg != "" {
			out = append(out, Error{file, ln, msg})
		}
	}
	return out
}

// checkKnobRange enforces the kernel-side clamps so checkconf rejects values
// the kernel would silently ignore.
func checkKnobRange(canon, key string, num uint64) string {
	switch canon {
	case "quantum_us":
		us := num
		if key == "quantum_ms" || key == "quantum" {
			us = num * 1000
		}
		if us < 100 || us > 200000 {
			return key + " out of range [100..200000]us"
		}
	case "log_level":
		if num > 3 {
			return "log_level out of range [0..3]"
		}
	case "audit_mask":
		if num > 255 {
			return "audit_mask out of range [0..255]"
		}
	case "preempt":
		if num > 1 {
			return "preempt out of range [0..1]"
		}
	}
	return ""
}

// ValidateUsers checks /etc/users text: name:uid:salted-hash:capmask per
// line (Phase 10). The hash field may be empty (first-boot provisioning
// writes admin:0::0x1fff before the first passwd); a non-empty hash must
// carry salt$hex form. Capmask is 0x-prefixed or bare hex.
func ValidateUsers(text string) []Error {
	return ValidateUsersFile(FileUsers, text)
}

// ValidateUsersFile is ValidateUsers with an overridable label.
func ValidateUsersFile(file, text string) []Error {
	var out []Error
	for i, raw := range strings.Split(text, "\n") {
		ln := i + 1
		if len(raw) > MaxLineLen {
			out = append(out, Error{file, ln, "line too long"})
			continue
		}
		if strings.IndexByte(raw, 0) >= 0 {
			out = append(out, Error{file, ln, "NUL byte"})
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) != 4 {
			out = append(out, Error{file, ln, "want name:uid:hash:capmask"})
			continue
		}
		if parts[0] == "" || strings.ContainsAny(parts[0], " \t") {
			out = append(out, Error{file, ln, "bad user name"})
			continue
		}
		if _, err := strconv.ParseUint(parts[1], 10, 32); err != nil {
			out = append(out, Error{file, ln, "bad uid " + strconv.Quote(parts[1])})
			continue
		}
		if parts[2] != "" {
			dollar := strings.IndexByte(parts[2], '$')
			if dollar <= 0 || dollar == len(parts[2])-1 {
				out = append(out, Error{file, ln, "bad hash (want salt$hex)"})
				continue
			}
		}
		body := strings.TrimPrefix(strings.TrimPrefix(parts[3], "0x"), "0X")
		if body == "" {
			out = append(out, Error{file, ln, "bad capmask " + strconv.Quote(parts[3])})
			continue
		}
		if _, err := strconv.ParseUint(body, 16, 64); err != nil {
			out = append(out, Error{file, ln, "bad capmask " + strconv.Quote(parts[3])})
			continue
		}
	}
	return out
}

// TrustedKeySize is the ed25519 public key size in bytes (Phase 18).
const TrustedKeySize = 32

// ValidateTrusted checks /etc/trusted text: one hex ed25519 public key per
// line, #-comments allowed (mirrors services/pkg readTrusted, strict:
// malformed lines are errors here, silently skipped there).
func ValidateTrusted(text string) []Error {
	return ValidateTrustedFile(FileTrusted, text)
}

// ValidateTrustedFile is ValidateTrusted with an overridable label.
func ValidateTrustedFile(file, text string) []Error {
	var out []Error
	for i, raw := range strings.Split(text, "\n") {
		ln := i + 1
		if len(raw) > MaxLineLen {
			out = append(out, Error{file, ln, "line too long"})
			continue
		}
		if strings.IndexByte(raw, 0) >= 0 {
			out = append(out, Error{file, ln, "NUL byte"})
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := hex.DecodeString(line)
		if err != nil || len(key) != TrustedKeySize {
			out = append(out, Error{file, ln, "bad key (want 64 hex chars)"})
			continue
		}
	}
	return out
}
