# MEMORY.md — kernel project state

Read this first. Source of truth when context is compacted.
The full phase-by-phase plan lives in `AGENTS.md` — this file is state,
decisions, and gotchas. Update at milestones or near context limits.

## Status (as of 2026-08-22)

**Phase 4 gate GREEN (commit 7e7c2dd).** Message ports per ABI §1 in
'kernel' namespace; kernel-owned registry/devman/power endpoints (§7
framing {u16 op,u16 seq,payload}, replies to sending port, capability
bits + [audit] to serial); cooperative coroutine scheduler (ctx.S,
per-session native stacks + per-session WASI state); console.wasm +
login.wasm preloaded pre-EBS from /boot/modules; SPAWN resolves from a
boot preload table until fs exists (documented interim divergence).
Gate: ping-pong x3 + LIST shows 4 sessions + KILL console rc=0 with
login heartbeats continuing + KERNEL-OK.
Host driver tools/hostp4.cc runs the whole scenario pre-QEMU — always
do that first. Next: **Phase 5** — fs.wasm (Go, FAT16 over §3 block
window, RAM-disk backend), dual client routing (kernel-routed preview1
path_open/fd_read/fd_write/fd_close/path_create* AND guests/lib direct
ports), multiuser stubs (/etc static table, /home/<u> roots),
abi_ver custom section check; retire kernel/vm+vasm+demo.

Allocator lessons (rt.cc): free-list next ptr must NOT clobber hdr.size;
binned blocks rounded to FULL class size so pops never undersize;
cls_of returns NCLASS sentinel for oversized → exact alloc, leaked on
free; Go runtime PANICS at init unless fd_prestat_get returns EBADF;
host shims must use __libc_malloc aliases (std::malloc resolves to your
own definition). make test ≈150s each — budget bash timeouts.
ctx_make: RIP slot is the HIGHEST address of the crafted frame.

Phase-2 notes: C++ has no range designators (vm jump table fills at
runtime); efi_main needs extern "C" or the linker silently defaults the
entry point; mm_alloc sits AFTER where mm_pool was in mm.cc (don't
re-truncate it).

**Interface contracts FROZEN: `abi/ABI.md` v1** (ports §1, console §2,
block window §3, input/focus §4, timer §5, net windows §6, kernel-owned
service ports §7 — registry/devman/power with capability bits — and the
two-layer device driver model §8). Kernel AND services code against it
only; changes = version bump there. Key decisions encoded: fs.wasm Phase 5
runs on RAM-disk block backend, Phase 8 re-backs same window with virtio-blk
(no guest change); FS client routing = BOTH kernel-routed preview1 and
guests/lib direct ports; boot orchestration = kernel spawns only init.wasm,
which reads /etc/init.conf (Phase 7); admin tools are shell built-ins over
§7 ports; modules carry abi_ver custom section checked at instantiation.
Later additions to ABI v1: registry SPAWN op (bit6 CAP_SPAWN) = the ONLY
program-launch mechanism (no fork/exec); §10 audit trail relays rejected
ops to console; init.wasm supervises children + respawns per
/etc/init.conf policy; server inventory table in AGENTS.md.
Scheduler policy BINDING: round-robin forever (cooperative Phase 3 →
preemptive Phase 8, quantum via kernel.conf); sole sanctioned future
refinement = head-of-line bump for sessions with pending port messages;
priorities/MLFQ/CFS/RT classes explicitly rejected; ≤400 LOC budget.

Phase-1 fixes worth remembering:
- loader regression was REAL: AllocatePool called via FW4 (extra
  BootServices arg landed in Type slot → INVALID_PARAMETER). Now FW3.
- vasm pass-1 bug: text labels all resolved to offset 0 (p.text fills in
  pass 2) → every jz jumped to pc=0, guest spun silently forever. Host
  harness for vm.c (/tmp pattern: stub serial/mm_alloc) is the fast way
  to debug guest programs — use it before QEMU.
- CPUID OSXSAVE bit27 reads 0 until CR4.OSXSAVE set; never gate on it.
- Gate grep must accept zero-padded hex ('out 0x0000000000000028').
- make test costs ~150s (kernel halts → QEMU runs to 120s timeout);
  budget timeouts accordingly.

**Roadmap extended (user-approved pace): Phases 7–10 added to AGENTS.md**
(interactive shell/userland → preemption+persistent storage → Go network
stack → multiuser hardening/release eng; 9 is stretch). Completion sentinel
redefined there: ALL PHASES COMPLETE = every gate in AGENTS.md green (0–10,
6 optional). The running overnight.sh still carries old "Phases 0-5" wording
in its CONT prompt — harmless, AGENTS.md overrides completeness semantics;
rewrite script wording at next idle restart.

## Project vision (endgame, user's words)

System programming in **Go**, on a deliberately **non-POSIX** capability
microkernel — "otherwise it's replicating Linux, which is going for Rust
and C." Portable by construction to x86_64 / aarch64 / riscv64: services are
`.wasm` bytecode and never get ported; only the thin substrate shell does.

## Decision history (why the repo looks like this)

1. **Original vision**: microkernel *is* an 8-opcode restricted ISA machine
   (`kernel/vm/isa.h`, interpreter in C) for portability/minimal TCB.
2. **The pivot (rejected)**: a prior agent was asked to use Plan 9 asm
   instead of GNU as, escalated into porting the full Go runtime to bare
   metal: custom GOOS=baremetal, 13 GOROOT patches ($GOROOT_BARE), merged
   PE shim+Go image. User calls this "a nightmare" — it buried the 8-opcode
   kernel under a runtime port nobody asked for. Artifacts still in tree:
   `gokernel/`, `tools/goshim/`, Go rules in Makefile, marker handoff in
   `kernel/main.c`. All retired in Phases 2–3.
3. **Converged design**: wasm + mini-WASI as guest ABI (replaces the
   8-opcode ISA entirely once wasm works). Rationale: stock toolchains
   (Go wasip1 since 1.21 — zero patching, the anti-nightmare), sandboxing,
   byte-identical portability. 8-opcode ISA proved native boot in Phase 1,
   then retired to git history.

User-approved choices: retire 8-opcode ISA · adapt wasm3 (MIT, not WAMR,
not clean-room) · guest ladder C→Rust→Go · GNU as (Plan 9 asm mandate dead)
· freestanding C++20 with RAII allowed · separate servers over message ports
(NOT a stacked monolith) · multiuser = policy layer via login.wasm capability
grants, not ABI-baked.

## Architecture decisions (binding)

- Sessions = wasm instances; isolation via engine bounds checks, NOT MMU.
- WASI is per-session capability surface implemented by the kernel natively;
  there is no "WASI server". FS/login/drivers-policy are separate .wasm
  servers talking over kernel-relayed message ports (Phase 4 imports).
- Drivers: raw HW only in tiny frozen native device-window shims inside the
  substrate; wasm holds policy. New hardware class ⇒ substrate edit.
- Client→FS routing decision deferred to Phase 5: kernel-routed preview1
  path ops vs client lib over ports. Document choice here when made.
- No fork/exec/signals/global namespace ever. Non-POSIX is the point.

## What survives / what gets retired

Survives: UEFI C shim core (`kernel/{main,serial,cpu,mm,gdt_idt,lib,loader}`,
link.ld), mkpefi.py approach, mtools disk flow, make-test harness pattern,
`tools/vasm` until Phase 5 cleanup.
Retired (recoverable from Phase-0 git snapshot): `gokernel/*`,
`tools/goshim/*` (Plan 9 asm IDT bank), `scripts/goaddr.sh`, all
GOROOT_BARE make rules, GO_MAGIC markers in main.c, eventually
`kernel/vm/ tools/vasm programs/demo.*`.

## Environment (no root!)

- `~/.local/osdev-root`: qemu-system-x86_64 10.x, OVMF_CODE_4M/VARS_4M.fd,
  mtools, seabios. QEMU needs `LD_LIBRARY_PATH=$ROOT/usr/lib/x86_64-linux-gnu`
  and `-L $ROOT/usr/share/qemu -L $ROOT/usr/share/seabios`.
- Network OK; downloads/install prefixes under `~/.local` work (uid 1000).
- System go exists (check version ≥1.21 for wasip1). $GOROOT_BARE is DEAD —
  never reference or rebuild it; do not delete it either (out of repo scope).
- KVM available iff /dev/kvm writable, else TCG `-accel tcg` (fine).

## Gotchas (still valid — don't relearn)

- PE32+ optional header offsets above CheckSum are easy to get wrong;
  section table entries exactly 40 bytes. mkpefi.py encodes these — touch
  carefully.
- objcopy cannot emit EFI PEs here; mkpefi.py is the ELF→PE converter and
  must parse PT_LOAD itself (objcopy -O binary start-vaddr bug).
- OVMF rejects images whose DOS stub isn't padded to exactly 0x80 before
  'PE\0\0' → falls to UEFI shell ("Unsupported").
- Loader uses raw vtable offsets into EFI protocols (`(char*)sfs+8`,
  `file+56` etc.) — spec-fixed but fragile; keep FW1..FW5 wrappers.
- Serial log greps: strip ANSI escapes before matching.
- Stale-object danger: after changing kernel sources, rebuild clean; a boot
  log older than its binary is worthless (this bit during the Go era).
- QEMU TCG supports AVX2 with `-cpu max`; irrelevant post-ISA-retirement.

## Obsolete knowledge (historical — gone with the Go pivot)

GOROOT_BARE rebuild procedure, go-tool-asm quirks (DATA $sym(SB), bare
INB/OUTB, nosplit self-JMP), linkname/cgo_import_static linker restrictions,
baremetal GOOS patch list, pageAlloc 32-bit-style workaround, doubled serial
chars issue. Do not resurrect; kept here only so future agents don't dig.

## Next actions (verbatim checklist for next session)

1. `git init && printf 'build/\n' > .gitignore && git add -A && git commit`
   — verbatim snapshot BEFORE any surgery.
2. Phase 1 per AGENTS.md: rewire main.c → kmain(); Makefile objects;
   mkpefi shim-only; root-cause loader regression (stale log vs
   LocateProtocol); gate = KERNEL-OK + out 0x28 + 'E'.
3. Proceed Phase 2 onward only while gates stay green.

**ABI v1.1 RATIFIED (master b9263a2, 2026-08-22)**: canonical datagram
header {u16 op,u16 seq,u32 uid,char rname[16]} payload@24 (uid
kernel-stamped); §3 block offsets pinned + scratch@0x1000 min window
0x2000; registry ops 5=LOGIN 6=SETCONF (+bit7 CAP_CONF); abi_ver custom
section byte 0x01 mandatory; managed-runtime guests use kern_blk_read/
write imports instead of §3 window. ACTION REQUIRED before your next
commit: `git merge master` in this worktree (disjoint paths => clean),
then conform code to v1.1. Verify lane: check conformance.

**ABI v1.2 RATIFIED (master 8d2c106)**: §9 FRAMEBUFFER class window
DEFINED — magic 'FBW', geometry@0x04, fb_off@0x10, caps@0x18, mailbox
SET_MODE/FLIP/UPDATE_RECT @0x20; single-buffer default; width==0 = no
display. Backends: Bochs DISPI then VMware SVGA II. Merge master before
next commit.

**ABI v1.3 RATIFIED (master 3912a37, 2026-08-23)**: §4 input records now 6 bytes {u8 kind,u8 mods,u16 scan,u16 codepoint} — scan = raw i8042 set-1 scancode; layouts become userland keymaps under /etc/keymaps/ (kernel keeps US default). Merge master before next commit. VERIFY: audit consumers of the old 4-byte record shape.
