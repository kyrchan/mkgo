package main

import (
	"bytes"
	"regexp"
	"strings"
)

// Gate checking: all three hypervisor backends feed their captured serial
// output through the same GateSet, so the assertion is structurally
// identical no matter where the image booted.

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// StripANSI removes CSI escape sequences (the kernel console emits them).
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// NormalizeSerial folds CR/LF noise so greps are line-ending agnostic.
func NormalizeSerial(s string) string {
	return strings.ReplaceAll(StripANSI(s), "\r\n", "\n")
}

// GateSet accumulates serial output and reports which gate strings have
// been seen. Gates match as plain substrings of normalized output.
type GateSet struct {
	gates []string
	found map[string]bool
	buf   bytes.Buffer
	onHit func(gate string) // optional callback (progress reporting)
}

func NewGateSet(gates []string, onHit func(string)) *GateSet {
	return &GateSet{
		gates: gates,
		found: make(map[string]bool, len(gates)),
		onHit: onHit,
	}
}

// Feed ingests a chunk of raw (possibly ANSI-laden) serial data.
func (g *GateSet) Feed(chunk []byte) {
	if len(g.found) == len(g.gates) {
		return // everything already matched; stop buffering
	}
	g.buf.Write(chunk)
	norm := NormalizeSerial(g.buf.String())
	for _, gate := range g.gates {
		if !g.found[gate] && strings.Contains(norm, gate) {
			g.found[gate] = true
			if g.onHit != nil {
				g.onHit(gate)
			}
		}
	}
	// Keep the tail: a gate could straddle a chunk boundary. Trim to the
	// longest possible partial match length to bound memory.
	const keep = 4096
	if g.buf.Len() > keep {
		tail := g.buf.Bytes()[g.buf.Len()-keep:]
		g.buf.Reset()
		g.buf.Write(tail)
	}
}

// Satisfied reports whether every gate has been seen.
func (g *GateSet) Satisfied() bool { return len(g.found) == len(g.gates) }

// Missing lists gates not yet observed.
func (g *GateSet) Missing() []string {
	var out []string
	for _, gate := range g.gates {
		if !g.found[gate] {
			out = append(out, gate)
		}
	}
	return out
}
