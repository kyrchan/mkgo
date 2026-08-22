package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// VBoxArgs builds the VBoxManage command sequences for the VirtualBox
// backend. Split from execution so tests can assert exact arguments.

func VBoxConvertArgs(manage, rawPath, vdiPath string) []string {
	return []string{"convertfromraw", rawPath, vdiPath, "--format", "VDI"}
}

func VBoxCreateVMArgs(manage, vmName, vmDir string, memMB int) []string {
	return []string{"createvm", "--name", vmName,
		"--basefolder", vmDir, "--ostype", "Other",
		"--register"}
}

func VBoxConfigVMArgs(manage, vmName, vdiPath, sockPath string, memMB int) []string {
	args := []string{"modifyvm", vmName,
		"--firmware", "efi",
		"--memory", fmt.Sprint(memMB),
		"--cpus", "1",
		"--audio", "none",
		"--usb", "off",
		"--nic1", "none",
		// Storage: the converted disk on an IDE controller (firmware-visible).
		"--storagectl", "IDE",
		"--add", "ide",
		"--controller", "PIIX4",
		"--portcount", "2",
	}
	attach := []string{"storageattach", vmName,
		"--storagectl", "IDE",
		"--port", "0", "--device", "0",
		"--type", "hdd", "--medium", vdiPath,
	}
	_ = attach // attached via separate call; kept here for documentation
	// Serial 1: host pipe (unix domain socket) server mode — the gate source.
	serial := []string{"modifyvm", vmName,
		"--uart1", "0x3F8", "4",
		"--uartmode1", "server", sockPath,
	}
	return append(args, serial...)
}

func VBoxAttachDiskArgs(manage, vmName, vdiPath string) []string {
	return []string{"storageattach", vmName,
		"--storagectl", "IDE",
		"--port", "0", "--device", "0",
		"--type", "hdd", "--medium", vdiPath,
	}
}

func VBoxStartArgs(manage, vmName string) []string {
	return []string{"startvm", vmName, "--type", "headless"}
}

func VBoxTeardownArgs(manage, vmName, vmDir string) [][]string {
	return [][]string{
		{"controlvm", vmName, "poweroff"},
		{"unregistervm", vmName, "--delete"},
	}
}

// SocketPath derives a per-run unix socket path for the VBox serial pipe.
func SocketPath(runDir string) string {
	return filepath.Join(runDir, "com1.sock")
}

// SanitizeVMName keeps VM names filesystem/VBox friendly.
func SanitizeVMName(s string) string {
	repl := strings.NewReplacer("/", "-", " ", "_", "\\", "-")
	return repl.Replace(s)
}
