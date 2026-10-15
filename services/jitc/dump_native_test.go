package jitc

import (
	"fmt"
	"os"
	"testing"
)

func TestDumpNative882(t *testing.T) {
	raw, err := os.ReadFile("../net/net.wasm.raw")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}

	fidx := uint32(882)
	a, err := CompileOne(m, fidx)
	if err != nil {
		t.Skipf("compile: %v", err)
	}

	fmt.Printf("Region length: %d bytes\n", len(a.Code))
	fmt.Printf("Polls: %v\n", a.Polls)
	fmt.Printf("Calls: %v\n", a.Calls)

	// Dump as hex
	for i, b := range a.Code {
		if i%16 == 0 {
			fmt.Printf("\n%04x: ", i)
		}
		fmt.Printf("%02x ", b)
	}
	fmt.Println()
}
