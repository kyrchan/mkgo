//go:build wasip1

// console.wasm entry: wires the frozen ABI surface (kern.Real) and
// serial stdout into the portable Serve loop. When a §9.FB framebuffer
// is present (devman ENUM class 9), every relayed line is additionally
// rendered into a text-mode terminal — the "display" face.
//
// Build:
//
//	GOOS=wasip1 GOARCH=wasm go build -o console.wasm main.go console.go
package main

import (
	"io"
	"os"

	"kernel.lane/guests/lib"
	"kernel.lane/services/display/terminal"
)

func main() {
	os.Stdout.WriteString("[console] up\n")
	Serve(lib.Real(), os.Stdout, Options{Mirror: displayFace()})
}

// displayFace returns a writer that renders lines into the framebuffer
// terminal, or nil when no §9.FB device is attached (headless boot).
func displayFace() io.Writer {
	k := lib.Real()
	dm, err := lib.BindDevman(k)
	if err != nil {
		return nil
	}
	recs, derr := dm.Enum()
	if derr != nil {
		return nil
	}
	for _, r := range recs {
		if r.Class != lib.ClassFramebuffer {
			continue
		}
		mem := fbMemAt(r.WinOff, terminal.FBWindowMin)
		fbw, ferr := terminal.NewFBWindow(mem)
		if ferr != nil {
			return nil
		}
		tr := terminal.New(fbw)
		tr.WriteString("\x1b[1;37mkernel-lane\x1b[0m\n")
		tr.Flush()
		return &terminalWriter{tr: tr}
	}
	return nil
}

// terminalWriter adapts the Terminal to io.Writer (one Write == one line).
type terminalWriter struct{ tr *Terminal }

func (tw *terminalWriter) Write(p []byte) (int, error) {
	tw.tr.WriteString(string(p))
	tw.tr.WriteString("\n")
	tw.tr.Flush()
	return len(p), nil
}
