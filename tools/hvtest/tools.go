package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Backend-agnostic runner interface: real exec at runtime, fakes in tests.
type Runner func(name string, args ...string) error

func ExecRunner() Runner {
	return func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}

func LookPath(file string) (string, error) { return exec.LookPath(file) }

// ToolLocator resolves external hypervisor tools. Absence is reported
// honestly as a skip reason, never silently ignored.
type ToolLocator struct {
	Home string // os.UserHomeDir override for tests
	Find func(string) (string, error)
}

func DefaultLocator() *ToolLocator {
	home, _ := os.UserHomeDir()
	return &ToolLocator{
		Home: home,
		Find: func(n string) (string, error) { return LookPath(n) },
	}
}

// QEMU resolves qemu-system-x86_64 from PATH or the project's local
// toolchain prefix (~/.local/osdev-root), matching the Makefile's search.
func (l *ToolLocator) QEMU() (string, error) {
	if p, err := l.Find("qemu-system-x86_64"); err == nil {
		return p, nil
	}
	p := filepath.Join(l.Home, ".local/osdev-root/usr/bin/qemu-system-x86_64")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("qemu-system-x86_64 not found in PATH or ~/.local/osdev-root/usr/bin")
}

// OVMF resolves the UEFI firmware code+vars pair.
func (l *ToolLocator) OVMF() (code, vars string, err error) {
	dirs := []string{
		filepath.Join(l.Home, ".local/osdev-root/usr/share/OVMF"),
		"/usr/share/OVMF",
		"/usr/share/ovmf",
	}
	names := [][]string{
		{"OVMF_CODE_4M.fd", "OVMF_VARS_4M.fd"},
		{"OVMF_CODE.fd", "OVMF_VARS.fd"},
	}
	for _, d := range dirs {
		for _, n := range names {
			c, v := filepath.Join(d, n[0]), filepath.Join(d, n[1])
			if _, e1 := os.Stat(c); e1 == nil {
				if _, e2 := os.Stat(v); e2 == nil {
					return c, v, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("no OVMF firmware found under %s", strings.Join(dirs, ", "))
}

// VBoxManage finds VirtualBox's CLI. Not bundled with this project; if it
// is absent we report a skip with installation guidance.
func (l *ToolLocator) VBoxManage() (string, error) {
	if p, err := l.Find("VBoxManage"); err == nil {
		return p, nil
	}
	for _, p := range []string{
		"/usr/bin/VBoxManage",
		"/usr/local/bin/VBoxManage",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("VBoxManage not installed (skip: install VirtualBox >= 6.1 and ensure VBoxManage is on PATH)")
}

// Vmrun finds VMware's vmrun CLI.
func (l *ToolLocator) Vmrun() (string, error) {
	if p, err := l.Find("vmrun"); err == nil {
		return p, nil
	}
	for _, p := range []string{
		"/usr/bin/vmrun",
		"/usr/local/bin/vmrun",
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("vmrun not installed (skip: install VMware Workstation/Fusion and ensure vmrun is on PATH)")
}
