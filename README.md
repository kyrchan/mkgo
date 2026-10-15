# mkgo

A freestanding x86‑64 microkernel that boots via UEFI and runs every OS service
as WebAssembly. The substrate is a small, owned C++20 kernel with a
project‑native Wasm engine — no third‑party runtime, no JIT fallback in
production. All services are `.wasm` modules, preferably written in Go on a
stock `GOOS=wasip1` toolchain. Non‑POSIX by design: capability bits, sessions,
and kernel‑mediated message ports — no fork/exec, no signals, no global
namespace.

## Quick start

```sh
make image   # build/BOOTX64.EFI + build/disk-p7.img (64 MiB)
make run     # boot disk-p7.img in QEMU with OVMF (serial console)
make test-g1 # headless boot gate: asserts the kernel banner + `>` shell
```

Host tooling (all stock, no patches):
- g++/gcc (freestanding C++20: `-fno-exceptions -fno-rtti -fno-threadsafe-statics`)
- Go ≥ 1.24 (`GOOS=wasip1 GOARCH=wasm` for services)
- python3 (runs `scripts/mkpefi.py` and `scripts/add_abiver.py`)
- clang + WABT `wat2wasm`, or `rustup` target `wasm32v1-none` + `wasm-ld` (guests)
- OVMF (`OVMF_CODE_4M.fd` / `OVMF_VARS_4M.fd`) + QEMU/KVM

## Layout

| path | contents |
|------|----------|
| `kernel/link.ld` | UEFI link script (ELF → `kernel.so` → `BOOTX64.EFI` via `mkpefi.py`) |
| `core/` | kernel substrate: scheduler, MM, capability registry, message ports, owned async preemption, and the owned Wasm engine + mini‑WASI bridge (`core/wasm/`) |
| `arch/x86_64/` | machine contact only: GNU as boot/traps/ctx/timer/vector, UART, paging, MP, VFIO — the substrate stays `#ifdef`/inline‑asm free |
| `services/` | service `.wasm` modules in Go (`console`, `login`, `init`, `shell`, `fs`, `net`, …) |
| `guests/` | hello‑world ladders — C → `wat2wasm`, Rust → `wasm32v1-none`, Go → `wasip1` — plus phase test guests |
| `kfs/` | KFS1 log‑structured filesystem (FAT16 import/export filter) |
| `tools/` | `img` (disk builder), `modulesign`/`module-sign` (ed25519), C++ host tests |
| `scripts/` | `mkpefi.py` (ELF→PE32+), `add_abiver.py` (stamps the `abi_ver` custom section) |
| `abi/ABI.md` | guest‑facing interface contracts (ports, windows, caps, net, PCI/VFIO) |

## ABI highlights (`abi/ABI.md`)

- **Message ports** (`kern_port_create/bind/send/recv`) — kernel‑relayed,
  4096‑byte datagrams, non‑blocking `recv`, capability‑guarded.
- **Block window** — `fd_read`/`fd_write`/`path_*` routed to `fs.wasm` over
  ports; FAT16 on a RAM‑disk backend (virtio‑blk gives persistence).
- **Devices** — PCI/VFIO mediated BAR access, framebuffer, input, net packet
  windows, doorbells — all handed to the owning service, never touched by
  guest wasm directly.
- **Capabilities** — per‑session cap bits at the kernel registry; `/home/<u>`
  root‑namespacing, `/tmp` world‑writable, `/etc` readable, `/boot/modules`
  writable only with `CAP_SPAWN`+`CAP_FS_ADMIN`.
- **Frozen WASI profile** — `fd_write, proc_exit, clock_time_get, random_get,
  args_*`, `sched_yield`; guest `sched_yield` is **not** required for
  preemption — a per‑CPU timer interrupts at any host instruction, the engine
  resumes only at an explicit commit/continuation point.
- **Module identity** — every `.wasm` carries `abi_ver=2`; the kernel rejects
  a mismatch at load (`core/wasm/owned_session.cc`).

## Verification

`make image` builds a 64 MiB disk (`build/disk-p7.img`) holding the kernel EFI
+ stamped service modules + a boot payload, and boots to an interactive `>`
shell. The host‑test suite exercises the substrate and owned engine from C++:
`make test-kernel` (177+ tests), `make test-wasm-interp`, `make test-wasm-owned`,
`make test-wasm-wasi`, `make test-wasm-artifacts`, `make test-wasm-fuzz`,
`make test-ctx-fpu`, `make test-unit`.

## License

Eclipse Public License v1.0 — see `LICENSE`.
