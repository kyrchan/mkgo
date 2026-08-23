//go:build wasip1

package main

import (
	"unsafe"

	lib "kernel.lane/guests/lib"
	"kernel.lane/services/display/terminal"
)

type consoleRelay struct{ h lib.Handle }

func (r consoleRelay) PortRecv(buf []byte) int32 {
	return lib.Real().PortRecv(r.h, buf)
}

func (r consoleRelay) Yield() { lib.Real().Yield() }

func consoleBind() consoleRelay {
	// The relay is useless until the console service owns its name;
	// retry instead of latching onto InvalidHandle when boot order or
	// respawn timing puts display ahead of console.
	for {
		if h := lib.Real().PortBind(lib.NameConsole); h != lib.InvalidHandle {
			return consoleRelay{h: h}
		}
		lib.Real().Yield()
	}
}

func ptrAt(off uint64, n int) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(off))), n)
}

func attachFB() (*terminal.FBWindow, error) {
	dm, err := lib.BindDevman(lib.Real())
	if err != nil {
		return nil, err
	}
	recs, derr := dm.Enum()
	if derr != nil {
		return nil, derr
	}
	for _, r := range recs {
		if r.Class == lib.ClassFramebuffer {
			return terminal.NewFBWindow(ptrAt(r.WinOff, terminal.FBWindowMin))
		}
	}
	return nil, terminal.ErrNoDisplay
}
