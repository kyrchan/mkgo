// Package pkg implements Phase 18 module package management:
// signature verification (ed25519), install/list/remove, and a host-testable
// core that the shell's `pkg` built-in wraps.
package pkg

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	lib "kernel.lane/guests/lib"
)

// FS is the subset of fs.wasm the package manager needs.
type FS interface {
	List(p string) ([]lib.FileInfo, error)
	ReadFile(p string, off uint64, buf []byte) (int, error)
	WriteFile(p string, off uint64, data []byte) (int, error)
	Create(p string) error
	Delete(p string) error
	Stat(p string) (lib.FileInfo, error)
}

// Manager owns the package index and trusted keys.
type Manager struct {
	fs        FS
	trusted   []ed25519.PublicKey
	modulesDir string
}

// New builds a Manager; reads /etc/trusted for hex-encoded ed25519 public
// keys (one per line, lines starting with # are comments). modulesDir is
// typically "/boot/modules".
func New(f FS, modulesDir string) *Manager {
	m := &Manager{fs: f, modulesDir: modulesDir}
	m.trusted = m.readTrusted()
	return m
}

// readTrusted parses /etc/trusted into a list of public keys.
// Missing/unreadable file => empty list (no keys trusted).
func (m *Manager) readTrusted() []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	if m.fs == nil {
		return keys
	}
	st, err := m.fs.Stat("/etc/trusted")
	if err != nil {
		return keys
	}
	buf := make([]byte, int(st.Size))
	n, err := m.fs.ReadFile("/etc/trusted", 0, buf)
	if err != nil || n == 0 {
		return keys
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := hex.DecodeString(line)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}

// TrustedKeyCount returns how many keys are loaded (for diagnostics).
func (m *Manager) TrustedKeyCount() int { return len(m.trusted) }

// extractABIVer finds the custom section named `abi_ver` and returns its
// first byte (the ABI version).
func extractABIVer(data []byte) (byte, bool) {
	body, ok := extractCustomSection(data, "abi_ver")
	if !ok || len(body) < 1 {
		return 0, false
	}
	return body[0], true
}

// extractSig returns the "sig" custom section payload (64-byte ed25519 sig).
func extractSig(data []byte) ([]byte, bool) {
	return extractCustomSection(data, "sig")
}

// stripSigSection returns a copy of data with the "sig" custom section
// removed. The signed message is [abi_ver_byte || stripped_wasm].
func stripSigSection(data []byte) []byte {
	payload, found := extractSig(data)
	if !found {
		return data
	}
	nameLenBytes := leb(len("sig"))
	sectionBody := append([]byte{}, nameLenBytes...)
	sectionBody = append(sectionBody, "sig"...)
	sectionBody = append(sectionBody, payload...)
	sectionHeader := append([]byte{0}, leb(len(sectionBody))...)
	sectionHeader = append(sectionHeader, sectionBody...)
	idx := findBytes(data, sectionHeader)
	if idx < 0 {
		return data
	}
	out := make([]byte, 0, len(data)-len(sectionHeader))
	out = append(out, data[:idx]...)
	out = append(out, data[idx+len(sectionHeader):]...)
	return out
}

// Verify checks the module's abi_ver + ed25519 signature against all
// trusted keys. Returns nil on first matching key.
func (m *Manager) Verify(wasm []byte) error {
	if len(m.trusted) == 0 {
		return fmt.Errorf("no trusted keys loaded")
	}
	abiVer, ok := extractABIVer(wasm)
	if !ok {
		return fmt.Errorf("no abi_ver section found")
	}
	sig, ok := extractSig(wasm)
	if !ok {
		return fmt.Errorf("no sig section found")
	}
	stripped := stripSigSection(wasm)
	msg := append([]byte{abiVer}, stripped...)
	for _, k := range m.trusted {
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return fmt.Errorf("signature did not match any trusted key")
}

// ABIVer returns the module's embedded abi_ver byte (0 if missing).
func ABIVer(wasm []byte) byte {
	v, _ := extractABIVer(wasm)
	return v
}

// ModuleName returns the module name extracted from the "name" custom
// section, or "" if missing.
func ModuleName(wasm []byte) string {
	body, ok := extractCustomSection(wasm, "name")
	if !ok {
		return ""
	}
	return strings.TrimRight(string(body), "\x00")
}

// Install verifies a module and writes it to modulesDir/<name>.wasm.
// Returns the installed name on success.
func (m *Manager) Install(wasm []byte) (string, error) {
	if err := m.Verify(wasm); err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	name := ModuleName(wasm)
	if name == "" {
		return "", fmt.Errorf("module has no name section")
	}
	dst := path.Join(m.modulesDir, name+".wasm")
	// Overwrite: create (truncates) then write.
	if err := m.fs.Create(dst); err != nil {
		return "", fmt.Errorf("create %s: %w", dst, err)
	}
	written := 0
	for written < len(wasm) {
		n, err := m.fs.WriteFile(dst, uint64(written), wasm[written:])
		if err != nil {
			return "", fmt.Errorf("write %s: %w", dst, err)
		}
		if n == 0 {
			return "", fmt.Errorf("write %s: zero progress", dst)
		}
		written += n
	}
	return name, nil
}

// Remove deletes modulesDir/<name>.wasm.
func (m *Manager) Remove(name string) error {
	return m.fs.Delete(path.Join(m.modulesDir, name+".wasm"))
}

// List returns the .wasm modules in modulesDir.
func (m *Manager) List() ([]string, error) {
	ents, err := m.fs.List(m.modulesDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name, ".wasm") {
			out = append(out, strings.TrimSuffix(e.Name, ".wasm"))
		}
	}
	return out, nil
}

// --- wasm custom section parser (minimal: id=0 only) ---

func readLEB128Full(data []byte) (int, int) {
	result := 0
	shift := 0
	pos := 0
	for pos < len(data) {
		b := data[pos]
		pos++
		result |= int(b&0x7F) << shift
		if b < 0x80 {
			return result, pos
		}
		shift += 7
	}
	return 0, 0
}

func readLEB128(data []byte, pos *int) (int, bool) {
	result := 0
	shift := 0
	for *pos < len(data) {
		b := data[*pos]
		*pos++
		result |= int(b&0x7F) << shift
		if b < 0x80 {
			return result, true
		}
		shift += 7
	}
	return 0, false
}

func leb(n int) []byte {
	var out []byte
	for {
		b7 := n & 0x7F
		n >>= 7
		if n > 0 {
			out = append(out, byte(b7|0x80))
		} else {
			out = append(out, byte(b7))
			return out
		}
	}
}

func extractCustomSection(data []byte, name string) ([]byte, bool) {
	if len(data) < 8 || string(data[:4]) != "\x00asm" {
		return nil, false
	}
	i := 8
	for i < len(data) {
		id := data[i]
		i++
		size, ok := readLEB128(data, &i)
		if !ok {
			return nil, false
		}
		if i+size > len(data) {
			return nil, false
		}
		if id == 0 {
			sec := data[i : i+size]
			nameLen, j := readLEB128Full(sec)
			if j+nameLen <= len(sec) && string(sec[j:j+nameLen]) == name {
				return sec[j+nameLen:], true
			}
		}
		i += size
	}
	return nil, false
}

func findBytes(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
