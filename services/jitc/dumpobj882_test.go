package jitc

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestDumpObj882(t *testing.T) {
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

	// Build a flat binary with a simple _start that calls our function
	// Write as raw binary for objdump
	tmpfile := "/tmp/k882.bin"
	f, err := os.Create(tmpfile)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(a.Code)
	f.Close()

	// Use objdump to disassemble
	cmd := exec.Command("objdump", "-D", "-b", "binary", "-m", "i386:x86-64", "--start-address=0", tmpfile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("objdump error: %v\n", err)
		fmt.Printf("output: %s\n", out)
	} else {
		fmt.Println(string(out))
	}
}
