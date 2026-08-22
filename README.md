# kernel-lane-tools

A **freestanding C++20 microkernel** whose guest ABI is **WebAssembly +
a frozen mini-WASI profile**. Every OS service — shell, login, filesystem,
network stack — is an ordinary `.wasm` module written in Go (`GOOS=wasip1`,
stock unpatched toolchain) and isolated by the kernel's wasm engine.

Deliberately **non-POSIX**: capabilities, sessions, message ports. No fork,
no exec, no signals, no global namespace. Portable by construction to
x86_64 / aarch64 / riscv64: services are bytecode and never get ported;
only the thin native substrate does.

```
login.wasm   shell.wasm   your-app.wasm          sessions (Go/Rust/C)
    │             │             │
    └──────┬──────┴──────┬──────┘
     message ports (kernel-relayed, capability-guarded)
    ┌──────┴──────────────────────┐
 fs.wasm    drivers.wasm*   console.wasm            servers
    └──────────────┬───────────────┘
      MICROKERNEL: scheduler · capability registry · wasm3 ·
      mini-WASI · mm · native device shims (tiny, frozen)
hardware
```

\* Raw hardware access lives in tiny native device-window shims; wasm
drivers hold policy only.

## Design notes (the non-POSIX part)

- **Sessions, not processes.** A session is one wasm instance. The kernel
  schedules sessions round-robin (cooperative today, IRQ-preemptive in
  Phase 8) and owns every capability.
- **Message ports instead of IPC syscalls.** Datagram ports with
  well-known names (`"console"`, `"fs"`, `"net"`, …); the kernel mediates
  every copy; `recv` never blocks.
- **Capabilities instead of UIDs.** Authority is a bit-set granted only at
  login and enforced in the kernel registry. There is no root/su; the
  static `admin` user holds every bit.
- **WASI as the syscall layer.** Guests import a frozen preview1 subset
  (`fd_write`, `proc_exit`, `clock_time_get`, `random_get`, `args_*`,
  `environ_*`, `sched_yield`), implemented natively per-session. New
  imports are an explicit ABI decision, not an accident.
- **Arch-blind core.** `core/` has zero `#ifdef`/inline asm; all machine
  contact is in `arch/<target>/` shims (~1–2k LOC per port). No per-arch
  page tables: flat/identity mapping everywhere, the engine isolates.

The full binding contract — port imports, block/net/input/timer window
layouts, kernel-owned service endpoints, capability bits — is **frozen in
[`abi/ABI.md`](abi/ABI.md) v1** (currently `v1, FROZEN`; any extension
ships as a version bump there before code). Read it before writing any
service.

## Fleet & lanes

Development runs as parallel lanes over one repo; each owns disjoint
paths and commits per subtree:

| lane | scope | status |
|---|---|---|
| kernel | `core/`, `arch/`, `third_party/wasm3`, `kernel/` | Phases 0–4 gates green |
| services | `services/`, `guests/`, `abi/` | console + login shipped; fs next |
| tools | `tools/`, `README.md` | `img` image builder, `hvtest` hypervisor matrix harness |

Cross-lane contract discipline: everything above the substrate codes only
against `abi/ABI.md`; anything below never parses guest-visible formats.

## Quickstart

Prerequisites: `gcc`/`g++`, `python3`, Go ≥ 1.21, `rustup` with target
`wasm32v1-none`, `wat2wasm` under `~/.local/wabt/bin`, and QEMU + OVMF
under `~/.local/osdev-root` (see `MEMORY.md` "Environment").

```sh
make image        # build/BOOTX64.EFI + build/disk.img (mtools flow)
make run          # boot it headless-ish on serial stdio
make test         # headless QEMU gate: KERNEL-OK + out 0x28 on serial.log
```

Per-phase guest/service gates:

```sh
make test-g1      # C guest  -> 'hello from C'   via fd_write
make test-g2      # Rust     -> 'hello from Rust'
make test-g3      # stock Go -> 'hello from Go'    (GOOS=wasip1)
make test-p4      # ports: ping-pong, registry LIST, console kill isolation
make test-all     # everything
```

Each gate boots its own disk image so payloads can never go stale, runs
QEMU headless for ≤120 s, strips ANSI escapes from `build/serial.log`,
and greps for the phase's marker strings.

## Repo layout

| path | what |
|---|---|
| `core/` | arch-blind kernel: mm, scheduler, capability registry, ports, wasm3 glue, mini-WASI |
| `arch/x86_64/` | boot.S, uart, traps, timer — the only machine-aware code |
| `third_party/wasm3/` | vendored wasm3 engine (MIT), freestanding shims |
| `services/` | `.wasm` servers: console, login (Go sources) |
| `guests/` | guest ladder: hello.wat / .rs / .go |
| `tools/img/` | this lane's release tool: builds disk images end-to-end |
| `tools/hvtest/` | headless hypervisor test matrix (QEMU/VirtualBox/VMware, identical gates) |
| `tools/vasm/` | assembler for the retired 8-opcode ISA (Phase-5 cleanup) |
| `abi/ABI.md` | frozen guest-facing interface contracts (v1) |

## tools/img — disk images without mtools

`tools/img` reproduces the Makefile's mtools layout byte-for-byte where it
matters (superfloppy FAT16, no MBR; 2-sector clusters and 255-sector FATs
on a 64 MiB image, verified against `minfo`; long-file-name entries for
names like `console.wasm` or `init.conf`):

```sh
cd tools/img && go build -o img .

img -o build/disk.img \
    -efi build/BOOTX64.EFI \           # → /EFI/BOOT/BOOTX64.EFI (required)
    -app  build/hello3.wasm \          # → /vm/app (guest payload)
    -modules services/out \            # tree → /boot/modules/*
    -seed   tools/img/templates/etc \  # tree → /etc/*
    -size 64M [-label OSDEV]
```

Hypervisor-matrix helpers (Phase 12 prep), from the same raw image:

```sh
img ... -vmdk build/disk.vmdk   # VMware monolithicFlat descriptor +-flat.vmdk
img ... -vdi  build/disk.vdi    # VirtualBox fixed VDI
```

Both outputs are locked by golden-file tests (`UPDATE_GOLDEN=1 go test`
regenerates); CIDs/UUIDs are content-derived so builds are reproducible.
To assert the same boot gates across hypervisors, use
[`tools/hvtest`](tools/hvtest) — see its README for prerequisites.

Implementation notes: pure Go stdlib; the FAT16 builder writes through a
tiny `BlockDevice` interface so every code path is tested against an
in-memory device (`go test ./tools/img`); an independent read-back parser
re-parses BPB/FAT/LFN chains in tests; when local mtools binaries exist, a
parity test runs real `mdir`/`mtype` against generated images. Images are
deterministic given identical inputs (content-derived volume serial).

## Writing a Go service module

Services are plain `main` packages built for wasip1. Talk to the system
through the frozen ABI: WASI for basics, `kern_*` port imports for
everything else. The worked example below is the real
[`services/console/main.go`](services/console/main.go) — the same recipe
is what `services/fs` will follow when it lands (Phase 5).

**1. Bind a well-known name** (§1: one owner per name, many binders):

```go
//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

h := port_create(&cstr("console")[0], 7)
if h < 0 { // already owned (e.g. respawn): attach instead
	h = port_bind(&cstr("console")[0], 7)
}
```

**2. Poll, never block** (`recv` returns 0 when empty; yield between
polls so round-robin scheduling stays fair):

```go
//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

//go:wasmimport kernel kern_port_recv
func port_recv(h int32, buf *byte, cap uint32) int32

for {
	if n := port_recv(h, &buf[0], uint32(len(buf))); n > 0 {
		os.Stdout.Write(buf[:n]) // console relays to its window (§2)
	}
	sched_yield()
}
```

**3. Build & ship:**

```sh
cd services/console
GOOS=wasip1 GOARCH=wasm go build -o console.wasm main.go
img -o build/disk.img -efi build/BOOTX64.EFI \
    -app build/guest.bin -modules services/out -seed tools/img/templates/etc
# reboot: /boot/modules/*.wasm preloads until fs.wasm exists;
# from Phase 7, init.conf lists <name> <path> <capmask-hex> instead.
```

Rules of thumb: never block (`recv` returns 0 when empty — yield and
retry); never touch hardware (that's shim territory); expect to be killed
and respawned at any time (console is the crash-isolation target of the
Phase-4 gate); embed your `abi_ver` custom section once Phase 5 lands
(the kernel refuses mismatched modules). For file service specifically,
fs.wasm speaks §3 block-window requests over its RAM-disk backend — see
ABI.md §3 for the exact mailbox layout it must implement.

Verify your image boots under every available hypervisor with identical
gates: `cd tools/hvtest && hvtest -img build/disk.img -gates 'KERNEL-OK' all`.

## Status

Phases 0–4 gates green (native boot → C++ substrate de-Go → wasm3 engine +
C/Rust/Go guests → message ports + crash isolation). Tools lane: `img`
replaces the mtools pipeline with golden-locked VMDK/VDI converters;
`hvtest` readies the Phase-12 hypervisor matrix. Phase plan and binding
decisions live in `AGENTS.md`; state and gotchas in `MEMORY.md`.
