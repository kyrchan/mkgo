package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunVBox boots the image under VirtualBox headless via VBoxManage:
// raw -> VDI conversion, EFI VM with uart1 in server mode on a unix
// socket, then streams the socket into the same GateSet as QEMU.
// Requires VBoxManage on PATH; absence is an honest SKIP, not a failure.
func RunVBox(loc *ToolLocator, opt Options) Result {
	manage, err := loc.VBoxManage()
	if err != nil {
		return Result{"vbox", "skip", err.Error()}
	}
	dir, err := RunDir(opt.runDir(), "vbox")
	if err != nil {
		return Result{"vbox", "fail", err.Error()}
	}
	vmName := SanitizeVMName("hvtest-" + filepath.Base(opt.ImagePath))
	vdiPath := filepath.Join(dir, "disk.vdi")
	sockPath := SocketPath(dir)

	run := func(args ...string) error {
		return ExecRunner()(manage, args...)
	}
	step := func(label string, args []string) error {
		fmt.Fprintf(os.Stderr, "[hvtest] vbox: %s\n", label)
		if err := run(args...); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}

	if err := step("convertfromraw", VBoxConvertArgs(manage, opt.ImagePath, vdiPath)); err != nil {
		return Result{"vbox", "fail", err.Error()}
	}
	if err := step("createvm", VBoxCreateVMArgs(manage, vmName, dir, opt.MemMB)); err != nil {
		return Result{"vbox", "fail", err.Error()}
	}
	if err := step("modifyvm+serial", VBoxConfigVMArgs(manage, vmName, vdiPath, sockPath, opt.MemMB)); err != nil {
		return Result{"vbox", "fail", err.Error()}
	}
	if err := step("storageattach", VBoxAttachDiskArgs(manage, vmName, vdiPath)); err != nil {
		return Result{"vbox", "fail", err.Error()}
	}

	gs := NewGateSet(opt.Gates, func(g string) {
		fmt.Fprintf(os.Stderr, "[hvtest] vbox: gate %q hit\n", g)
	})
	deadline := time.Now().Add(opt.Timeout)
	go captureSocket(sockPath, gs, deadline)

	if err := step("startvm headless", VBoxStartArgs(manage, vmName)); err != nil {
		return Result{"vbox", "fail", err.Error()}
	}

	for !gs.Satisfied() && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	for _, t := range VBoxTeardownArgs(manage, vmName, dir) {
		_ = run(t...) // best-effort cleanup
	}
	if gs.Satisfied() {
		return Result{"vbox", "pass", "all gates hit"}
	}
	return Result{"vbox", "fail", fmt.Sprintf("missing %v after %s", gs.Missing(), opt.Timeout)}
}

// RunVMware boots via vmrun with a generated .vmx (EFI, serial to file).
// Requires vmrun on PATH; absence is an honest SKIP.
func RunVMware(loc *ToolLocator, opt Options) Result {
	vmrun, err := loc.Vmrun()
	if err != nil {
		return Result{"vmware", "skip", err.Error()}
	}
	dir, err := RunDir(opt.runDir(), "vmware")
	if err != nil {
		return Result{"vmware", "fail", err.Error()}
	}
	vmdk := filepath.Join(dir, "disk.vmdk")
	serialLog := filepath.Join(dir, "serial.log")
	vmx := filepath.Join(dir, "hvtest.vmx")

	if err := WriteVMDKForVMware(opt.ImagePath, vmdk); err != nil {
		return Result{"vmware", "fail", err.Error()}
	}
	if err := os.WriteFile(vmx, []byte(GenerateVMX("hvtest", vmdk, serialLog, opt.MemMB)), 0o644); err != nil {
		return Result{"vmware", "fail", err.Error()}
	}

	gs := NewGateSet(opt.Gates, func(g string) {
		fmt.Fprintf(os.Stderr, "[hvtest] vmware: gate %q hit\n", g)
	})
	run := ExecRunner()
	startErr := run(vmrun, "-T", "ws", "start", vmx, "nogui")
	if startErr == nil || true { // vmrun may return nonzero while still booting
		deadline := time.Now().Add(opt.Timeout)
		pollGates(serialLog, gs, deadline, func() bool { return false })
		_ = run(vmrun, "-T", "ws", "stop", vmx, "hard") // best-effort
	}
	if gs.Satisfied() {
		return Result{"vmware", "pass", "all gates hit"}
	}
	return Result{"vmware", "fail",
		fmt.Sprintf("missing %v; vmrun start err=%v; serial at %s", gs.Missing(), startErr, serialLog)}
}
