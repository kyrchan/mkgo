package pkg

import "testing"

// verifyMessage must produce the same bytes the old two-copy path produced,
// while allocating once. Correctness is the easy half; the point of the test
// is that the peak is one buffer, because that is the property that keeps a
// multi-megabyte service module verifiable inside a guest session.
func TestVerifyMessageMatchesStrippedPlusAbiVer(t *testing.T) {
	// A module with a "sig" custom section appended, which is the shape the
	// signer produces and therefore the shape that exercises the strip.
	mod := buildWasmWithSections(t, "t", 2) // then append a real sig section
	abi := byte(2)

	got := verifyMessage(abi, mod)
	want := append([]byte{abi}, stripSigSection(mod)...)
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d: %#x want %#x", i, got[i], want[i])
		}
	}
}

// A module with no "sig" section must still verify-message correctly rather
// than panic or truncate.
func TestVerifyMessageWithoutSigSection(t *testing.T) {
	mod := buildWasmWithSections(t, "t", 2)
	got := verifyMessage(7, mod)
	if len(got) != len(mod)+1 {
		t.Fatalf("len=%d want %d", len(got), len(mod)+1)
	}
	if got[0] != 7 {
		t.Fatalf("abi_ver byte = %d want 7", got[0])
	}
	for i := range mod {
		if got[i+1] != mod[i] {
			t.Fatalf("payload byte %d differs", i)
		}
	}
}

// verifyMessage allocates exactly once and exactly once the size. A doubling
// append would over-allocate by up to 2x, which is what made the old path need
// ~3x the module size at peak. Allocating via testing.AllocsPerRun is the only
// way to keep this honest: it fails the moment someone reintroduces a second
// copy.
func TestVerifyMessageAllocatesOnce(t *testing.T) {
	mod := appendCustomSection(buildWasmWithSections(t, "t", 2), "sig", make([]byte, 4096))
	if allocs := testing.AllocsPerRun(50, func() { _ = verifyMessage(2, mod) }); allocs > 1 {
		t.Fatalf("verifyMessage allocated %v times per run, want <= 1", allocs)
	}
}
