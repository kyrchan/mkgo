package main

import (
	"os"
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	in := "\x1b[2J\x1b[HKERNEL-OK \x1b[1;32mgreen\x1b[0m out 0x28\n"
	got := NormalizeSerial(in)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape survived: %q", got)
	}
	if !strings.Contains(got, "KERNEL-OK") || !strings.Contains(got, "out 0x28") {
		t.Fatalf("content lost: %q", got)
	}
}

func TestGateSetChunkBoundaries(t *testing.T) {
	// Gates straddling feed-chunk boundaries must still be found.
	gs := NewGateSet([]string{"KERNEL-OK"}, nil)
	chunks := [][]byte{
		[]byte("[boot] loa"),
		[]byte("ding...\r\nKERNE"),
		[]byte("L-OK\r\n"),
	}
	for _, c := range chunks {
		gs.Feed(c)
	}
	if !gs.Satisfied() {
		t.Fatalf("gate missed across chunks; missing %v", gs.Missing())
	}
}

func TestGateSetANSIAndCRLF(t *testing.T) {
	gs := NewGateSet([]string{"hello from Go", "rounds ok=3"}, nil)
	gs.Feed([]byte("\x1b[Khello from Go\x1b[0m\r\n"))
	gs.Feed([]byte("rounds ok=3\r\n"))
	if !gs.Satisfied() {
		t.Fatalf("missing %v", gs.Missing())
	}
}

func TestGateSetMissing(t *testing.T) {
	gs := NewGateSet([]string{"a", "b"}, nil)
	gs.Feed([]byte("only a here"))
	if gs.Satisfied() || len(gs.Missing()) != 1 || gs.Missing()[0] != "b" {
		t.Fatalf("missing=%v satisfied=%v", gs.Missing(), gs.Satisfied())
	}
}

func TestVMXGeneration(t *testing.T) {
	vmx := GenerateVMX("hvtest", "/vms/disk.vmdk", "/vms/serial.log", 512)
	required := []string{
		`firmware = "efi"`,
		`guestOS = "other-64"`,
		`memsize = "512"`,
		`ide0:0.fileName = "/vms/disk.vmdk"`,
		`ide0:0.deviceType = "disk"`,
		`serial0.present = "TRUE"`,
		`serial0.fileType = "file"`,
		`serial0.fileName = "/vms/serial.log"`,
	}
	for _, want := range required {
		if !strings.Contains(vmx, want) {
			t.Fatalf(".vmx missing %q:\n%s", want, vmx)
		}
	}
}

func TestWriteVMDKForVMware(t *testing.T) {
	dir := t.TempDir()
	raw := make([]byte, 2<<20)
	rawPath := dir + "/in.img"
	if err := writeAll(rawPath, raw); err != nil {
		t.Fatal(err)
	}
	out := dir + "/disk.vmdk"
	if err := WriteVMDKForVMware(rawPath, out); err != nil {
		t.Fatal(err)
	}
	desc := readFileStr(t, out)
	for _, want := range []string{
		`createType="monolithicFlat"`,
		"RW 4096 FLAT \"disk-flat.vmdk\" 0",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("descriptor missing %q:\n%s", want, desc)
		}
	}
	flat := readFileStr(t, dir+"/disk-flat.vmdk")
	if len(flat) != len(raw) {
		t.Fatalf("flat size %d want %d", len(flat), len(raw))
	}
}

func TestVBoxArgsBuilders(t *testing.T) {
	conv := VBoxConvertArgs("VBoxManage", "/x/in.img", "/x/d.vdi")
	if !contains(conv, "convertfromraw", "/x/in.img", "/x/d.vdi") {
		t.Fatalf("convert args wrong: %v", conv)
	}
	cfg := VBoxConfigVMArgs("VBoxManage", "vm1", "/x/d.vdi", "/x/com1.sock", 512)
	joined := strings.Join(cfg, " ")
	for _, want := range []string{"--firmware efi", "--uart1 0x3F8 4",
		"--uartmode1 server /x/com1.sock"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("config args missing %q: %v", want, cfg)
		}
	}
	start := VBoxStartArgs("VBoxManage", "vm1")
	if !contains(start, "startvm", "--type", "headless") {
		t.Fatalf("start args wrong: %v", start)
	}
	teardown := VBoxTeardownArgs("VBoxManage", "vm1", "/x")
	if len(teardown) != 2 ||
		!contains(teardown[0], "controlvm", "poweroff") ||
		!contains(teardown[1], "unregistervm", "--delete") {
		t.Fatalf("teardown wrong: %v", teardown)
	}
}

func TestQEMUArgsBuilder(t *testing.T) {
	args := QEMUArgs("/usr/bin/qemu-system-x86_64",
		"/fw/OVMF_CODE.fd", "/fw/OVMF_VARS.fd", "/tmp/VARS.fd",
		"/img/disk.img", "/tmp/serial.log", 512)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-machine q35",
		"if=pflash,format=raw,readonly=on,file=/fw/OVMF_CODE.fd",
		"if=pflash,format=raw,file=/tmp/VARS.fd",
		"format=raw,file=/img/disk.img",
		"-display none -no-reboot",
		"-serial file:/tmp/serial.log",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("qemu args missing %q: %v", want, args)
		}
	}
}

func TestToolLocatorHonestSkips(t *testing.T) {
	// A locator that finds nothing must report absence, not fake success.
	loc := &ToolLocator{Home: t.TempDir(), Find: func(string) (string, error) {
		return "", &notFoundErr{}
	}}
	if _, err := loc.VBoxManage(); err == nil {
		t.Fatal("VBoxManage should be absent")
	} else if !strings.Contains(err.Error(), "skip") {
		t.Fatalf("skip guidance missing from error: %v", err)
	}
	if _, err := loc.Vmrun(); err == nil {
		t.Fatal("Vmrun should be absent")
	}
}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

// helpers ------------------------------------------------------------

func contains(args []string, wants ...string) bool {
	j := strings.Join(args, "\x00")
	for _, w := range wants {
		if !strings.Contains(j, w) {
			return false
		}
	}
	return true
}

func writeAll(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
