// addabiver appends an `abi_ver` custom section to a wasm module:
//
//	go run ./tools/addabiver in.wasm out.wasm [version]
//
// Section encoding (wasm binary format): id 0x00 (custom), then
// size-prefixed payload = length-prefixed name "abi_ver" followed by a
// little-endian u32 version. Appending after the last section is legal
// per the spec; the kernel's instantiation-time ABI check scans all
// custom sections (AGENTS.md "Module updates").
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: addabiver in.wasm out.wasm [version]")
		os.Exit(2)
	}
	ver := uint32(1)
	if len(os.Args) >= 4 {
		v, err := parseU32(os.Args[3])
		if err != nil {
			fatal("bad version:", err)
		}
		ver = v
	}

	in, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal(err)
	}
	if len(in) < 8 || in[0] != 0x00 || string(in[1:4]) != "asm" { // \0asm magic
		fatal("not a wasm module")
	}

	out := append(in, customSection("abi_ver", ver)...)
	if err := os.WriteFile(os.Args[2], out, 0o644); err != nil {
		fatal(err)
	}
}

func customSection(name string, ver uint32) []byte {
	var payload []byte
	payload = append(payload, byte(len(name)))
	payload = append(payload, name...)
	var v [4]byte
	binary.LittleEndian.PutUint32(v[:], ver)
	payload = append(payload, v[:]...)

	sec := []byte{0x00} // custom section id
	var size [5]byte
	n := binary.PutUvarint(size[:], uint64(len(payload)))
	sec = append(sec, size[:n]...)
	return append(sec, payload...)
}

func parseU32(s string) (uint32, error) {
	var v uint64
	var err error
	if len(s) > 2 && s[:2] == "0x" {
		_, err = fmt.Sscanf(s, "%x", &v)
	} else {
		_, err = fmt.Sscanf(s, "%d", &v)
	}
	if err != nil || v > 0xFFFFFFFF {
		return 0, fmt.Errorf("invalid u32 %q", s)
	}
	return uint32(v), nil
}

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
	os.Exit(1)
}
