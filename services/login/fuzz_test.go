package main

import (
	"testing"
)

// FuzzLoginAuthPayload fuzzes the AUTH payload decoder (AGENTS.md practice
// #4 — "LOGIN/AUTH payloads"): the {u8 nameLen, name, u8 passLen, pass}
// parser must never panic on arbitrary bytes and must stay within bounds.
// We exercise the lbyte decoder exactly as kernAuth does.
func FuzzLoginAuthPayload(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0})                     // empty name, empty pass
	f.Add([]byte{4, 'u', 's', 'r', 's', 0}) // name="usrs", empty pass
	f.Add([]byte{0, 3, 'p', 'w', 'd'})     // empty name, pass="pwd"
	f.Add([]byte{3, 'a', 'd', 'm', 2, 'x', 'y'})
	f.Add([]byte{255}) // truncated length byte

	f.Fuzz(func(t *testing.T, data []byte) {
		name, n2, ok1 := lbyte(data)
		if !ok1 {
			return // truncated name: legitimate reject
		}
		if n2 > len(data) {
			t.Fatalf("name consumed past end: n2=%d len=%d", n2, len(data))
		}
		if len(name) > 255 {
			t.Fatalf("name too long: %d", len(name))
		}
		pass, n3, ok2 := lbyte(data[n2:])
		if !ok2 {
			return // truncated pass: legitimate reject
		}
		if n2+n3 > len(data) {
			t.Fatalf("pass consumed past end: n2=%d n3=%d len=%d", n2, n3, len(data))
		}
		_ = pass
	})
}
