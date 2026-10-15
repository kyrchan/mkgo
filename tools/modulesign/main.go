// modulesign -- stamp or re-stamp a module's abi_ver custom section, and
// report what a module already carries.
//
// The READER lives in the guest (`module verify`, ABI section 1) so a user
// can ask inside the OS why a module would be refused. The WRITER belongs on
// the host, next to scripts/add_abiver.py, because stamping happens at build
// time and never at runtime -- a module that could rewrite its own abi_ver
// could defeat the check by writing whatever it likes.
//
//	modulesign -report in.wasm                  print the current stamp
//	modulesign -stamp in.wasm out.wasm [ver]    write out.wasm with ver
//	modulesign -mismatch in.wasm out.wasm       write a DELIBERATELY wrong
//	                                            stamp, for the negative gate
package main

import (
	"flag"
	"fmt"
	"os"
)

// leb appends an unsigned LEB128 value, matching the wasm binary format and
// the writer in scripts/add_abiver.py byte for byte.
func leb(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

// customSection returns the bytes of one custom section carrying name=payload.
func customSection(name string, payload []byte) []byte {
	body := append(leb(uint64(len(name))), name...)
	body = append(body, payload...)
	out := []byte{0x00} // custom section id
	out = append(out, leb(uint64(len(body)))...)
	return append(out, body...)
}

// findAbiVer scans for existing abi_ver sections and returns the version
// byte from the first one found, plus a copy of the module with ALL abi_ver
// sections removed.
//
// Re-stamping must REPLACE, not append: a module with two abi_ver sections
// has no single version, and the guest reader takes the first one it finds.
// Removing ALL abi_ver sections (not just the first) ensures that modules
// corrupted with duplicate stamps by add_abiver.py (which blindly appends)
// are sanitized on every re-stamp.
//
// Non-abi_ver sections are preserved in their original order.
// Malformed sections cause a break (same as the original); if no abi_ver
// was found, the original wasm is returned unmodified.
func findAbiVer(wasm []byte) (int, []byte, bool) {
	if len(wasm) < 8 || string(wasm[0:4]) != "\x00asm" {
		return 0, wasm, false
	}
	out := append([]byte{}, wasm[:8]...)
	rest := wasm[8:]
	var firstVer int
	found := false
	for len(rest) > 0 {
		secStart := rest
		id := rest[0]
		rest = rest[1:]
		v, n, ok := readLEB(rest)
		if !ok || uint64(len(rest)-n) < v {
			break // malformed: leave the tail
		}
		bodyLen := int(v)
		body := rest[n : n+bodyLen]
		rest = rest[n+bodyLen:]
		isAbiVer := false
		if id == 0 {
			nameLen, n2, ok := readLEB(body)
			if ok && uint64(len(body)-n2) >= nameLen &&
				string(body[n2:n2+int(nameLen)]) == "abi_ver" {
				isAbiVer = true
				if !found {
					found = true
					payload := body[n2+int(nameLen):]
					if len(payload) > 0 {
						firstVer = int(payload[0])
					}
				}
			}
		}
		if !isAbiVer {
			out = append(out, secStart[:1+n+bodyLen]...)
		}
	}
	if !found {
		return 0, wasm, false
	}
	return firstVer, out, true
}

func readLEB(b []byte) (uint64, int, bool) {
	var v uint64
	var shift uint
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		v |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			return v, i + 1, true
		}
		shift += 7
	}
	return 0, 0, false
}

func main() {
	report := flag.Bool("report", false, "print the module's current abi_ver")
	mismatch := flag.Bool("mismatch", false, "stamp a deliberately wrong version (negative tests)")
	stamp := flag.Int("stamp", 0, "stamp this version onto the output")
	flag.Parse()
	want := 2
	if *report {
		want = 1 // -report only reads
	}
	if flag.NArg() != want {
		fmt.Fprintln(os.Stderr, "usage: modulesign -report in.wasm | -stamp N in.wasm out.wasm | -mismatch in.wasm out.wasm")
		os.Exit(2)
	}
	in, out := flag.Arg(0), ""
	if want == 2 {
		out = flag.Arg(1)
	}
	wasm, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modulesign:", err)
		os.Exit(1)
	}
	if *report {
		ver, _, ok := findAbiVer(wasm)
		if !ok {
			fmt.Printf("%s: no abi_ver section\n", in)
			return
		}
		fmt.Printf("%s: abi_ver=%d\n", in, ver)
		return
	}
	ver := *stamp
	if ver == 0 {
		ver = 2 // the stamp the kernel accepts
	}
	if *mismatch {
		ver = 99
	}
	_, stripped, _ := findAbiVer(wasm)
	// Guard against silent byte truncation: byte(258) would wrap to 2,
	// inadvertently stamping the kernel-accepted version. The abi_ver
	// payload is a single byte (ABI §7), so versions must be 1..255.
	if ver < 1 || ver > 255 {
		fmt.Fprintf(os.Stderr, "modulesign: version %d out of range (1..255)\n", ver)
		os.Exit(1)
	}
	// Re-stamp, replacing any existing section.
	stripped = append(stripped, customSection("abi_ver", []byte{byte(ver)})...)
	if err := os.WriteFile(out, stripped, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "modulesign:", err)
		os.Exit(1)
	}
	fmt.Printf("modulesign: %s -> %s abi_ver=%d\n", in, out, ver)
}
