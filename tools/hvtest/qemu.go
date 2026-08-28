package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// QEMUArgs assembles the headless UEFI boot command for the reference
// backend. Mirrors the Makefile's QEMU_BASE (q35, OVMF pflash pair,
// no display, serial to file) so gates assert the identical boot path.
func QEMUArgs(qemuBin, ovmfCode, ovmfVars, varsCopy, diskPath, serialLog string, memMB int) []string {
	args := []string{
		qemuBin,
		"-machine", "q35",
		"-cpu", "max",
		"-m", fmt.Sprint(memMB),
		"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
		"-drive", fmt.Sprintf("if=pflash,format=raw,file=%s", varsCopy),
		"-drive", fmt.Sprintf("format=raw,file=%s", diskPath),
		"-display", "none",
		"-no-reboot",
		"-net", "none",
		"-serial", fmt.Sprintf("file:%s", serialLog),
	}
	return args
}

// PrepareVarsCopy copies the OVMF vars template (QEMU requires a writable
// pflash varstore per VM).
func PrepareVarsCopy(template, dst string) error {
	in, err := os.ReadFile(template)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}

// TailFile returns the current contents of path, tolerating not-yet-created
// files (hypervisors create serial outputs lazily).
func TailFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// RunDir creates and returns a fresh working directory for one backend run.
func RunDir(base, backend string) (string, error) {
	d := filepath.Join(base, "hvtest-"+backend)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

// ShortSummary renders a compact one-line result for matrix reporting.
func ShortSummary(backend string, res Result) string {
	return fmt.Sprintf("%-8s %-5s %s", backend, strings.ToUpper(res.Status), res.Detail)
}
