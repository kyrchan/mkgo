// Command module-sign signs wasm modules for kernel package management.
// It embeds an ed25519 signature of the module's abi_ver byte + wasm bytes
// into a custom "sig" section. Usage:
//
//	module-sign sign <privatekey.hex> <in.wasm> <out.wasm> <abi_ver>
//	module-sign verify <publickey.hex> <in.wasm>
//	module-sign genkey
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ed25519"
)

func leb(n int) []byte {
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

func appendCustomSection(buf []byte, name string, payload []byte) []byte {
	section := append(leb(len(name)), []byte(name)...)
	section = append(section, payload...)
	buf = append(buf, 0)
	buf = append(buf, leb(len(section))...)
	buf = append(buf, section...)
	return buf
}

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
	return result, pos
}

func extractCustomSection(data []byte, name string) ([]byte, bool) {
	i := 8
	for i < len(data) {
		id := data[i]
		i++
		size, ok := readLEB128(data, &i)
		if !ok || size < 0 {
			break
		}
		if i+size > len(data) {
			break
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

func extractABIVer(data []byte) (byte, bool) {
	payload, ok := extractCustomSection(data, "abi_ver")
	if !ok || len(payload) < 1 {
		return 0, false
	}
	return payload[0], true
}

func sign(wasmData []byte, privKey ed25519.PrivateKey, abiVer byte, moduleName string) ([]byte, error) {
	if moduleName != "" {
		if _, ok := extractCustomSection(wasmData, "module_name"); !ok {
			wasmData = appendCustomSection(wasmData, "module_name", []byte(moduleName))
		}
	}
	msg := append([]byte{abiVer}, wasmData...)
	sig := ed25519.Sign(privKey, msg)
	return appendCustomSection(wasmData, "sig", sig), nil
}

func verify(data []byte, pubKey ed25519.PublicKey) error {
	abiVer, ok := extractABIVer(data)
	if !ok {
		return fmt.Errorf("no abi_ver section found")
	}
	sig, ok := extractCustomSection(data, "sig")
	if !ok {
		return fmt.Errorf("no signature found")
	}
	// The signed message: [abi_ver_byte] + [wasm_data_without_sig_section]
	stripped := stripSigSection(data)
	msg := append([]byte{abiVer}, stripped...)
	if !ed25519.Verify(pubKey, msg, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

func stripSigSection(data []byte) []byte {
	payload, found := extractCustomSection(data, "sig")
	if !found {
		return data
	}
	// Reconstruct: find the section start and remove it
	nameLenBytes := leb(len("sig"))
	sectionBody := append(nameLenBytes, []byte("sig")...)
	sectionBody = append(sectionBody, payload...)
	sectionHeader := append([]byte{0}, leb(len(sectionBody))...)
	sectionHeader = append(sectionHeader, sectionBody...)

	// Find and remove the section
	idx := findBytes(data, sectionHeader)
	if idx < 0 {
		return data
	}
	out := make([]byte, 0, len(data)-len(sectionHeader))
	out = append(out, data[:idx]...)
	out = append(out, data[idx+len(sectionHeader):]...)
	return out
}

// tamperSig flips the first byte of the "sig" custom-section payload, leaving
// abi_ver and everything else untouched. The module remains bit-identical
// except for one signature byte — exactly the "tampered-signature" rejection
// path the gate needs to separate from the abi_ver mismatch (-mismatch).
func tamperSig(data []byte) ([]byte, error) {
	payload, found := extractCustomSection(data, "sig")
	if !found || len(payload) == 0 {
		return nil, fmt.Errorf("no sig section to tamper")
	}
	// Strip the original sig section, flip one byte, re-append.
	tampered := append([]byte{}, payload...)
	tampered[0] ^= 0x01
	stripped := stripSigSection(data)
	return appendCustomSection(stripped, "sig", tampered), nil
}

func findBytes(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i] == needle[0] && string(haystack[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// replaceABIVer returns wasm with every abi_ver custom section removed and a
// single fresh abi_ver section carrying ver appended. It reports whether an
// abi_ver section was found at all.
//
// ALL abi_ver sections are removed, not just the first: a module carrying two
// abi_ver sections has no single version, and the guest reader takes the
// first one it finds, so re-stamping must replace rather than append.
func replaceABIVer(wasm []byte, ver int) ([]byte, bool) {
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		return wasm, false
	}
	if ver < 1 || ver > 255 {
		// byte(258) would wrap to 2 and stamp the kernel-accepted version by
		// accident; the abi_ver payload is a single byte (ABI section 7).
		fmt.Fprintf(os.Stderr, "module-sign: version %d out of range (1..255)\n", ver)
		os.Exit(1)
	}
	out := append([]byte{}, wasm[:8]...)
	rest := wasm[8:]
	found := false
	for len(rest) > 0 {
		secStart := rest
		id := rest[0]
		rest = rest[1:]
		pos := 0
		v, ok := readLEB128(rest, &pos)
		if !ok || uint64(len(rest)-pos) < uint64(v) {
			break // malformed: leave the tail intact
		}
		body := rest[pos : pos+v]
		secLen := 1 + pos + v // id byte + LEB size + body, measured from secStart
		rest = rest[pos+v:]   // `rest` already consumed the id byte above
		isAbiVer := false
		if id == 0 {
			npos := 0
			nameLen, ok2 := readLEB128(body, &npos)
			if ok2 && uint64(len(body)-npos) >= uint64(nameLen) &&
				string(body[npos:npos+nameLen]) == "abi_ver" {
				isAbiVer = true
				found = true
			}
		}
		if !isAbiVer {
			out = append(out, secStart[:secLen]...)
		}
	}
	if !found {
		return wasm, false
	}
	out = appendCustomSection(out, "abi_ver", []byte{byte(ver)})
	return out, true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: module-sign <sign|verify|genkey|tamper> ...")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "genkey":
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "genkey: %v\n", err)
			os.Exit(1)
		}
		result := map[string]string{
			"public":  hex.EncodeToString(pub),
			"private": hex.EncodeToString(priv),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	case "sign":
		if len(os.Args) < 6 {
			fmt.Fprintln(os.Stderr, "usage: module-sign sign <privatekey.hex> <in.wasm> <out.wasm> <abi_ver>")
			os.Exit(1)
		}
		privHex := os.Args[2]
		inFile := os.Args[3]
		outFile := os.Args[4]
		abiVer := byte(1)
		fmt.Sscanf(os.Args[5], "%d", &abiVer)
		privBytes, err := hex.DecodeString(privHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode private key: %v\n", err)
			os.Exit(1)
		}
		priv := ed25519.PrivateKey(privBytes)
		wasmData, err := os.ReadFile(inFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read wasm: %v\n", err)
			os.Exit(1)
		}
		moduleName := strings.TrimSuffix(filepath.Base(inFile), filepath.Ext(inFile))
		signed, err := sign(wasmData, priv, abiVer, moduleName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sign: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outFile, signed, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("signed: %s (abi_ver=%d, module_name=%s)\n", outFile, abiVer, moduleName)
	case "verify":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: module-sign verify <publickey.hex> <in.wasm>")
			os.Exit(1)
		}
		pubHex := os.Args[2]
		wasmFile := os.Args[3]
		pubBytes, err := hex.DecodeString(pubHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode public key: %v\n", err)
			os.Exit(1)
		}
		pub := ed25519.PublicKey(pubBytes)
		data, err := os.ReadFile(wasmFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		if err := verify(data, pub); err != nil {
			fmt.Fprintf(os.Stderr, "verify: %v\n", err)
			os.Exit(1)
		}
		abiVer, _ := extractABIVer(data)
		fmt.Printf("verified: abi_ver=%d\n", abiVer)
	case "-mismatch":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: module-sign -mismatch <in.wasm> <out.wasm>")
			os.Exit(1)
		}
		inFile := os.Args[2]
		outFile := os.Args[3]
		wasmData, err := os.ReadFile(inFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read wasm: %v\n", err)
			os.Exit(1)
		}
		stripped := stripSigSection(wasmData)
		modified, ok := replaceABIVer(stripped, 1)
		if !ok {
			fmt.Fprintln(os.Stderr, "mismatch: no abi_ver section in input")
			os.Exit(1)
		}
		_, newPriv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mismatch: genkey: %v\n", err)
			os.Exit(1)
		}
		existingName, _ := extractCustomSection(modified, "module_name")
		signed, err := sign(modified, newPriv, 1, string(existingName))
		if err != nil {
			fmt.Fprintf(os.Stderr, "mismatch: sign: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outFile, signed, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "mismatch: write: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("mismatch: %s (abi_ver=1, new key)\n", outFile)
	case "-tamper":
		// Tamper: keep abi_ver and the trusted key intact, but flip a byte
		// in the signature payload. This isolates the signature-rejection
		// path from the abi_ver-rejection path (which -mismatch covers by
		// also using a new key).
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: module-sign -tamper <in.wasm> <out.wasm>")
			os.Exit(1)
		}
		inFile := os.Args[2]
		outFile := os.Args[3]
		wasmData, err := os.ReadFile(inFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read wasm: %v\n", err)
			os.Exit(1)
		}
		tampered, err := tamperSig(wasmData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tamper: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outFile, tampered, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "tamper: write: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("tamper: %s (sig byte flipped, abi_ver retained)\n", outFile)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
