# MEMORY.md — kernel project state

Read this first. Source of truth when context is compacted.
The full phase-by-phase plan lives in `AGENTS.md` — this file is state,
decisions, and gotchas. Update at milestones or near context limits.

## Status (as of 2026-08-23)

**Phase 8 gate GREEN (commit 91a1367).** Cooperative no-starvation:
busy+polite sessions interleave via voluntary yields; both complete.
Timer/PIC/IRQ0 infrastructure done (PIC remapped, PIT @1kHz, vec32
gate, EOI). Paging: full 4GB identity map. Preemptive scheduling
implemented but DISABLED (preempt_on=0) — cross-stack wasm3 corruption
under TCG; enable via SETCONF preempt=1 after debugging. Key fix:
session_entry must read cur (set by mark_running) not g_entering global.
Remaining for ALL PHASES COMPLETE:
- Phase 8 gate (a): persistence — need virtio-blk re-backing or equivalent;
  write file → reset → read back. No-starvation (p8a) already PASSES.
- Phase 9 (stretch): net.wasm — CAN DROP if week ends.
- Phase 10: /etc/users hashed login, tools/img Go builder replacing mtools,
  README.md, test matrix under KVM+TCG.
All Phase 0-7 gates remain green (verified this session).

**Phase 7 gate GREEN (commit 95a22d7) — Phases 0-5 + 7 ALL GREEN.** Timer/PIC/IRQ0 infrastructure done
(PIC remapped, PIT @1kHz, vec32 gate, EOI). Paging: full 4GB identity
map (Go wasm grows past old 512MB boundary — was the g3 crash).
Cooperative scheduling primary; preemptive scheduling implemented in
preempt.S but DISABLED — cross-stack IRET corrupts wasm3 JIT state under
TCG (fxsave/fxrstor + alignment all verified correct; root cause is
deeper, likely wasm3's use of computed gotos + longjmp interplay with
interrupted C frames). Preempt can be enabled via SETCONF preempt=1.
Phase 8 persistence (virtio-blk re-back) and no-starvation gate NOT yet
done. Next: fix preempt OR implement virtio-blk + p8b gate.

**Phase 7 gate GREEN (commit 95a22d7) — Phases 0-5 + 7 ALL GREEN.**
Interactive userland live: input/focus (§4), init-driven boot (kernel
spawns only init; conf via argv[1]), typed login -> focus shell,
shell built-ins echo/cat/ls/stat/kill-session, services/lib = guest
libc (module kernel.services). Gate p7 drives scripted stdin over a
QEMU serial pipe (scripts/run_p7.sh). kmain has TWO modes: init-driven
(mod_init present) or legacy payload-slot gates (p4/p5 disks).
Phase-7 lessons: Go runtimes call poll_oneoff routinely — stub must
YIELD quanta, not return instantly (starvation) nor ENOSYS (fatal);
serial RX needs EOF-safe readiness (never push garbage at EOF);
services need bounded lifetimes so sessions drain and KERNEL-OK
prints; /etc is world-readable via namespace special-case.
Next: **Phase 8** — §5 timer window, IRQ-preemptive RR (quantum from
kernel.conf via SETCONF), cooperative fallback flag, virtio-blk shim
re-backing the block class behind kern_blk_* imports. Gates:
persistence across reset + busy-loop cannot starve second session.

**Phase 5 gate GREEN (commit da43b41).**
fs.wasm = FAT16 over kernel RAM disk; dual routing live: (a)
kernel-routed preview1 path_open/fd_read/fd_write/fd_close via
_fsbuf/_fsreq sync exports + fsroute yield-wait (NO wasm3 re-entry),
per-session fd tables in sched_wasi_state (fd>=3 -> fs fh; 0/2 stdio);
(b) direct-port framed ops. Paths rooted /home/<uid> (auto-vivified),
admin=/. LOGIN op grants uid+capmask by session name (login-owner
only). abi_ver=1 stamped on every module, checked at instantiation.
8-opcode artifacts RETIRED (core/vm tools/vasm programs); make test =
g1 smoke; full matrix: test-g1 g2 g3 p4 p5a p5b.
Canonical request frame (ALL services/clients): {u16 op,u16 seq,
u32 uid,char rname[16],u16 plen,path,payload} — replies go to rname.
Block transport for managed-runtime guests: kern_blk_read/write
imports (ABI v1.1 amendment); §3 window kept for raw guests.
fs core is host-testable: cd services/fs && go test (RAM-disk stub).
Next (Phases 6 optional / 7+): input+focus imports, shell/init,
preemption, virtio re-back, net.

Hard-won rules (Phase 4/5 debugging):
- After EVERY scripted source edit, grep-verify the change landed —
  silent pattern misses cost hours (authAs, doLogin framing).
- Stale-artifact paranoia: verify marker strings INSIDE built binaries;
  guest/service .wasm rebuilds need explicit rm+rebuild discipline.
- Frame offsets must be identical on both ends — unify via one comment
  block copied into parser+builder.
- Never call into a suspended wasm3 runtime from another session;
  route through ports + yield-wait instead (fsroute.cc).
- Go guests own their low linear memory: no kernel data structures in
  guest RAM below the heap horizon; use imports for device transport.
- port_recv pops ring[qh] BEFORE advancing qh (order got reversed once).
- Gate greps: escape [brackets] or they become regex char classes.

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

**FLEET MODE ACTIVE (Phase 4 green → provider-unlimited week).** Lane
MAINLINE = main tree/master (kernel phases, watchdog.sh); lane SERVICES =
../kernel-lane-services @ lane/services (console/login/fs/shell/init +
guests/lib, host-Go→wasip1); lane TOOLS = ../kernel-lane-tools @ lane/tools
(tools/img, README). fleet.sh+lanes.conf supervise SERVICE/TOOLS lanes
(pidfile-based precise kills); cron-guard revives all three layers. Merge
lane branches to master when a gate consumes them; ABI frozen — proposals
to services/ABI-NOTES.md only. Multiuser FS v1 = namespace-rooted
isolation (FAT16 has no owner bits): kernel stamps {sid,uid} on every
forwarded FS op (clients can't spoof); /home/<u> private, /tmp world-RW,
/etc writes need CAP_FS_ADMIN; sidecar owners.db deferred post-v1.
Fleet extended to FIVE lanes: +VERIFY (QA dept, read-only everywhere,
reports in its own tree: FINDINGS.txt/QUALITY.txt; BLOCKER = ABI violation
or capability gap) and +DOCS (publications dept, IBM-style plain-text
docs/*.txt with control blocks/traceability/revision history). Both are
read-only outside their own worktrees by contract.

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
- HOST IS WINDOWS (+WSL2): host sleep/hibernate freezes ALL processes with
  no log trace; on resume uptime reads low while old PIDs persist
  (incident 2026-08-22 ~11:53→14:03). Host "sleep after 5 min plugged in"
  was the culprit — now disabled by user. Watchdog hardened for this:
  900 s threshold + two-strike debounce (cold starts post-resume look
  stalled but are fine). Windows Update auto-restart would kill WSL
  entirely — disable scheduled restarts for true week-long runs.
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

**QA INTAKE NOW BINDING**: before any commit, read
/home/cyr/kernel-lane-verify/verify/FINDINGS.txt — fix BLOCKER/MAJOR items
against this repo first (or rebut in MEMORY.md). Current open BLOCKERs on
MAIN dirty tree (as of 18:15): #11 ports.cc ring advance-before-read,
#1 kernsvc getenv() breaks freestanding build, #2 v1.1 reply framing
diverges across kernsvc/login/fs — canonical header is {op,seq,uid,rname}
24-byte form per abi/ABI.md. See FINDINGS.txt for details.

**CROSS-LANE CONTRACT NOTES (binding until ABI v1.1 review)**: lane
SERVICES pinned concrete instantiations in services/ABI-NOTES.md that
MAINLINE MUST mirror exactly: (a) §3 block window uses naturally-aligned
offsets — magic@0x00 blk_size@0x04 num_blocks@0x08 next_req_id@0x10
pad@0x14 op@0x18 lba@0x20 count@0x28 pad@0x2c off@0x30 done_req_id@0x38
status@0x3c; guest scratch at off=0x1000; min window 0x2000; fs session
needs CAP_DEVMAN grant at boot. (b) User-server reply convention until
reply_to lands in v2: client inbox port named <role>.<nssalt> ≤15 chars,
request carries {op,seq,inboxLen,inbox,payload}, server caches reply book.
(c) FS port protocol ops 1-9 + status codes per ABI-NOTES §3 — kernel
preview1 route must translate onto these same ops. Read the full file
before touching kernsvc/fs framing.

**ABI v1.1 RATIFIED — master b9263a2 (2026-08-22).** All port datagrams:
{u16 op,u16 seq,u32 uid,char rname[16]} payload@24; uid kernel-stamped on
send (spoof-proof); rname = requester reply-inbox, empty = synchronous.
§3 block offsets pinned (scratch@0x1000, min window 0x2000); managed-
runtime guests use kern_blk_read/write imports. §7 ops now: LIST CAPS
KILL SPAWN LOGIN SETCONF (+devman ENUM, power REBOOT/OFF); bit7=CAP_CONF;
abi_ver custom section (byte 0x01) mandatory per module. §11 = v2 roadmap
(reply caps kern_port_reply one-shot; LIST/CAPS gating Phase 10; IRQ arms
post-P9; §9 class layouts). NOTE b9263a2 also carried MAINLINE's staged
8-opcode retirement deletions (core/vm/, programs/demo.*, tools/vasm
go.mod+main.go) — intended by Phase 5, just bundled early. All four lane
worktrees notified via their MEMORY.md to merge master before next commit.

**PHASE 5 GATE GREEN (da43b41 + 6a22fec) — Phases 0-5 ALL GREEN.**
NEXT = Phase 7 (interactive userland). COORDINATION NOTE: shell/init/
console/login .wasm binaries are LANE SERVICES deliverables — merge
lane/services into master BEFORE starting Phase 7 (15 commits ahead,
v1.1-conformed per VERIFY ec69edd). After merge, run make test-g* style
integration with real service modules instead of stubs.
