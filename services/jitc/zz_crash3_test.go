package jitc

import (
	"testing"
)

func TestZZCrash3(t *testing.T) {
	// Minimal import-call smoke: main pushes fd_write args, calls the
	// import, drops errno, leaves const 7 as the () -> i32 result.
	// Type section must include the fd_write signature at the index the
	// import references (type 2), matching FibModule — a single-type
	// section made CompileModuleWithRelocs fail with "bad import type 2"
	// and the old ignored error then nil-deref'd at ir.Image.
	var ops []byte
	ops = append(ops, constI32(1)...)
	ops = append(ops, constI32(64)...)
	ops = append(ops, constI32(1)...)
	ops = append(ops, constI32(80)...)
	ops = append(ops, 0x10, 0x00, 0x1A) // call fd_write; drop
	ops = append(ops, constI32(7)...)
	ops = append(ops, 0x0B)
	mod := []byte("wasi_snapshot_preview1")
	nm := []byte("fd_write")
	p := ulebEnc(1)
	p = append(p, ulebEnc(uint32(len(mod)))...)
	p = append(p, mod...)
	p = append(p, ulebEnc(uint32(len(nm)))...)
	p = append(p, nm...)
	impSec := sec(2, append(p, 0x00, 0x02))
	raw := module(
		typesSec(
			ftype(nil, []byte{0x7F}), // 0: main () -> i32
			ftype(nil, nil),          // 1: filler so import can use type 2
			ftype([]byte{0x7F, 0x7F, 0x7F, 0x7F}, []byte{0x7F}), // 2: fd_write
		),
		impSec,
		funcsSec(0),
		memSec(1),
		exportSec("main", 0, 1),
		codeSec(body(nil, ops...)),
		func() []byte {
			msg := []byte("NATIVE-OK\n")
			p := ulebEnc(1)
			p = append(p, 0x00, 0x41)
			p = append(p, slebEnc(100)...)
			p = append(p, 0x0B)
			p = append(p, ulebEnc(uint32(len(msg)))...)
			p = append(p, msg...)
			return sec(11, p)
		}(),
	)
	m, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	ir, err := CompileModuleWithRelocs(m)
	if err != nil {
		t.Fatalf("CompileModuleWithRelocs: %v", err)
	}
	stub := []byte{0x31, 0xC0, 0xC3} // xor eax,eax; ret
	img := ir.Image
	for _, r := range ir.ImportRelocs {
		off := len(img.Region)
		img.Region = append(img.Region, stub...)
		rel := int32(off - (int(r.CodeOff) + 4))
		img.Region[r.CodeOff] = byte(rel)
		img.Region[r.CodeOff+1] = byte(rel >> 8)
		img.Region[r.CodeOff+2] = byte(rel >> 16)
		img.Region[r.CodeOff+3] = byte(rel >> 24)
	}
	r, err := NewRunner(m, img, 1)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer r.Close()
	res, err := r.Call(m, "main")
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	expectValue(t, res, 7)
}
