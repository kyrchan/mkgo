# kernel

A freestanding C++20 microkernel whose guest ABI is WebAssembly + a frozen
mini-WASI profile. All OS services (init, console, login, fs, shell, graphics,
network, USB, block storage) are `.wasm` modules written in Go
(`GOOS=wasip1`, stock toolchain). Deliberately non-POSIX: capabilities,
sessions, message ports — no fork/exec/signals/global namespace.

```
shell.wasm   login.wasm   your-app.wasm          sessions (Go/Rust/C/wat)
    │             │             │
    └────────┬────┴────┬──────┘
     message ports (kernel-relayed, capability-guarded)
    ┌──────┴──────────────────────┐
 fs.wasm    graphics.wasm   console.wasm           servers (layer 2: policy)
    └──────────────┬───────────────┘
      MICROKERNEL: scheduler · capability registry · wasm3 · mini-WASI · mm
      VFIO foundation · native device shims (tiny, frozen: PS/2/PIC/UART)
    ┌──────────────┬───────────────┘
  ┌─┴──────────────┴──────────────────┐
  │  virtio path    │  VFIO path      │   ← two interchangeable backends
  │  (paravirtual)  │  (passthrough)  │
  └─────────────────┴─────────────────┘
hardware ← arch/x86_64 shims · PCIe devices via IOMMU
```

Two driver paths per class expose **identical class windows** — guests cannot
tell the difference. virtio for QEMU/VMware/VBox without PCIe passthrough;
VFIO for real hardware or hypervisors with IOMMU. devman ENUM reports
class/instance/window only, never the transport.

## Quickstart

```sh
make image && make run
```

This builds a full disk image (UEFI bootloader + kernel + all `.wasm`
services) and launches it in QEMU. You'll see the boot log on the serial
console, then `login.wasm` prompts for credentials. Log in as `admin`,
`u1`, or `u2` to get a shell.

Available test gates (headless, assert on serial log):

```sh
make test-g1     # smallest guest: hand-written WASI hello (wat2wasm)
make test-g3     # stock Go hello (GOOS=wasip1) — the anti-nightmare proof
make test-p4     # message ports + crash isolation + registry KILL
make test-p5a    # filesystem via kernel-routed preview1 path ops
make test-p5b    # same via direct-port route + cross-user denial test
make test-p7     # interactive: typed login → shell → cat /etc/motd
make test-p8a    # cooperative multitasking: busy + polite interleave
make test-p8b    # virtio-blk device detection
make test-p9     # network E2E: UDP echo + HTTP GET through net.wasm
make test-p10    # multiuser negatives: two users, cross-user denial
make test-all    # every gate above + kernel unit tests
```

Each `test-*` target boots QEMU headless (300 s timeout), asserts literal
substrings on the serial log, and prints `TEST PASS` / `TEST FAIL`.

## Building a Go service module

Services are ordinary Go programs compiled to `wasip1`. The kernel loads
them as `.wasm` modules and relays their port traffic. Example — a `hello`
service that binds a well-known name and echoes messages:

```go
//go:build wasip1

package main

import (
	"kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()
	h := k.PortBind("hello")            // bind well-known name
	buf := make([]byte, lib.MaxMsg)
	for {
		n := k.PortRecv(h, buf)         // >0 = length, 0 = none, -1 = err
		if n > 0 {
			k.PortSend(h, append([]byte("echo: "), buf[:int(n)]...))
		}
		if n == 0 {
			k.Yield()
		}
	}
}
```

Build and ship it:

```sh
cd services/hello
GOOS=wasip1 GOARCH=wasm go build -o hello.wasm
# Embed the mandatory abi_ver custom section (checked by kernel at load):
../../scripts/add_abiver.py hello.wasm hello.wasm 2
# Add it to an existing disk image:
../../tools/img/img ../build/disk.img 64 \
  services/hello/hello.wasm:/boot/modules/hello.wasm
```

Key rules for service modules:

- Import `guests/lib` for port I/O, input, focus — it wraps all `kern_*`
  imports with Go-friendly types.
- Every module MUST carry the `abi_ver` custom section (byte `0x02` for
  ABI v2). The kernel refuses modules without it. v1 modules are NOT
  supported on v2 kernels — clean break.
- Services talk to each other over kernel-relayed message ports — never
  shared memory. Max payload 4096 B per datagram.
- Capability bits (KILL, DEVMAN, POWER, FOCUS, FS_ADMIN, NET_ADMIN,
  SPAWN, CONF, PCI, FB) are granted at login or via init.conf — never
  self-assigned.
- To add a new service: write the Go, compile, embed `abi_ver`, add its
  line to `/etc/init.conf` on the disk image, reboot.

### Writing a VFIO driver (PCIe)

VFIO drivers are also Go→wasm services, but they use `CAP_PCI` and the
`kern_pci_*` imports to drive hardware directly. Example — a skeletal PCIe
driver:

```go
//go:build wasip1

package main

import (
	"kernel.lane/guests/lib"
)

func main() {
	k := lib.Real()
	// Map BAR0 of an assigned PCI device into our address space:
	bar0 := k.PciMapBar(0, 0, 0, 0)  // bus 0, dev 0, fn 0, bar 0
	// Bind the device's MSI-X interrupt to a doorbell:
	irq := k.PciBindIrq(0, 0, 0, 2)   // type 2 = MSI-X
	// Enable bus mastering (DMA) for the device:
	k.PciEnableBusmaster(0, 0, 0)
	// Wait for interrupts instead of polling:
	for {
		k.DoorbellWait(irq, 1000)     // block until IRQ fires or timeout
		// handle device event via bar0 MMIO window...
		_ = bar0
	}
}
```

Once the VFIO foundation lands (Phase 11), new PCIe devices need **zero
kernel code** — just a Go driver in `services/` and an init.conf entry
granting `CAP_PCI`.

## Driver model: two-layer architecture

```
┌─────────────────────────────────────────────────────────┐
│  Layer 2 — wasm session (policy)                        │
│  Consumes class windows / port names. Never touches HW. │
│  fs.wasm, net.wasm, graphics.wasm, ahci.wasm, usb.wasm  │
└────────────────────────┬────────────────────────────────┘
                         │ class window (§2-§6, §9)
┌────────────────────────┴────────────────────────────────┐
│  Layer 1a — native shim (mechanism, ≤300 LOC each)      │
│  Owns real hardware, exposes ONE class window per inst. │
│  Used for: virtio-net, virtio-blk, PS/2, PIT, PIC, UART│
│                                                          │
│  Layer 1b — VFIO passthrough (~2,000 LOC one-time)      │
│  Maps PCI BARs into guest memory with IOMMU protection. │
│  No per-device code — generic infrastructure.            │
│  Used for: GPU, NIC, storage, USB, WiFi, etc.           │
└─────────────────────────────────────────────────────────┘
```

### Adding new hardware — the three-step recipe

1. **Define its class window layout** in `abi/ABI.md` (version bump).
2. **EITHER** write a native shim (≤300 LOC) **OR** assign via VFIO
   (zero LOC). Both expose the same class window.
3. **Register** the instance in devman table (+ capability grant
   template in init.conf).

No kernel-wide changes permitted (except the one-time VFIO foundation).

### PCIe device recipe (VFIO)

1. Kernel enumerates PCI at boot, assigns device to a container via
   `ASSIGN_PCI` registry op.
2. Driver session holds `CAP_PCI`; calls `kern_pci_map_bar()` to get a
   window offset for the device's BAR MMIO.
3. Driver calls `kern_pci_bind_irq()` to get a doorbell handle for
   MSI/MSI-X interrupts.
4. Driver programs the device through MMIO writes to the mapped window;
   blocks on `kern_doorbell_wait()` instead of polling.
5. IOMMU restricts all DMA to assigned pages — compromised driver cannot
   DMA outside its scope.

### Capability bits

| Bit | Name | Purpose |
|-----|------|---------|
| 0 | KILL | Terminate sessions |
| 1 | DEVMAN | Device enumeration, PCI assignment |
| 2 | POWER | Reboot / poweroff |
| 3 | FOCUS | Set input focus |
| 4 | FS_ADMIN | Raw block access, /etc writes |
| 5 | NET_ADMIN | Network stack administration |
| 6 | SPAWN | Launch new modules |
| 7 | CONF | Apply kernel.conf knobs |
| 8 | PCI | `kern_pci_*` VFIO access |
| 9 | FB | Framebuffer modesetting |

## Architecture

- `core/` — arch-blind kernel (zero `#ifdef`, zero inline asm). WASI glue,
  engine wrapper, scheduler, ports, input, FS transport, block backends,
  VFIO foundation, PCI.
- `arch/x86_64/` — machine shims: uart, cpu, traps (IDT), paging, timer,
  math (SSE), vector (AVX2), ctx/preempt (context switching).
- `third_party/wasm3/` — vendored wasm3 v0.5.0 (MIT) execution engine.
- `services/` — Go services compiled to wasip1: console, login, fs, init,
  shell, graphics, net, usb, bt, wlan, e1000, ahci. Shared libc in
  `lib/kern.go`.
- `guests/` — payload programs in C(wat), Rust(wasm32v1-none), Go(wasip1).
- `abi/ABI.md` — frozen guest-facing interface contracts (v2.0).

## Building guests

| Language | Toolchain | Target |
|----------|-----------|--------|
| C (wat)  | [wabt](https://github.com/WebAssembly/wabt) → `wat2wasm` | wasm32 |
| Rust     | rustup target wasm32v1-none | wasm32v1-none |
| Go       | stock go ≥ 1.21 | GOOS=wasip1 GOARCH=wasm |

All modules carry an `abi_ver` custom section (byte `0x02` for v2).

## Requirements

- GCC ≥ 12 / G++ ≥ 12 (C++20 freestanding)
- Go ≥ 1.21 (for wasip1 target)
- QEMU ≥ 6.0 with OVMF firmware
- [wabt](https://github.com/WebAssembly/wabt) (wat2wasm) at ~/.local/wabt

## License

MIT (see third_party/wasm3/LICENSE for wasm3).
