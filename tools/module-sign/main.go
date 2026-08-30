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
		if !ok {
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

func extractABIVer(data []byte) (byte, bool) {
	payload, ok := extractCustomSection(data, "abi_ver")
	if !ok || len(payload) < 1 {
		return 0, false
	}
	return payload[0], true
}

func sign(wasmData []byte, privKey ed25519.PrivateKey, abiVer byte) ([]byte, error) {
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: module-sign <sign|verify|genkey> ...")
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
		signed, err := sign(wasmData, priv, abiVer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sign: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(outFile, signed, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("signed: %s (abi_ver=%d)\n", outFile, abiVer)
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
