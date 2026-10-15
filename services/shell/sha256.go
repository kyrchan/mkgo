package main

// Local SHA-256, and why it exists.
//
// The `passwd` built-in hashes a salted password, and the obvious
// implementation is crypto/sha256. Linking that one package costs this
// guest 32 MiB of *declared* linear memory: Go 1.24+ routes crypto
// through crypto/internal/fips140, whose drbg package declares
//
//	var memory entropy.ScratchBuffer   // 32 MiB, BSS
//
// and the wasm linker sets a module's initial memory to runtime.end +
// 1 MiB, where runtime.end is the end of data+BSS. So shell.wasm
// declared 556 pages (34.8 MiB) while every other service declared
// ~2.1 MiB, and the kernel has to back the whole declared range --
// 32 MiB of the boot fleet's pool, ~20% of what the fleet actually
// uses, for a hash of a password.
//
// SHA-256 is a fixed, specified compression function, so a local
// implementation is a mechanical transcription rather than a design
// decision, and sha256_test.go checks it against crypto/sha256 on the
// host (NIST vectors plus a differential sweep over every length that
// matters around the padding boundaries, plus multi-megabyte inputs).
// The host test imports the stdlib; the shipped module does not.

import "encoding/binary"

var sha256K = [64]uint32{
	0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
	0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
	0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
	0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
	0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
	0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
	0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
	0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
}

func sha256Rotr(x uint32, n uint) uint32 { return x>>n | x<<(32-n) }

// sha256Sum256 returns the SHA-256 of data, matching crypto/sha256's
// Sum256 byte for byte.
func sha256Sum256(data []byte) [32]byte {
	var h [8]uint32 = [8]uint32{
		0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
		0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
	}

	var w [64]uint32

	// Whole blocks straight out of the input.
	total := len(data)
	off := 0
	for ; off+64 <= total; off += 64 {
		sha256Block(&h, data[off:off+64], &w)
	}

	// The tail: 0x80, zero padding, then the message length in bits as a
	// big-endian uint64 in the last 8 bytes. That is either the rest of
	// the current block or a second block.
	var tail [128]byte
	rest := copy(tail[:], data[off:])
	tail[rest] = 0x80
	if rest+1+8 <= 64 {
		binary.BigEndian.PutUint64(tail[56:], uint64(total)*8)
		sha256Block(&h, tail[:64], &w)
	} else {
		binary.BigEndian.PutUint64(tail[120:], uint64(total)*8)
		sha256Block(&h, tail[:64], &w)
		sha256Block(&h, tail[64:], &w)
	}
	var out [32]byte
	for i, v := range h {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out
}

func sha256Block(h *[8]uint32, block []byte, w *[64]uint32) {
	for i := 0; i < 16; i++ {
		w[i] = binary.BigEndian.Uint32(block[i*4:])
	}
	for i := 16; i < 64; i++ {
		s0 := sha256Rotr(w[i-15], 7) ^ sha256Rotr(w[i-15], 18) ^ (w[i-15] >> 3)
		s1 := sha256Rotr(w[i-2], 17) ^ sha256Rotr(w[i-2], 19) ^ (w[i-2] >> 10)
		w[i] = w[i-16] + s0 + w[i-7] + s1
	}
	a, b, c, d := h[0], h[1], h[2], h[3]
	e, f, g, hh := h[4], h[5], h[6], h[7]
	for i := 0; i < 64; i++ {
		s1 := sha256Rotr(e, 6) ^ sha256Rotr(e, 11) ^ sha256Rotr(e, 25)
		ch := (e & f) ^ (^e & g)
		t1 := hh + s1 + ch + sha256K[i] + w[i]
		s0 := sha256Rotr(a, 2) ^ sha256Rotr(a, 13) ^ sha256Rotr(a, 22)
		maj := (a & b) ^ (a & c) ^ (b & c)
		t2 := s0 + maj
		hh, g, f, e = g, f, e, d+t1
		d, c, b, a = c, b, a, t1+t2
	}
	h[0] += a
	h[1] += b
	h[2] += c
	h[3] += d
	h[4] += e
	h[5] += f
	h[6] += g
	h[7] += hh
}
