package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"testing"
)

// The local SHA-256 exists so the shipped module does not link
// crypto/sha256 (32 MiB of declared linear memory via the fips140 DRBG).
// These tests import the stdlib deliberately: the host test may pay for
// it, the guest must not.

func TestSHA256NISTVectors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
			"248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"},
		{string(bytes.Repeat([]byte("a"), 1000000)),
			"cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"},
	}
	for _, c := range cases {
		got := hex.EncodeToString(hashOf([]byte(c.in)))
		if got != c.want {
			t.Fatalf("sha256(%d bytes) = %s, want %s", len(c.in), got, c.want)
		}
	}
}

func hashOf(b []byte) []byte {
	sum := sha256Sum256(b)
	return sum[:]
}

// Every length that crosses a padding boundary, plus a long sweep: one
// bug in the tail handling shows up as a mismatch at a specific length,
// not as a general failure.
func TestSHA256MatchesStdlibEveryLength(t *testing.T) {
	for n := 0; n <= 200; n++ {
		msg := make([]byte, n)
		for i := range msg {
			msg[i] = byte(i*7 + n)
		}
		want := sha256.Sum256(msg)
		got := sha256Sum256(msg)
		if got != want {
			t.Fatalf("length %d: got %x, want %x", n, got, want)
		}
	}
}

func TestSHA256MatchesStdlibRandom(t *testing.T) {
	r := rand.New(rand.NewSource(20260928))
	for i := 0; i < 500; i++ {
		msg := make([]byte, r.Intn(9000))
		r.Read(msg)
		want := sha256.Sum256(msg)
		got := sha256Sum256(msg)
		if got != want {
			t.Fatalf("random case %d (len %d): got %x, want %x", i, len(msg), got, want)
		}
	}
}

// Multi-megabyte input: many blocks, and the length field no longer fits
// in the low bits of a word.
func TestSHA256MatchesStdlibLarge(t *testing.T) {
	msg := bytes.Repeat([]byte("kernel.lane "), 200000) // 2.4 MiB
	if got, want := sha256Sum256(msg), sha256.Sum256(msg); got != want {
		t.Fatalf("2.4 MiB: got %x, want %x", got, want)
	}
}
