// console.wasm — the well-known "console" server (abi/ABI.md §1/§2).
//
// Core logic (this file) is plain Go: it runs on the host against
// kern.Bus under `go test` and unmodified as wasm via main.go. The
// service owns-or-binds "console", relays every datagram to its output
// (fd 1 → kernel console window → serial), one line per datagram.
//
// Logging convention (AGENTS.md): services send "[tag] message"
// datagrams; console passes tagged lines through untouched and prefixes
// "[console] " only when a line arrives without a tag.
package main

import (
	"bytes"
	"io"

	"kernel.lane/guests/lib"
)

// Options tunes Run (zero value is production behavior).
type Options struct {
	// Name defaults to kern.NameConsole.
	Name string
	// Stop closes to end the relay loop (tests); nil runs forever.
	Stop <-chan struct{}
	// Mirror receives every rendered line a second time — the
	// "display" face (services/display terminal on the §9.FB window).
	// May be nil. Writes never block the relay path on error.
	Mirror io.Writer
}

const defaultTag = "[console] "

// Serve owns-or-binds name and relays datagrams to out until Stop.
// It never returns an error for transient conditions: bind races are
// retried, would-block sends cannot happen (output is not a port).
func Serve(k kern.Kernel, out io.Writer, opts Options) {
	name := opts.Name
	if name == "" {
		name = kern.NameConsole
	}
	h := k.PortCreate(name)
	for h == kern.InvalidHandle {
		h = k.PortBind(name) // fan-in alias after respawn/preemption
		if h != kern.InvalidHandle {
			break
		}
		if stopped(opts.Stop) {
			return
		}
		k.Yield()
	}

	buf := make([]byte, kern.MaxMsg)
	line := make([]byte, 0, kern.MaxMsg+len(defaultTag))
	for {
		n := k.PortRecv(h, buf)
		if n > 0 {
			rendered := render(line[:0], buf[:int(n)])
			out.Write(rendered)
			if opts.Mirror != nil {
				opts.Mirror.Write(rendered)
			}
		}
		if opts.Stop != nil && stopped(opts.Stop) {
			return
		}
		if n == 0 {
			k.Yield() // §1: recv never blocks; poll with sched_yield
		}
	}
}

// render appends one normalized line (datagram + "\n") onto dst.
func render(dst, msg []byte) []byte {
	m := bytes.TrimRight(msg, "\r\n")
	if len(m) == 0 {
		return dst // kernel rejects empty sends; be safe anyway
	}
	if m[0] != '[' {
		dst = append(dst, defaultTag...)
	}
	dst = append(dst, m...)
	return append(dst, '\n')
}

func stopped(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
