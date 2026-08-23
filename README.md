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
make test-g1     # smallest guest: hand-written WASI hello (wat2wasm)
make test-g3     # stock Go hello (GOOS=wasip1) — the anti-nightmare proof
make test-p4     # message ports + crash isolation + registry KILL
make test-p5a    # FAT16 filesystem via kernel-routed preview1 path ops
make test-p5b    # same via direct-port route + cross-user denial test
make test-p7     # interactive: typed login → shell → cat /etc/motd
make test-p8a    # cooperative multitasking: busy + polite interleave
make test-p8b    # virtio-blk device detection
make run         # interactive serial session (console.wasm visible)
```

Each `test-*` target boots QEMU headless, asserts literal substrings on the
serial log, and prints TEST PASS / TEST FAIL.

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

## Building guests

| Language | Toolchain | Target |
|----------|-----------|--------|
| C (wat)  | [wabt](https://github.com/WebAssembly/wabt) → `wat2wasm` | wasm32 |
| Rust     | rustup target wasm32v1-none | wasm32v1-none |
| Go       | stock go ≥ 1.21 | GOOS=wasip1 GOARCH=wasm |

All modules carry an `abi_ver=1` custom section (checked by kernel at load).

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
- mtools (mformat/mmd/mcopy for disk image construction)

## License

MIT (see third_party/wasm3/LICENSE for wasm3).
