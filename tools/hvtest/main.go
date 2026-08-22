// hvtest: headless hypervisor test scaffolding for the Phase-12 matrix.
//
// Boots the SAME disk image under QEMU (reference), VirtualBox, and
// VMware; asserts identical gate strings on serial output from all three.
// Missing hypervisor tools are reported honestly as SKIP with install
// guidance — never silently ignored.
//
// Usage:
//
//	hvtest -img build/disk.img [-gates KERNEL-OK] [-timeout 120s] \
//	       [-mem 512] [qemu|vbox|vmware|all]
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		img     = flag.String("img", "build/disk.img", "raw disk image to boot")
		gateStr = flag.String("gates", "KERNEL-OK", "comma-separated gate strings")
		timeout = flag.Duration("timeout", 120*time.Second, "per-backend boot timeout")
		mem     = flag.Int("mem", 512, "guest memory MB")
		base    = flag.String("workdir", "", "scratch dir (default os.TempDir())")
	)
	flag.Parse()

	backend := "all"
	if args := flag.Args(); len(args) > 0 {
		backend = args[0]
	}
	switch backend {
	case "qemu", "vbox", "vmware", "all":
	default:
		fmt.Fprintf(os.Stderr, "hvtest: unknown backend %q (want qemu|vbox|vmware|all)\n", backend)
		os.Exit(2)
	}
	if _, err := os.Stat(*img); err != nil {
		fmt.Fprintf(os.Stderr, "hvtest: image not readable: %v\n", err)
		os.Exit(2)
	}
	opt := Options{
		ImagePath: *img,
		Gates:     splitGates(*gateStr),
		Timeout:   *timeout,
		MemMB:     *mem,
		BaseDir:   *base,
	}

	loc := DefaultLocator()
	var results []Result
	switch backend {
	case "qemu":
		results = []Result{RunQEMU(loc, opt)}
	case "vbox":
		results = []Result{RunVBox(loc, opt)}
	case "vmware":
		results = []Result{RunVMware(loc, opt)}
	case "all":
		results = []Result{
			RunQEMU(loc, opt),
			RunVBox(loc, opt),
			RunVMware(loc, opt),
		}
	}

	failed := false
	fmt.Fprintln(os.Stderr, "backend  status detail")
	for _, r := range results {
		fmt.Fprintln(os.Stderr, ShortSummary(r.Backend, r))
		if r.Status == "fail" {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func splitGates(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
