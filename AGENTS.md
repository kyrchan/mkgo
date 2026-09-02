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
      mini-WASI · mm · VFIO (Phase 11) ·
      native device shims (tiny, frozen: PS/2/PIC/UART)
hardware
```

\* Raw hardware access stays in tiny native device-window shims (legacy
ISA/LPC: PS/2, PIT, PIC, UART) or VFIO passthrough (PCIe: GPU, NIC, storage,
USB, etc.). All driver POLICY runs in `.wasm` — preferably Go. No wasm code
ever touches hardware directly. Driver model and new-hardware recipe:
`abi/ABI.md` §8.

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
   engine+WASI glue+VFIO, the design is rotting — push logic into .wasm
   servers. VFIO is the LAST sanctioned kernel growth area (Phase 11); after
   it lands, all new drivers are pure Go→wasm with zero kernel code.
4. **Frozen WASI profile**: preview1 subset only — `fd_write proc_exit
   clock_time_get random_get args_get args_sizes_get environ_* sched_yield`
   (+ `fd_read` when needed). New imports require explicit decision.
   The kernel-routed preview1 file layer (path_open/fd_read/fd_write/
   fd_close, ~584 LOC) is RETAINED as a v2 convenience — enables stock
   Go `os.Open` cross-platform development. Not part of the frozen profile.
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
  **Landed 2026-09-02 (commit 92313c5).** The design is
  *cooperative-under-interrupt*: the IRQ0 stub runs on the GUEST's
  stack, saves 16 GPRs, captures the running sid, sets `preempt_pending`,
  EOI, iretqs back. The actual switch happens at the session's next
  `sched_yield_current`. See "Scheduler policy" below for why this is
  NOT a true preemptive context switch.
- virtio-blk native shim RE-BACKS the existing block window (§3) with the
  QEMU disk — zero guest-visible changes. mtools-built image stays as seed
  image (documented decision).
- **Gate**: (a) persistence: write file → reset QEMU → read back;
  (b) no-starvation: busy-loop session cannot block second session
  (serial shows interleaved progress lines).

### Phase 8.1 — Substrate hardening (SMP-portability, landed 2026-09-02)
- Locking primitives: `core/arch_lock.h` (arch-blind interface) +
  `arch/x86_64/lock.cc` (ticket spinlock via LOCK XADD + sfence;
  `arch_irq_state_t` via pushf/cli/popf). Host impl in
  `arch/x86_64/lock_host.cc` uses C11 `__atomic` builtins + no-op
  IRQ primitives (the kernel's `lock.cc` would segfault on `cli` in
  userspace). Commits f20ed90 (API), f8a0b33 (IRQ-save discipline on
  port ring + UART write), d873672 (arch_spinlock on the port
  message-ring enqueue path).
- Ordering convention: `arch_irq_save()` FIRST, then
  `arch_spinlock_acquire()`. Acquire-then-IRQ would deadlock if the
  IRQ tried to take the same lock.
- **Gate**: hosttest 53 → 63 → 68 → 76 (+T13 lock API, +T14
  irq-save discipline, +T15 port-ring lock); integration gates
  g1 g2 g3 p4 p5a p5b p7 p8a p9 p10 p11 p11b p12 p13 all PASS.

### Phase 8.2 — AP-core bring-up (planned, not implemented)
- Bring up N AP cores via MADT/MP table + SIPI. Each AP runs its own
  cooperative-under-interrupt scheduler over its own session pool.
  No session migrates between cores.
- Per-core `cur` pointer replaces the single global in `core/sched.cc`.
  Cross-core shared state (port ring, mm pool) is safe via the spinlock
  API from Phase 8.1.
- All cores share the same identity PML4 set up by `paging_init` for
  CPU0 -- rule #2 (no per-arch page tables) is satisfied.
- **Why this is safe without a true preemptive switch**: the Go runtime
  *is* the preemption mechanism. Go 1.14+ yields cooperatively in wasm
  at every goroutine switch point; the kernel switches sessions at
  those yield points. Multiple cores provide the parallelism, the Go
  runtime provides the per-core preemption -- neither requires touching
  the opaque interpreter state in `m3_exec.c`.
- **Gate**: boot with `-smp 4`, assert `[ap] cpu0..cpu3 booted` on
  serial, assert two sessions run on different cores concurrently.

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
- Real login: `/etc/users` file on fs.wasm (`name:uid:salted-hash:capmask` per
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

Phase order/priority: 0→1→2→3→4→5→6(opt)→7→8→9→10→11 ✅ 12 ✅ 13 ✅ → 14 → 15 → 16 → 17 → 18 → 19.
Completion definition: **ALL PHASES COMPLETE means every phase gate in this
document is green** (Phases 0–19 as numbered above; Phase 6 stays optional).
Phases 14–19 are the userland-expansion arc on top of the completed 0–13
foundation; each is green only when its gate passes under `make test`.

### Phase 11 ✅ — VFIO foundation + framebuffer (complete, gated)
**The true microkernel endgame begins.** VFIO (Virtual Function I/O) is a
generic device-passthrough foundation: the kernel provides IOMMU management,
region mapping, and interrupt routing — all future PCIe drivers reuse it with
**zero new kernel code**. Drivers become pure Go→wasm.

**VFIO foundation** (~2,000 LOC kernel, one-time investment):
- IOMMU driver: program VT-d/AMD-Vi page tables, TLB invalidation, DMA
  restriction to assigned pages (security boundary — compromised guest cannot
  DMA outside its scope)
- Container/group/device management: PCI enumeration, domain assignment
- Region mapping: `kern_vfio_map_region(bar_index)` → maps PCI BAR into guest
  linear memory with IOMMU-protected read/write/prot bits
- Interrupt routing: MSI/INTX → guest doorbell via eventfd
- Once this exists, every future PCIe device (GPU, NIC, storage, USB, etc.)
  reuses the same infrastructure — drivers are pure Go→wasm

**Framebuffer-only policy** (~500 LOC kernel):
- Map only the Linear Framebuffer (LFB) BAR into guest memory (RW, cached)
- Modesetting/cursor ports: `kern_fb_set_mode(w,h,bp,stride)`,
  `kern_fb_set_cursor(x,y)` — slow path, kernel-controlled
- Guest writes pixels directly to mapped LFB — **zero-copy, zero overhead**
- VSYNC interrupt → guest doorbell for page-flip timing

**Go graphics driver** (~3,000 LOC, `services/graphics` → `graphics.wasm`):
- Software compositor writing to mapped LFB
- Window management, clipping, alpha blending
- Modesetting via kernel import on resolution change

**Legacy devices NOT covered by VFIO** (~250 LOC frozen):
- PS/2 keyboard (port I/O 0x60/0x64), PIT timer (0x40-0x43), PIC (0x20/0xA0),
  UART COM1 (0x3F8), PCI config (0xCF8/0xCFC) — ISA/LPC, no BARs/DMA/IOMMU.
  Keep tiny shims or use VT-x I/O bitmap passthrough.

**Gate**: display `cat /etc/motd` output through graphics.wasm to a visible
console window (framebuffer writes reach the screen).

### Phase 12 ✅ — Wireless + USB (all via VFIO)
Prerequisite: Phase 11 VFIO foundation. All devices use VFIO passthrough —
**no per-device kernel shims**. Drivers are pure Go→wasm.

1. **USB xHCI controller** (VFIO passthrough): `services/usb` (Go) implements
   the full xHCI spec — transfer rings, doorbells, port management — over
   mapped PCI BARs. No kernel xHCI shim.
2. **Bluetooth**: HCI-over-UART (H4) on the legacy UART shim (Phase 11) — tiny.
   `services/bt` (Go): L2CAP/ATT/GATT above it. QEMU test rig: guest UART on
   a pty ↔ host BlueZ talks H4 to the guest stack.
3. **WiFi**: offload-module dongles only (e.g. ESP-Hosted-style framed
   Ethernet over SPI/UART); `services/wlan` (Go) does scan/assoc/DHCP above
   a WLAN window bridged into net.wasm's packet path.

**Gate**: usb.wasm enumerates devices through VFIO-mapped xHCI; bt.wasm pairs
with host BlueZ over pty and answers an ATT read; wlan.wasm associates with
an offload module and passes one UDP datagram end-to-end.

### Phase 13 ✅ — Hypervisor matrix: VirtualBox & VMware
All three hypervisors boot UEFI, so the boot path is unchanged. VFIO
foundation (Phase 11) handles all PCIe devices generically. This phase adds
only **legacy/non-PCIe adapters** and hypervisor-specific backends.

Priority order driven by "which adapter works on the most hypervisors":

1. **PS/2 keyboard** (`i8042`, ~150 LOC legacy shim): scancode set 2 → §4
   input records. Works on QEMU/VBox/VMware AND bare metal. ISA device —
   cannot use VFIO (no BARs/DMA).
2. **E1000 net backend** (VFIO passthrough, Go driver): `services/e1000`
   (Go) implements EEPROM MAC read, tx/rx descriptor rings over VFIO-mapped
   BARs. Runs on VMware + VBox + QEMU (`-device e1000`). No kernel shim.
3. **AHCI block backend** (VFIO passthrough, Go driver): `services/ahci`
   (Go) implements port reset, command-list/FIS construction, polled
   completion over VFIO-mapped BARs. VBox + VMware SATA storage. No kernel
   shim.
4. **VMware backdoor** (~60 LOC, optional): I/O port 0x5658 RPC — host time
   sync, hostname/uuid info, log channel. Register as a tiny console-adjacent
   info source, not a device class.

**Framebuffer is NOT here** — superseded by Phase 11 VFIO foundation. Bochs
DISPI LFB and VMware SVGA II FIFO shims are unnecessary; generic VFIO
passthrough handles both, plus any future GPU.

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

### Phase 14 — Shell userland core (built-ins, non-POSIX)
The daily-use tools, all as **shell built-ins** (no SPAWN, no fork/exec —
in-process over `fs.wasm` + frozen WASI + timer). Logic is host-testable
Go first (`go test` in services/shell), then wired as built-ins.

- File ops: `cp`, `mv`, `rmdir` (completes mkdir/rm/touch/stat/ls/cat).
- Text pipeline: `grep`, `find`, `head`, `tail`, `wc`, `sort`, `uniq`,
  `tr`, `cut`, `sed` — pipes are in-shell (no separate processes).
- Scripting primitives: `sleep` (timer window), `true`, `false`, `test`/`[`,
  `expr`, `seq`, `env`, `printenv` (environ_* WASI).
- Terminal/time: `clear`, `reset`, `date` (clock_time_get).
- Identity: `whoami`, `id` (caps with no arg → self).
- Non-POSIX discipline: these reuse POSIX *names* for familiarity only;
  no POSIX syscalls, no signals, no uid/gid/perms. Authority stays
  capability bits + namespace rooting.
- Regression: one `go test` per built-in's logic; a scripted serial
  pipeline in `make test`.
- **Gate**: scripted serial session runs
  `cp /etc/motd /tmp/m && grep -n kernel /tmp/m | sort | head -3` and
  `sleep 1; date` — output reaches serial.

### Phase 15 — Identity, auth & observability
Make the multiuser/admin system legible and changeable. No su/sudo —
caps granted only at login.

- `passwd`: change own row in `/etc/users` (self-only own row;
  CAP_FS_ADMIN for others); re-salt+hash; login.wasm honors new hash;
  survives reboot (fs Phase 8).
- `top`: live session monitor — refresh over registry LIST + scheduler
  state + mm-pool free pages; quantum display.
- `dmesg`/`log`: read the console audit ring buffer (v1 syslog path;
  `/var/log` file sink stays post-v1 — documented deferral).
- `memstat`: mm-pool dump (free/used pages; per-session quotas when those land).
- `audit`: filter the audit relay by sid/event — capability-check trail
  viewer (feeds Phase-10 hardening).
- Regression: `go test` for passwd hashing + audit filter.
- **Gate**: `top` shows 2 sessions live; `passwd` changes u1's password,
  login rejects the old and accepts the new; `dmesg` prints the boot audit
  trail including a recorded capability denial.

### Phase 16 — Network client userland
Turn `net.wasm` (Phase 9 stack) into shell-facing tools. Sockets are
port-mediated via the "net" well-known name, not a global BSD-socket layer.

- `ping`: ICMP echo via net.wasm.
- `nc`: interactive UDP/TCP (promote the Phase-9 host test rig onto-system).
- `http`: GET/POST client (thin wrapper over net.wasm TCP).
- `netstat`/`ipaddr`: query net.wasm for interfaces/routes (user-facing;
  devman ENUM stays admin).
- `ssh` client: outbound (ssh server already exists; add client for
  remote management from the shell).
- Regression: `go test` for each client's framing/parsing (fuzz per
  engineering practice #4).
- **Gate**: `ping 10.0.2.2` prints replies; `http get http://10.0.2.2:8000/`
  prints a status line; `nc -u` round-trips with a host listener.

### Phase 17 — Capability & port introspection (non-POSIX layer)
The distinctive tools with no POSIX analogue — make the authority and
communication model legible.

- `ports`: list well-known names (registry/devman/console/fs/net/login)
  + owning sid + pending count — "netstat for message ports".
- `sessinfo <sid>`: open fds (kernel-routed fd table), bound ports,
  pending messages, cap bits, memory — deepens `sessions`.
- `caphint <action>`: "which capability does run/reboot/pkg install/
  devices need?" — self-documenting capability map derived from ABI §7.
- `chcaps <sid> +/-<cap>`: admin grant/revoke cap bits on a live session
  (CAP_ADMIN only, audited). This is the *capability* answer to the
  POSIX chmod/chown urge — not file perms.
- Regression: `go test` for the capability-map table; audit entry on
  every chcaps.
- **Gate**: `ports` lists all well-known servers + owners; `sessinfo
  <shell-sid>` shows its fds/ports; `caphint run` names CAP_SPAWN; a
  `chcaps` grant is audited and takes effect immediately.

### Phase 18 — Package management & module integrity
Safe third-party module distribution: signature-checked install on top
of the kernel's existing `abi_ver` enforcement at instantiation.

- `pkg install <file.wasm>`: verify signature + abi_ver, copy to
  `/boot/modules`, update index. Signature scheme: ed25519 over the wasm
  + its embedded abi_ver; trusted public keys in `/etc/trusted`.
- `pkg list` / `pkg remove` / `pkg update`.
- `module verify`: inspect a .wasm's embedded abi_ver + signature
  (`addabiver` is the writer; this is the reader).
- `module sign`: host-side signing tool in `tools/`.
- Non-POSIX: install is not exec — modules run via registry SPAWN, never
  setuid/shebang. CAP_SPAWN stays admin-only by default.
- Threat model: STRIDE-lite pass over the package path (extends the
  Phase-10 threat model); fuzz the signature/manifest parsers.
- **Gate**: build+sign hello.wasm, `pkg install`, reboot, `run hello`
  runs; a tampered-signature module is rejected; a mismatched-abi_ver
  module is rejected by the kernel at instantiation.

### Phase 19 — Supervision & config control surface
Refine `init` + registry into a manageable control plane. No runlevels,
no /etc/init.d symlinks — init.conf is a flat list, supervision is
registry-LIST-driven.

- `sysctl`/`knob`: read/set `/etc/kernel.conf` keys (quantum_ms, log
  level) via the registry port — the kernel never parses the file; init
  applies it.
- `initctl`: tell init.wasm to restart/respawn a service, reload
  `/etc/init.conf`, apply kernel.conf changes.
- `checkconf`: validate `/etc/init.conf` + `/etc/users` + `/etc/trusted`
  syntax before commit — prevents boot failures.
- Extend `caps` to show cap source (login-issued vs `chcaps`-granted).
- Regression: `go test` for each config parser (fuzz per practice #4).
- **Gate**: `sysctl quantum_ms=20` applied via init→registry→scheduler
  (visible in `top`); `initctl restart fs` respawns fs.wasm without
  losing other sessions; `checkconf` rejects a malformed init.conf.

**Userland-arc discipline (binding, Phases 14–19):** these phases add
NO POSIX-isms — no fork/exec, no signals, no su/sudo, no uid/gid/perms,
no global namespace. Authority = capability bits; communication = message
ports; launch = registry SPAWN. Where a POSIX name is reused (grep, sed,
ping, nc), only the user-facing behavior is copied; the implementation is
shell built-ins or SPAWNed .wasm over ports, never a POSIX syscall surface.
Every tool's logic is host-testable Go first (`go test`), then wired; each
phase commits with its regression tests (practice #2).

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
     kernel/, Makefile; drives phase plan 5→7→8→9→10→11→12→13; supervised by
     scripts/watchdog.sh (+cron-guard).
   - Lane SERVICES — worktree ../kernel-lane-services @ branch lane/services;
     scope services/ + guests/ ONLY. Builds console/login/fs/shell/init as
     host-tested Go → wasip1 wraps; guest libc in guests/lib. Phase 11+:
     graphics/e1000/ahci/usb/bt/wlan drivers as pure Go→wasm.
   - Lane TOOLS — worktree ../kernel-lane-tools @ branch lane/tools;
     scope tools/ + README.md ONLY. Delivers tools/img, README, image-format
     writers for Phase 13.
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
  - `/etc/users` — `name:uid:salted-hash:capmask` (Phase 10)
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
2. Phase 8 (landed 2026-09-02, commit 92313c5): **cooperative-under-
   interrupt** RR. The PIT fires IRQ0 at the configured quantum; the
   IRQ0 stub runs on the GUEST's stack, saves 16 GPRs into a stack-
   local buffer, captures the running sid, sets `preempt_pending`,
   EOI, iretqs back. The actual context switch happens at the
   session's next `sched_yield_current` -- the yield checks
   `preempt_is_on() && preempt_take_pending() == sid` and switches
   if both true. This is NOT a true preemptive context switch.
3. Post-v1 (only if needed, ABI-neutral): head-of-line bump — sessions
   with pending port messages get next-turn priority (~20 lines). This is
   the ONLY sanctioned refinement.

**Why no true preemptive context switch (binding design decision):**
the wasm3 interpreter is effectively a virtual machine whose internal
state (`_sp`, `_mem`, the metacode PC) is opaque C locals in
`m3_exec.c`. The kernel cannot save or resume it mid-op without either
patching wasm3 (violates the "vendor wasm3, don't clean-room it"
principle) or corrupting its state. The Go runtime *is* the
preemption mechanism: Go 1.14+ yields cooperatively in wasm at every
goroutine switch point, and our kernel switches sessions at those
yield points. So the kernel's job is to **switch sessions at yield
points**, not to preempt the interpreter. A session that never calls
a kernel import is still starved until it yields -- that's a correct
constraint, not an oversight. The busy/polite gate p8a exercises this.

**Multi-core (Phase 8.1+, planned, not implemented):**
each AP core runs its own cooperative-under-interrupt scheduler over
its own session pool. No session migrates between cores. The Go
runtime's natural yielding provides the "preemption" per core, and the
multiple cores provide the parallelism -- neither requires touching
the opaque interpreter state. Per-core `cur` pointer replaces the
single global; the spinlock API (`core/arch_lock.h`, commits
f20ed90 + d873672) makes cross-core shared state (port ring, mm pool)
safe. All cores share the same identity PML4 set up by `paging_init`
for CPU0 -- rule #2 (no per-arch page tables) is satisfied.

Explicitly rejected regardless of future justification: priority levels,
MLFQ/CFS-style fairness, real-time classes, affinity controls. Reasons:
two-level scheduling exists anyway (Go runtime schedules within sessions),
workload is I/O-shaped servers, and substrate-freeze rule 3. Scheduler
total budget: ≤400 lines including queues and timer hookup.

## Engineering practices (binding, added 2026-08-23)

1. **Compatibility contract tests**: gate matrix MUST include
   {committed kernel} × {each shipped *.wasm artifact}, not just
   current-tree builds. A green `test-all` on freshly built modules says
   nothing about already-shipped ones.
2. **No fix without a failing test**: closing any VERIFY finding requires
   attaching a regression test that fails on the pre-fix code.
3. **Artifact freshness ledger**: VERIFY records sha256 of every shipped
   .wasm plus the ABI commit it was built from; drift between artifact
   and latest ratified ABI = automatic MAJOR finding.
4. **Fuzzing**: native `go test -fuzz` targets for every wire-format
   parser (port header, LOGIN/AUTH payloads, input records, kfs record
   stream). Minimum soak: 30 s per target per QA sweep.
5. **Chaos gate**: integration runs include randomized service KILLs;
   assert respawn/isolation each time (extends the p4 harness).
6. **Threat model before Phase 10**: STRIDE-lite pass over registry +
   port routing + fs rooting, produced by VERIFY, consumed by MAINLINE.
7. Blameless post-mortem notes per incident land in MEMORY.md (existing
   practice, now formalized).
8. **Anti-thrash / commit discipline**: if ALL current gates pass on
   your tree after two consecutive sweeps, you MUST commit before running
   any gate again. Re-running a green gate more than twice without an
   intervening commit is a defect (observed: 127 g1 reruns, zero fails,
   zero commits). Security-negative-path work lands INCREMENTALLY — each
   hardened surface commits with its own regression test; waiting for
   "everything safe" means committing never.
9. **Escape-hatch honesty**: emitting ALL PHASES COMPLETE while fixes
   exist only as uncommitted working-tree state is forbidden (see Gate
   audit 2026-08-23). If blocked from committing, say so in MEMORY.md
   and continue other work instead of declaring victory.

## OS server inventory

| server | phase | role |
|---|---|---|
| init.wasm | 7 | boot order, supervision/respawn, kernel.conf |
| console.wasm | 4 | output relay + log tags + audit sink display |
| login.wasm | 4/10 | auth → per-user capability sets → focus |
| fs.wasm | 5/8 | FAT16 over block window; /etc, /home/<u>, /boot/modules |
| shell.wasm | 7 | built-ins + `run` via SPAWN; admin built-ins per §7 |
| net.wasm | 9 | ARP/IP/ICMP/UDP/TCP over packet windows; "net" port |
| graphics.wasm | 11 | software compositor over VFIO-mapped framebuffer |
| usb.wasm | 12 | xHCI over VFIO passthrough |
| bt/wlan.wasm | 12 | wireless stacks above reserved classes |
| e1000.wasm | 13 | NIC over VFIO passthrough |
| ahci.wasm | 13 | block storage over VFIO passthrough |

Post-v1 candidates: syslog file sink, package installer with signature
checks, per-session memory quotas.

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
                        vfio pci (Phase 11)
third_party/wasm3/      adapted engine (MIT)
services/               console.wasm login.wasm fs.wasm (sources: go/rust/c)
                        net.wasm graphics.wasm usb.wasm bt.wasm wlan.wasm
                        e1000.wasm ahci.wasm
guests/                 hello.c hello.rs hello.go → .wasm
tools/img               host-side image builder (go, replaces mkpefi glue later)
abi/ABI.md              guest-facing contracts (v2.0)
kernel/link.ld scripts/mkpefi.py Makefile AGENTS.md MEMORY.md README.md
```
