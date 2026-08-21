# AGENTS.md — kernel project guide

Read this before working. Pair with `MEMORY.md` (state + gotchas).
Coding sessions follow the phase plan below **in order**; do not skip gates.

## What this project is

A **freestanding C++20 microkernel** (GNU as, UEFI boot) whose guest ABI is
**wasm + a frozen mini-WASI profile**. All OS services are `.wasm` modules,
written preferably in **Go** (`GOOS=wasip1`, stock unpatched toolchain).
Deliberately **non-POSIX**: capabilities, sessions, message ports — no
fork/exec/signals/global namespace. Endgame: system programming in Go on a
capability microkernel portable by construction to x86_64 / aarch64 / riscv64.

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

\* Raw hardware access stays in tiny native device-window shims; wasm drivers
hold policy only. No wasm code ever touches hardware directly.

## Autonomy mandate (user preference — binding)

Work the phase plan **continuously and unabated**. Do not pause to ask
permission, confirm choices, or summarize between steps: every design
decision worth preserving is already recorded here and in MEMORY.md.
Make implementation decisions yourself and proceed. Stop ONLY when:
a phase gate fails twice on the same root cause, an action would reach
outside the repo/`~/.local` scope in a destructive way, or the session
ends. Never re-litigate settled decisions — if code reality diverges
from the docs, fix the divergence and note it in MEMORY.md.

## Hard engineering rules

1. **`core/` is arch-blind**: zero `#ifdef`, zero inline asm, zero direct HW
   access. All machine contact via `arch/<target>/` shims:
   `boot.S · uart · timer · traps · bootinfo` (~1–2k LOC per port).
2. **No per-arch page tables**: run flat/identity everywhere (x86-64 identity
   map; arm64 MMU off; riscv64 bare M-mode). Engine provides isolation.
3. **Substrate freeze discipline**: kernel/*.cpp does mechanism only
   (schedule, isolate, expose windows). If it grows beyond boot+mm+scheduler+
   engine+WASI glue, the design is rotting — push logic into .wasm servers.
4. **Frozen WASI profile**: preview1 subset only — `fd_write proc_exit
   clock_time_get random_get args_get args_sizes_get environ_* sched_yield`
   (+ `fd_read` when needed). New imports require explicit decision.
5. **Guest ABI stability**: `.wasm` modules must never need recompilation
   across kernel versions or architectures.
6. Language split: substrate C++20/GNU as (freestanding, `-fno-exceptions
   -fno-rtti -fno-threadsafe-statics`); services any language via wasm;
   host/build tools may be Go. No Plan 9 asm anywhere (retired mandate).

## Phase plan (each phase ends with its gate green)

### Phase 0 — Safety net (first action of next session)
- `git init`; `.gitignore`: `build/`; commit tree **verbatim** (pre-surgery
  snapshot preserves gokernel/, Plan 9 asm, 8-opcode ISA forever).

### Phase 1 — Native boot, no Go
- `kernel/main.c`: delete Go marker/handoff (`GO_MAGIC_*`, `go_entry_fn`);
  after gdt/idt/mm/paging call `kmain(&g_bi)` directly.
- Makefile: build+link `kernel/vm/vm.o kernel/vm/vector.o kmain.o`;
  remove `kernel.elf`/`goaddr.mk` from image deps.
- `scripts/mkpefi.py`: shim-only mode (no `.gokern` section, no markers).
- **Debug loader regression first**: last log shows `prog=0x0 len=0x0` and NO
  `[loader]` trace lines — check whether log was stale vs `LocateProtocol`
  failure path (`loader.c`). Verify disk.img actually holds `/vm/prog.vbin`.
- **Gate**: `make test` → `KERNEL-OK` + `out 0x28` + `'E'` on serial.

### Phase 2 — C++ substrate, de-Go/de-Plan9
- Convert `kernel/*.c` → `.cpp` progressively; CXXFLAGS freestanding set.
- Replace `tools/goshim` (Plan 9 IDT bank) with GNU as `.S` vectors.
- Delete: `gokernel/`, `scripts/goaddr.sh`, all Go/GOROOT_BARE make rules,
  `tools/goshim/`.
- Shrink `boot.h`: drop `free_base free_end tsc_khz` (Go-heap relics); keep
  mmap/prog fields.
- Introduce the `core/` ↔ `arch/x86_64/` split NOW (rule 1) even if only
  x86_64 exists.
- **Gate**: `make test` green again.

### Phase 3 — wasm engine + mini-WASI + guests
- Vendor wasm3 (MIT) → `third_party/wasm3`; strip platform layer; provide
  freestanding shims: `memcpy/memset/memmove/realloc` over mm pool.
- Implement frozen WASI profile: `fd_write`→console window/serial,
  `proc_exit`→session end, `clock_time_get`→timer shim, `random_get`→PRNG,
  `args_*`/`environ_*`→static tables, `sched_yield`→cooperative yield.
- Session scheduler skeleton (round-robin; single-session suffices here).
- Guest ladder, one at a time, each printing to serial through the kernel:
  1. **C → wasm** hello world (smallest; validates engine core)
  2. **Rust → wasm32-wasip1**
  3. **stock Go → GOOS=wasip1 GOARCH=wasm** (the anti-nightmare proof:
     unpatched toolchain)
- Toolchain check EARLY (see below); network works, installs to `~/.local`.
- **Gate**: all three guests print to serial through the kernel.

### Phase 4 — Ports + server isolation
- Add message-port imports: create/send/recv, kernel-mediated copy,
  capability-guarded. (~200 lines; keep minimal.)
- Two servers: `console.wasm`, `login.wasm` (stub auth fine).
- **Gate**: kill console.wasm → login.wasm + kernel keep running
  (crash-isolation demo).

### Phase 5 — FS + multiuser
- `fs.wasm` in Go over block-device window capability (FAT first).
- Decide client routing: kernel-routed preview1 `path_open`/`fd_read` vs
  client-lib speaking ports directly (document choice in MEMORY.md).
- Multiuser = login.wasm issuing per-user capability sets. Policy layer only.
- Retire 8-opcode artifacts if not already: `kernel/vm/ tools/vasm
  programs/demo.*` (preserved in git history).

### Phase 6 (optional, later) — Architecture ports
- `arch/aarch64/` then `arch/riscv64/` shells; `core/` unchanged.
- QEMU: `qemu-system-aarch64`/`riscv64` static builds + OVMF-arm64/OpenSBI
  into `~/.local` (no root needed). Same headless test pattern.

## Phase-3 toolchain checklist (verify before relying)

- clang with wasm32-wasip1/wasi-sdk OR fallback: hand-written `.wat` +
  `wat2wasm` (wabt static tarball → `~/.local`) for guest #1.
- rustup target wasm32-wasip1.
- System `go` version ≥ 1.21 for wasip1. **Stock go only** — `$GOROOT_BARE`
  is dead; never reference it.

## Verification protocol

- `make test`: headless QEMU, 120 s timeout, assert expected strings on
  serial (per-phase gates above); strip ANSI before grepping.
- After ANY change: rebuild clean if kernel sources changed — stale objects
  have caused silent wrong-image bugs before (see MEMORY.md).
- Never trust a boot log older than the binary that produced it.

## Target repo map (end state)

```
arch/x86_64/            boot.S uart timer traps bootinfo (+aarch64,riscv64 later)
core/                   main.cc kmain.cc mm sched capreg ports wasi_glue
third_party/wasm3/      adapted engine (MIT)
services/               console.wasm login.wasm fs.wasm (sources: go/rust/c)
guests/                 hello.c hello.rs hello.go → .wasm
tools/img               host-side image builder (go, replaces mkpefi glue later)
kernel/link.ld scripts/mkpefi.py Makefile AGENTS.md MEMORY.md README.md
```
