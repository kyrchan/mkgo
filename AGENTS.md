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
hold policy only. No wasm code ever touches hardware directly. Driver model
and new-hardware recipe: `abi/ABI.md` §8.

## Autonomy mandate (user preference — binding)

Work the phase plan **continuously and unabated**. Do not pause to ask
permission, confirm choices, or summarize between steps: every design
decision worth preserving is already recorded here and in MEMORY.md.
Make implementation decisions yourself and proceed. Stop ONLY when:
a phase gate fails twice on the same root cause, an action would reach
outside the repo/`~/.local` scope in a destructive way, or the session
ends. Never re-litigate settled decisions — if code reality diverges
from the docs, fix the divergence and note it in MEMORY.md.
**Never end your turn while productive work remains.** If blocked >10 min
on one obstacle, switch tracks (services/, guests/, abi/, docs) and return
later — idling is a defect. When all phase gates are green, print exactly:
ALL PHASES COMPLETE

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
7. **Guest-visible contracts live in `abi/ABI.md`** (ports, windows, input,
   timer, net). They change only via a version bump there + MEMORY.md note;
   never ad-hoc in kernel code.

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
- Implement port imports EXACTLY as `abi/ABI.md` §1 (`kern_port_create/
  bind/send/recv`, kernel-mediated copy, 4096 B cap, non-blocking recv).
- Kernel-owned service endpoints per §7: native listeners on "registry"
  (LIST/CAPS/KILL/SPAWN), "devman" (ENUM), "power" (REBOOT/OFF) — same
  datagram framing as guest ports; capability-bit checks + audit relay.
- Two servers: `console.wasm` (binds name "console", relays messages to
  console window), `login.wasm` (name "login"; stub auth: any password).
- **Gate**: (a) port ping-pong between two test sessions on serial;
  (b) kill console.wasm → login.wasm + kernel keep running (crash-isolation);
  (c) registry LIST from a test session shows both servers' sids.

### Phase 5 — FS + multiuser
- `fs.wasm` in Go over the block window of `abi/ABI.md` §3 with the
  **RAM-disk backend** (kernel allocates pages; volatile). FAT16 only
  (512 B sectors, 2-sector clusters fine for 8 MB disks).
- Client routing — DECIDED, do both:
  1. kernel-routed preview1: mini-WASI gains `path_open/fd_read/fd_write/
     fd_close/path_create*`; kernel forwards each op to "fs" over ports,
     waits, maps reply to fd table (plain WASI clients just work);
  2. `guests/lib` speaks ports directly for richer ops (stat, mkdir, ls).
  Record the fd-mapping scheme in MEMORY.md when implemented.
- Multiuser (stub): login.wasm accepts user names from a static table and
  issues per-user capability sets; fs.wasm roots each session's path
  namespace at `/home/<user>`. Enforcement lives in kernel registry.
- Multiuser FS model (FAT16 stores NO owners — namespace-rooted isolation):
  kernel stamps every forwarded FS op with `{sid, uid}` from its own
  registry (clients cannot spoof identity); fs.wasm rejects any resolved
  path escaping the caller's root (`..` beyond it). Sharing policy v1:
  `/home/<u>/**` private · `/tmp` world-writable · `/etc` readable,
  writable only with CAP_FS_ADMIN · `/boot/modules` writable only with
  CAP_SPAWN+CAP_FS_ADMIN. Finer ownership via a sidecar owners.db is
  post-v1 (documented deferral, not an omission).
- Every service module embeds `abi_ver=1` custom section at build time
  (checked by kernel per the module-update rule).
- Retire 8-opcode artifacts if not already: `kernel/vm/ tools/vasm
  programs/demo.*` (preserved in git history).
- **Gate**: create/write/read/delete `/home/u1/hello.txt` through BOTH
  routes; second session rooted at `/home/u2` cannot open u1's file
  (serial shows the denial).

### Phase 6 (optional, later) — Architecture ports
- `arch/aarch64/` then `arch/riscv64/` shells; `core/` unchanged.
- QEMU: `qemu-system-aarch64`/`riscv64` static builds + OVMF-arm64/OpenSBI
  into `~/.local` (no root needed). Same headless test pattern.

### Phase 7 — Interactive userland
- Input: dedicated `kern_input_recv` import + kernel-owned focus attribute
  (`kern_focus_set`) per `abi/ABI.md` §4 — NOT a console-window hack.
- `guests/lib`: Go package wrapping all `kern_*` imports (ports, input,
  focus) + C header mirror; this is the guest "libc". Version it with ABI.
- `services/shell` (Go): prompt loop over ports; built-ins first:
  `echo ls cat stat kill-session run`; external commands via SPAWN later.
  Admin built-ins (`devices reboot poweroff sessions caps`) arrive with the
  phases that enable their §7 ops.
- `services/init` (Go): kernel spawns ONLY init.wasm; init.conf arrives
  pre-loaded from the ESP via `args_get` (see System administration);
  init spawns console/login/fs in order from `/boot/modules/*`, applies
  kernel knobs via the registry port, hands focus to login.
- **Gate**: scripted serial input (echo -e into QEMU stdin) makes shell
  run `cat /etc/motd`; file content reaches serial via console server.

### Phase 8 — Preemption + persistent storage
- Timer window per `abi/ABI.md` §5; scheduler gains IRQ-driven preemptive
  round-robin (quantum in kernel config); keep cooperative fallback flag.
- virtio-blk native shim RE-BACKS the existing block window (§3) with the
  QEMU disk — zero guest-visible changes. mtools-built image stays as seed
  image (documented decision).
- **Gate**: (a) persistence: write file → reset QEMU → read back;
  (b) no-starvation: busy-loop session cannot block second session
  (serial shows interleaved progress lines).

### Phase 9 — Network stack in Go (flagship showcase)
- virtio-net native shim exposing RX/TX packet windows per §6; shim owns
  real virtio rings, guests see only slots. No interrupts yet (polled).
- `services/net` (Go): ARP → IPv4/ICMP → UDP → TCP, in that order, each
  step serial-logged. Socket API via ports ("net" well-known name).
- Test rig: QEMU user-net (`-net user`); host-side listeners against
  10.0.2.2 port-forwards.
- **Gate**: (a) host `nc -u` echo round-trip through net.wasm;
  (b) net.wasm HTTP GET fetches from a host-side python http.server and
  prints status line on serial.

### Phase 10 — Multiuser hardening + release engineering
- Real login: `/etc/users` file on fs.wasm (`name:salted-hash:capmask` per
  ABI §7); per-user capability sets issued at login, enforced by kernel
  registry; `sessions`/`caps <sid>` shell tools dump registry state for
  auditing (lists session→caps).
- `tools/img` (Go): builds disk images end-to-end, retiring mtools and
  remaining mkpefi glue from the Makefile path.
- `README.md`: written this phase — architecture diagram, quickstart
  (`make image && make run`), how to build/ship a Go service module.
- Test matrix: every `test-pN` green under both KVM and TCG.
- **Gate**: two users logged in concurrently; negative tests prove u2
  cannot open u1's files, send to u1's private ports, or inherit caps.

Phase order/priority: 7 → 8 → 9 (stretch; drop if week ends) → 10.
Completion definition: **ALL PHASES COMPLETE means every phase gate in this
document is green** (Phases 0–10 as numbered above; Phase 6 stays optional).

### Phase 11 (future, not this cycle) — Wireless + USB
Prerequisite reality check: mainstream WiFi/BT dongles are USB devices;
real WiFi cards need per-vendor firmware shims (breaks §8 budget — only
"offload module" style adapters fit the model). Order of attack:
1. **USB-HC class (§9 id 8)**: xHCI shim, transfer-request mailbox window.
   Biggest single shim in the project; budget its own phase.
2. **Bluetooth**: HCI-over-UART (H4) shim reusing serial plumbing — tiny.
   `services/bt` (Go): L2CAP/ATT/GATT above it. QEMU test rig: guest UART
   on a pty ↔ host BlueZ talks H4 to the guest stack. No USB needed for v1!
3. **WiFi**: offload-module dongles only (e.g. ESP-Hosted-style framed
   Ethernet over SPI/UART); `services/wlan` (Go) does scan/assoc/DHCP
   above a WLAN window bridged into net.wasm's packet path.
- **Gate sketch (when scheduled)**: bt.wasm pairs with host BlueZ over pty
  and answers an ATT read; wlan.wasm associates with an offload module and
  passes one UDP datagram end-to-end.

### Phase 12 (future) — Hypervisor matrix: VirtualBox & VMware
All three hypervisors boot UEFI, so the boot path is unchanged. Every
device below is a NEW BACKEND behind an existing class window (§8 rule:
same window semantics, devman ENUM unchanged — guests cannot tell).
Priority order driven by "which adapter works on the most hypervisors":

1. **PS/2 keyboard shim** (`i8042`, ~150 LOC): scancode set 2 → §4 input
   records. Works on QEMU/VBox/VMware AND bare metal; unlocks real input
   beyond serial.
2. **E1000 net backend** (~600–900 LOC: EEPROM MAC read, tx/rx descriptor
   rings, moderate): runs on VMware + VBox + QEMU (`-device e1000`) —
   becomes the universal net fallback when virtio is absent.
3. **AHCI block backend** (~800–1200 LOC: port reset, command-list/FIS
   construction, polled completion): VBox + VMware SATA storage; also the
   path to real hardware disks later. Re-backs block window (§3), like
   virtio-blk did.
4. **Framebuffer backends** (post-v1, feeds compositor candidate):
   Bochs DISPI LFB (~120 LOC; VBox + QEMU `-device bochs-display`) and
   VMware SVGA II FIFO (~300–500 LOC). One new class window layout
   required in `abi/ABI.md` v2 (§9 id 9 FRAMEBUFFER).
5. **VMware backdoor shim** (~60 LOC, optional delight): I/O port 0x5658
   RPC — host time sync, hostname/uuid info, log channel. Register as a
   tiny console-adjacent info source, not a device class.

Test recipes (headless, same grep-the-serial pattern as make test):
- VBox: create VM with EFI enabled + serial pipe
  (`--uart1 0x3F8 4 --uartmode1 server /tmp/vbox-ser`); host test script
  connects to socket, asserts gate strings. Disk formats: VDI via
  `VBoxManage convertfromraw` from our raw disk.img.
- VMware: `.vmx` with `firmware="efi"`, `serial0.fileType="file"`,
  `serial0.fileName="vmware-serial.log"`; run headless via `vmrun start
  <vmx> nogui`; same gate greps on the serial file. Raw disk = flat VMDK
  descriptor over our image (tools/img grows a writer).
- **Gate sketch**: one shared `make test-hv` that boots the SAME disk.img
  under all three hypervisors and asserts identical Phase-7 gate strings;
  devman ENUM output printed on serial proves which backends attached.

### Parallel track — services/ (optional, start early)- `services/` Go packages (FS logic, login/auth logic) are ordinary
  host-testable Go (`go test`, no wasm needed initially) and have **no hard
  dependency on kernel phases**. Develop them in parallel whenever the main
  line is blocked on a gate, QEMU iteration, or toolchain download.
- Prerequisite before wiring: interface contracts are FROZEN in `abi/ABI.md`
  v1 — both sides code against it only.
- Wrap to `.wasm` (`GOOS=wasip1`) once Phase 3 gate is green; validate under
  any stock wasm runtime with stub imports first; drop into the kernel
  unchanged (guest ABI stability rule).
- Two-agent split (if used): agent A scoped to core/arch/third_party,
  agent B to services/guests/abi only; disjoint paths, commits per subtree.
- FLEET MODE (active since Phase 4 green, provider-unlimited week):
  - Lane MAINLINE — main tree @ master; scope core/, arch/, third_party/,
    kernel/, Makefile; drives phase plan 5→7→8→9→10; supervised by
    scripts/watchdog.sh (+cron-guard).
  - Lane SERVICES — worktree ../kernel-lane-services @ branch lane/services;
    scope services/ + guests/ ONLY. Builds console/login/fs/shell/init as
    host-tested Go → wasip1 wraps; guest libc in guests/lib.
  - Lane TOOLS — worktree ../kernel-lane-tools @ branch lane/tools;
    scope tools/ + README.md ONLY. Delivers tools/img, README, image-format
    writers for Phase 12.
  - Lane VERIFY — worktree ../kernel-lane-verify @ branch lane/verify.
    QA department: read-only everywhere; writes verify/FINDINGS.txt +
    verify/QUALITY.txt in its own tree only. Reviews every commit delta
    across all repos: races, memory bugs, unused code, stubs, ABI
    deviations, capability-check gaps. Severity rule: ABI violation or
    capability-check gap = BLOCKER. Style nits not reported.
  - Lane DOCS — worktree ../kernel-lane-docs @ branch lane/docs.
    Publications department: IBM-style formal docs as PLAIN TEXT under
    docs/*.txt (SDD, IFSPEC, GLOSSARY, TRACE matrix, TESTPLAN, RELHIST)
    with document control blocks and revision history per change.
  - Supervision: scripts/fleet.sh + scripts/lanes.conf per-lane (precise
    child-PID kills, 900 s two-strike stalls); cron-guard revives watchdog
    AND fleet. Lanes signal done via .overnight-complete marker only.
  - Merging: lanes commit to their own branches (disjoint paths ⇒ trivial
    merges); merge to master when a phase gate consumes lane output.
  - ABI changes remain forbidden mid-week; proposals go to
    services/ABI-NOTES.md for human review.

## System administration & configuration

- **Admin identity**: user `admin` (static table Phase 5, hashed Phase 10)
  holding every capability bit (`abi/ABI.md` §7). There is no su/sudo:
  capability sets are granted ONLY at login.
- **Config lives on fs.wasm** under `/etc`:
  - `/etc/users` — `name:salted-hash:capmask` (Phase 10)
  - `/etc/motd` — login banner (Phase 7's demo file for `cat`)
  - `/etc/init.conf` — one server per line: `<name> <path> <capmask-hex>`
  - `/etc/kernel.conf` — `key=value` knobs (quantum, log level); applied by
    init via the registry port, never parsed by the kernel itself
- **Boot orchestration**: the kernel spawns exactly ONE session —
  `init.wasm`. RESOLVED chicken-and-egg: init cannot read /etc from fs
  before fs exists, so the C loader fetches `\etc\init.conf` from the ESP
  at boot (same mechanism as `\vm\prog.vbin`) and passes it to init via
  `args_get`. init spawns console/fs/login/net from `/boot/modules/*`
  (ESP paths, also passed in), then hands focus to login. Everything
  AFTER fs is up (respawn policy changes, kernel.conf application) uses
  fs.wasm normally. Servers stop being kernel-hardcoded in Phase 7.
- **Supervision**: init stays resident and monitors its children via
  registry LIST; a dead server is respawned per the init.conf `respawn`
  column (yes|no, default yes for console/fs/login). Crash isolation is
  the kernel's job; restart-to-known-state is init's.
- **Program launch**: shells run programs via registry SPAWN op
  (`run <module> [args...]`), never fork/exec. CAP_SPAWN is admin-only by
  default; grant it to users deliberately.
- **Admin tools are shell built-ins** speaking ABI §7 ports:
  `sessions`, `caps <sid>`, `kill-session`, `devices`, `reboot`, `poweroff`.
- **Logging**: services send `[lvl] message` datagrams to "console";
  console prefixes its own tag. File logging (`/var/log`) is post-v1.
- **Module updates**: overwrite `/boot/modules/<name>.wasm`, reboot. The
  kernel refuses any module whose embedded ABI version ≠ its own (custom
  section `abi_ver` checked at instantiation).

## Scheduler policy (binding)

Round-robin, forever. Evolution path:
1. Phase 3: cooperative RR (yield/proc_exit points), single-session fine.
2. Phase 8: IRQ-preemptive RR; `quantum_ms` in `/etc/kernel.conf` applied
   via registry port; cooperative fallback flag retained.
3. Post-v1 (only if needed, ABI-neutral): head-of-line bump — sessions
   with pending port messages get next-turn priority (~20 lines). This is
   the ONLY sanctioned refinement.

Explicitly rejected regardless of future justification: priority levels,
MLFQ/CFS-style fairness, real-time classes, affinity controls. Reasons:
two-level scheduling exists anyway (Go runtime schedules within sessions),
workload is I/O-shaped servers, and substrate-freeze rule 3. Scheduler
total budget: ≤400 lines including queues and timer hookup.

## OS server inventory

| server | phase | role |
|---|---|---|
| init.wasm | 7 | boot order, supervision/respawn, kernel.conf |
| console.wasm | 4 | output relay + log tags + audit sink display |
| login.wasm | 4/10 | auth → per-user capability sets → focus |
| fs.wasm | 5/8 | FAT16 over block window; /etc, /home/<u>, /boot/modules |
| shell.wasm | 7 | built-ins + `run` via SPAWN; admin built-ins per §7 |
| net.wasm | 9 | ARP/IP/ICMP/UDP/TCP over packet windows; "net" port |
| bt/wlan.wasm | 11 | wireless stacks above reserved classes |

Post-v1 candidates: framebuffer/compositor (§8 class 9), syslog file sink,
package installer with signature checks, per-session memory quotas.

## Phase-3 toolchain checklist (verify before relying)

- clang with wasm32-wasip1/wasi-sdk OR fallback: hand-written `.wat` +
  `wat2wasm` (wabt static tarball → `~/.local`) for guest #1.
- rustup target wasm32-wasip1.
- System `go` version ≥ 1.21 for wasip1. **Stock go only** — `$GOROOT_BARE`
  is dead; never reference it.

## Verification protocol

- `make test`: headless QEMU, 120 s timeout, assert expected strings on
  serial (per-phase gates above); strip ANSI before grepping.
- Kernel fault policy: any substrate fault prints `KERNEL-PANIC <pc> <err>`
  on serial then halts — never a silent hang; the QEMU timeout treats
  absent output as failure. Guest faults only ever kill that session.
- After ANY change: rebuild clean if kernel sources changed — stale objects
  have caused silent wrong-image bugs before (see MEMORY.md).
- Never trust a boot log older than the binary that produced it.
- **QA intake (binding)**: before committing, read
  `/home/cyr/kernel-lane-verify/verify/FINDINGS.txt`. Every finding marked
  against your repo at severity BLOCKER or MAJOR must be fixed (or answered
  with a written rebuttal in MEMORY.md) BEFORE the next phase-gate commit.
  MINOR/NOTE items go into MEMORY.md backlog.

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
