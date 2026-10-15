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
	fs         FS
	trusted    []ed25519.PublicKey
	modulesDir string
}

// New builds a Manager. Trusted keys are loaded lazily on first Verify
// to avoid blocking during construction. modulesDir is typically
// "/boot/modules".
func New(f FS, modulesDir string) *Manager {
	return &Manager{fs: f, modulesDir: modulesDir}
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
func (m *Manager) TrustedKeyCount() int {
	if len(m.trusted) == 0 {
		m.trusted = m.readTrusted()
	}
	return len(m.trusted)
}

// ensureKeys loads trusted keys if not already done.
func (m *Manager) ensureKeys() {
	if len(m.trusted) > 0 {
		return
	}
	m.trusted = m.readTrusted()
}

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
// verifyMessage builds the exact bytes ed25519 must check: the abi_ver byte
// followed by the module with its "sig" custom section removed.
//
// It is one allocation of exactly the right size, and it does the strip in
// place rather than building an intermediate stripped copy. The previous
// form made two full copies -- stripSigSection's result, then an append onto
// a 1-byte slice that grew by doubling -- so verifying an N-byte module needed
// roughly 3N bytes of contiguous heap at the peak. A 2.7 MB service module
// therefore tried to allocate ~8 MB inside a guest session whose Go heap had
// only ~17 MB in use, and the shell died with "out of memory" mid-verify.
// Memory here is a function of module size exactly once, not three times.
func verifyMessage(abiVer byte, wasm []byte) []byte {
	offset, secSize, found := findSectionSize(wasm, "sig")
	if !found {
		out := make([]byte, 0, len(wasm)+1)
		out = append(out, abiVer)
		return append(out, wasm...)
	}
	out := make([]byte, 0, len(wasm)+1-secSize)
	out = append(out, abiVer)
	out = append(out, wasm[:offset]...)
	return append(out, wasm[offset+secSize:]...)
}

func stripSigSection(data []byte) []byte {
	offset, secSize, found := findSectionSize(data, "sig")
	if !found {
		return data
	}
	out := make([]byte, 0, len(data)-secSize)
	out = append(out, data[:offset]...)
	out = append(out, data[offset+secSize:]...)
	return out
}

// findSectionSize locates a custom section by name in a wasm binary.
// Returns the byte offset of the section's id byte, the total section
// size (id + size-LEB + nameLen-LEB + name + body), and whether found.
func findSectionSize(data []byte, name string) (int, int, bool) {
	if len(data) < 8 || string(data[:4]) != "\x00asm" {
		return 0, 0, false
	}
	i := 8
	for i < len(data) {
		id := data[i]
		sectionStart := i
		i++
		size, ok := readLEB128(data, &i)
		if !ok || size < 0 {
			return 0, 0, false
		}
		if i+size > len(data) {
			return 0, 0, false
		}
		if id == 0 {
			sec := data[i : i+size]
			nameLen, j := readLEB128Full(sec)
			if j+nameLen <= len(sec) && string(sec[j:j+nameLen]) == name {
				totalSize := (i - sectionStart) + size
				return sectionStart, totalSize, true
			}
		}
		i += size
	}
	return 0, 0, false
}

// Verify checks the module's abi_ver + ed25519 signature against all
// trusted keys. Returns nil on first matching key.
func (m *Manager) Verify(wasm []byte) error {
	m.ensureKeys()
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
	msg := verifyMessage(abiVer, wasm)
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

// ModuleName returns the module's name. It first looks for a "module_name"
// custom section (added by module-sign, capturing the filename-derived name
// as an integrity-protected string). This is needed because Go-compiled wasm
// lacks a standard module-name subsection in its "name" section. Falls back
// to the "name" custom section for unsigned/test modules.
func ModuleName(wasm []byte) string {
	body, ok := extractCustomSection(wasm, "module_name")
	if ok && len(body) > 0 {
		return string(body)
	}
	body, ok = extractCustomSection(wasm, "name")
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

// Update re-verifies a module and overwrites its installed copy. Unlike
// Install, Update requires the module to already exist — it is the
// replacement path for a newer build of a module that is already installed.
// The module is verified (signature + abi_ver) before the overwrite, so a
// tampered update never replaces a good module with a bad one.
func (m *Manager) Update(wasm []byte) (string, error) {
	if err := m.Verify(wasm); err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	name := ModuleName(wasm)
	if name == "" {
		return "", fmt.Errorf("module has no name section")
	}
	dst := path.Join(m.modulesDir, name+".wasm")
	// Require the module to already be installed — Update is a replacement,
	// not a fresh install.
	if _, err := m.fs.Stat(dst); err != nil {
		return "", fmt.Errorf("update: %s is not installed", name)
	}
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

// readLEB128Full decodes an unsigned LEB128 from the start of data.
// It returns (value, bytesConsumed), or (0, 0) on truncation or overflow.
// Capped at 5 bytes — a u32 LEB128 never needs more, and longer streams
// are malformed (or an attack) rather than valid wasm. This prevents the
// shift from wrapping an int into a negative that bypasses the caller's
// bounds check (see FuzzExtractCustomSection regression).
func readLEB128Full(data []byte) (int, int) {
	result := 0
	shift := 0
	pos := 0
	for pos < len(data) && pos < 5 {
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

// readLEB128 decodes an unsigned LEB128 from data starting at *pos,
// advancing *pos past the value. Returns (value, ok). Capped at 5 bytes
// for u32 safety — see readLEB128Full.
func readLEB128(data []byte, pos *int) (int, bool) {
	result := 0
	shift := 0
	start := *pos
	for *pos < len(data) && *pos-start < 5 {
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

// Leb encodes an unsigned integer as LEB128.
func Leb(n int) []byte {
	var out []byte
	for {
		b7 := n & 0x7F
		n >>= 7
		if n > 0 {
			out = append(out, byte(b7|0x80))
		} else {
			out = append(out, byte(b7))
			break
		}
	}
	return out
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
		if !ok || size < 0 {
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
