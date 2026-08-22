package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Result is the outcome of one backend run.
type Result struct {
	Backend string
	Status  string // "pass" | "skip" | "fail"
	Detail  string
}

// Options configures a harness run.
type Options struct {
	ImagePath string
	Gates     []string
	Timeout   time.Duration
	MemMB     int
	BaseDir   string // scratch dir (defaults to os.TempDir())
}

func (o *Options) runDir() string {
	if o.BaseDir != "" {
		return o.BaseDir
	}
	return os.TempDir()
}

// pollGates feeds file contents into the gate set until satisfied or
// deadline. Used by file-backed serial backends (QEMU, VMware).
func pollGates(path string, gates *GateSet, deadline time.Time, check func() bool) error {
	for time.Now().Before(deadline) {
		b, err := TailFile(path)
		if err != nil {
			return err
		}
		gates.Feed(b)
		if gates.Satisfied() {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
		_ = check
	}
	return fmt.Errorf("timeout waiting for gates")
}

// RunQEMU boots diskPath under QEMU headless and asserts the gates.
func RunQEMU(loc *ToolLocator, opt Options) Result {
	qemu, err := loc.QEMU()
	if err != nil {
		return Result{"qemu", "fail", err.Error()}
	}
	code, vars, err := loc.OVMF()
	if err != nil {
		return Result{"qemu", "fail", err.Error()}
	}
	dir, err := RunDir(opt.runDir(), "qemu")
	if err != nil {
		return Result{"qemu", "fail", err.Error()}
	}
	varsCopy := filepath.Join(dir, "VARS.fd")
	serial := filepath.Join(dir, "serial.log")
	if err := PrepareVarsCopy(vars, varsCopy); err != nil {
		return Result{"qemu", "fail", err.Error()}
	}

	gs := NewGateSet(opt.Gates, func(g string) {
		fmt.Fprintf(os.Stderr, "[hvtest] qemu: gate %q hit\n", g)
	})
	deadline := time.Now().Add(opt.Timeout)
	full := QEMUArgs(qemu, code, vars, varsCopy, opt.ImagePath, serial, opt.MemMB)
	cmd := exec.Command(full[0], full[1:]...) // builder returns full argv
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Local toolchain installs need their private libs and firmware dirs
	// (mirrors the Makefile's QEMU_ENV / -L flags).
	if strings.Contains(qemu, "osdev-root") {
		root := strings.TrimSuffix(qemu, "/usr/bin/qemu-system-x86_64")
		cmd.Env = append(os.Environ(),
			"LD_LIBRARY_PATH="+root+"/usr/lib/x86_64-linux-gnu")
		args := cmd.Args
		extra := []string{"-L", root + "/usr/share/qemu", "-L", root + "/usr/share/seabios"}
		// Insert firmware dirs directly after argv[0]; anything later
		// risks being swallowed by the preceding option's value.
		cmd.Args = append(append([]string{args[0]}, extra...), args[1:]...)
	}
	if err := cmd.Start(); err != nil {
		return Result{"qemu", "fail", err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for !gs.Satisfied() && time.Now().Before(deadline) {
		select {
		case <-tick.C:
			if b, err := TailFile(serial); err == nil {
				gs.Feed(b)
			}
		case werr := <-done:
			// QEMU exited (halt or crash); do a final feed then judge.
			if b, err := TailFile(serial); err == nil {
				gs.Feed(b)
			}
			if gs.Satisfied() {
				return Result{"qemu", "pass", "all gates hit"}
			}
			return Result{"qemu", "fail",
				fmt.Sprintf("qemu exited early (%v); missing %v", werr, gs.Missing())}
		}
	}
	_ = cmd.Process.Kill()
	<-done
	if gs.Satisfied() {
		return Result{"qemu", "pass", "all gates hit"}
	}
	return Result{"qemu", "fail", fmt.Sprintf("missing %v; serial at %s", gs.Missing(), serial)}
}

// captureSocket connects to the VBox serial unix socket and streams data
// into the gate set until satisfied or deadline.
func captureSocket(sockPath string, gs *GateSet, deadline time.Time) {
	for time.Now().Before(deadline) && !gs.Satisfied() {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue // VM may not have opened its end yet
		}
		buf := make([]byte, 4096)
		for !gs.Satisfied() && time.Now().Before(deadline) {
			n, err := conn.Read(buf)
			if n > 0 {
				gs.Feed(buf[:n])
			}
			if err != nil || n == 0 {
				break
			}
		}
		conn.Close()
	}
}
