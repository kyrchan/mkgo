# kernel

A freestanding C++20 microkernel whose guest ABI is WebAssembly + a frozen
mini-WASI profile. All OS services (init, console, login, fs, shell) are
`.wasm` modules written in Go (`GOOS=wasip1`, stock toolchain). Deliberately
non-POSIX: capabilities, sessions, message ports — no fork/exec/signals.

```
shell.wasm   login.wasm   your-app.wasm          sessions (Go/Rust/C/wat)
    │             │             │
    └────────┬────┴────┬──────┘
     message ports (kernel-relayed, capability-guarded)
    ┌──────┴──────────────────────┐
 fs.wasm    drivers        console.wasm            servers
    └──────────────┬───────────────┘
      MICROKERNEL: scheduler · capability registry · wasm3 · mini-WASI · mm
hardware ← arch/x86_64 shims (uart, traps, timer, PCI, virtio-blk)
```

## Quickstart

```sh
make image && make run
```

This builds a full disk image (UEFI bootloader + kernel + all `.wasm`
services) and launches it in QEMU. You'll see the boot log on the serial
console, then `login.wasm` prompts for credentials. Log in as `admin`,
`u1`, or `u2` (password same as username) to get a shell.

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

## Architecture

- `core/` — arch-blind kernel (zero `#ifdef`, zero inline asm). WASI glue,
  engine wrapper, scheduler, ports, input, FS transport, block backends.
- `arch/x86_64/` — machine shims: uart, cpu, traps (IDT), paging, timer,
  math (SSE), vector (AVX2), ctx/preempt (context switching).
- `third_party/wasm3/` — vendored wasm3 v0.5.0 (MIT) execution engine.
- `services/` — Go services compiled to wasip1: console, login, fs, init,
  shell. Shared libc in `lib/kern.go`.
- `guests/` — payload programs in C(wat), Rust(wasm32v1-none), Go(wasip1).
- `abi/ABI.md` — frozen guest-facing interface contracts (v1.3).

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
../../scripts/add_abiver.py hello.wasm hello.wasm 1
# Add it to an existing disk image:
../../tools/img/img ../build/disk.img 64 \
  services/hello/hello.wasm:/boot/modules/hello.wasm
```

Key rules for service modules:

- Import `guests/lib` for port I/O, input, focus — it wraps all `kern_*`
  imports with Go-friendly types.
- Every module MUST carry the `abi_ver` custom section (byte `0x01` for
  ABI v1). The kernel refuses modules without it.
- Services talk to each other over kernel-relayed message ports — never
  shared memory. Max payload 4096 B per datagram.
- Capability bits (KILL, DEVMAN, POWER, FOCUS, FS_ADMIN, NET_ADMIN,
  SPAWN, CONF) are granted at login or via init.conf — never self-assigned.
- To add a new service: write the Go, compile, embed `abi_ver`, add its
  line to `/etc/init.conf` on the disk image, reboot.

## Building guests

| Language | Toolchain | Target |
|----------|-----------|--------|
| C (wat)  | [wabt](https://github.com/WebAssembly/wabt) → `wat2wasm` | wasm32 |
| Rust     | rustup target wasm32v1-none | wasm32v1-none |
| Go       | stock go ≥ 1.21 | GOOS=wasip1 GOARCH=wasm |

All modules carry an `abi_ver` custom section (checked by kernel at load).

## Key design decisions

- **wasm as guest ABI**: stock toolchains, sandboxing, byte-identical
  portability. No patched Go runtime (the "anti-nightmare" rule).
- **Non-POSIX**: capabilities, sessions, message ports. No fork/exec.
- **Two-level drivers**: native shim owns HW; wasm session holds policy.
- **Flat identity mapping** (no per-arch page table management).
- **Cooperative scheduling primary**; IRQ preemption implemented but off.

## Requirements

- GCC ≥ 12 / G++ ≥ 12 (C++20 freestanding)
- Go ≥ 1.21 (for wasip1 target)
- QEMU ≥ 6.0 with OVMF firmware
- [wabt](https://github.com/WebAssembly/wabt) (wat2wasm) at ~/.local/wabt

## License

MIT (see third_party/wasm3/LICENSE for wasm3).
