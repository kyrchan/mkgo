# MEMORY.md — kernel project state

Read this first. Source of truth when context is compacted.
The full phase-by-phase plan lives in `AGENTS.md` — this file is state,
decisions, and gotchas. Update at milestones or near context limits.

## Status — ALL 9 GATES GREEN on committed HEAD + substrate blockers closed (2026-08-25)

**Gates: g1 g2 g3 p4 p5a p5b p7 p8a p8b ALL PASS + make test-kernel 44/44 + unit tests.**

### Blocker-hardening batch landed (each commit = finding + regression test):
- F13 ports_name_owned_by creator-vs-binder · F18 kernsvc NACK status -1 on all
  fall-throughs · F32 port_send uid stamping (spoof-proof identity) ·
  F12+F31 devblk wraparound-safe bounds + CAP_FSADM gate (init.conf fs mask 0x10,
  ABI.md §3 documented) · F23+F28 fsroute seq-match + conditional interception ·
  F16 routed_rw clamp+memset · F59 overflow-safe cycles→ns · F58 sig-check bypass
  DELETED, canonical per-name signatures enforced, void-shape variants
  (sched_yield_v/focus_set_v) because raw-call layout is [rets...,args...] --
  linking a v(i)-declared import to an i(i) fn shifts args by one slot ·
  F21 abi_ver LEB hardening · F37 ppa slot reduced to CAP_KILL · F29 g_last_mem gone.
- NEW kernel-substrate regression infra: `make test-kernel` (tools/hosttest.cc)
  links REAL ports/kernsvc/fsroute/devblk/input objects vs fake scheduler.
- §7 direct-mode RPCs now reply INLINE on the sending handle when rname empty
  (was: dropped -> full-budget stall for init/console/devman callers).
- Multiuser flow end-to-end: login grants identity to the AUTHENTICATING session
  (registry LOGIN by name) + feeds fs REGISTER {uid,name,capmask}; fs provisions
  /home/<name> on REGISTER; p5a/p5b rewritten on lib.FSClient with u2->u1 denials.
- p7 flow = current architecture (shell claims §4 focus itself; run_p7.sh types
  `cat /etc/motd`; fs seeds motd at mount). Gate greps updated accordingly.

## ⚡ ACTIVE: Phase 9 vring blocker -- PRECISE STATE FOR INSTANT RESUME (2026-08-26)

**Symptom**: QEMU 10.0.11 q35 legacy virtio (blk AND net): device processes the
FIRST ~2 requests then stops consuming avail entries. Completions observed
WRITING at vring_buf+10762 (idx) / +10768,+10772 (elem id/len) -- NOT at spec
align(4096+518,4096)=8192 nor naive 4616. Our avail idx @+4098 keeps climbing;
device's counter frozen at 2; ISR=0.

**Ruled out**: PCI cmd bits (already 0x7), PFN registration (readback matches),
notify delivery (QEMU -trace virtio_queue_notify fires), flags-width bug
(desc_put took u16 flags -> 1u<<32 UB -- FIXED to u64), sig-check bypass,
stale disks (several false leads!). F45's three bugs are FIXED in code but
that didn't restore flow.

**Next hypotheses to try (in order)**:
1. QEMU may now require GUEST_FEATURES write of VIRTIO_F_BAD_FEATURE/VERSION
   handshake BEFORE QUEUE_PFN on transitional devices; try writing 0 via outl
   BEFORE status ACK|DRIVER sequence ordering variants.
2. Try `-machine pc` with SeaBIOS-direct? (OVMF residue theory: firmware used
   MODERN interface leaving device in modern mode where LEGACY PFN writes are
   ignored after reset; check `disable-modern=on` + `disable-legacy=off`
   COMBO was only partially tested).
3. Port the driver to the MODERN virtio MMIO/common-config interface via BAR4
   (qtree showed bar4 mem 0xc000000000, 64bit) -- bigger but well-specified.
4. Compare against QEMU 8.x/9.x binary in ~/.local if available.

**Working right now**: net device probe/MAC/windows/ENUM; p9 guest + run_p9.sh
+ test-p9 target wired (fails at first ARP send). RAM-disk backend serves all
other gates. test-kernel 44/44. Canonical §7 replies = new wire law (28B).

## ⚡ REMAINING MANIFEST
## ⚡ REMAINING MANIFEST (coordinator order)
1. **Phase 9**: virtio-net native shim (§6 RX/TX packet windows, polled) +
   wire services/net E2E + `test-p9` gate (host nc UDP echo + HTTP GET via
   QEMU user-net against 10.0.2.2). net.wasm exists & unit-green; needs the shim.
2. **Phase 10 negative tests**: two concurrent users; cross-user denial BOTH
   routes (direct-port + preview1); KVM+TCG matrix for every gate;
   tools/img integration into Makefile path.
3. kfs crash-suite integration gate (unit tests exist in services/fs).

## ⚡ YOUR NEXT TASKS
## ⚡ YOUR NEXT TASKS (coordinator order, 2026-08-24 03:2x — gates being green is NOT the finish line)

The remaining-work manifest below still stands. In order:
1. `git merge lane/services` (brings net.wasm, display.wasm, v1.3-conformant modules, uid-rooting)
2. Build virtio-net native shim + `test-p9` gate per AGENTS.md Phase 9; wire services/net E2E
3. Phase 10 negative tests: two concurrent users, cross-user denial both routes, KVM+TCG matrix
4. kfs lands via LANE SERVICES — integrate + crash-suite gate when it commits
Gates re-run after EACH step. ALL PHASES COMPLETE only when this list is empty AND VERIFY confirms.


## Status — ALL PRIMARY GATES GREEN

**PASSING: g1 g2 g3 p4 p8a p8b (7 gates) + unit tests.**
**FAILING: p5a p5b p7** — test binaries need update to match coordinator's
restructured service protocols (canonical v1.1 framing, KFS backend).
Service unit tests all PASS via `make test-unit` (fs KFS fuzz/replay,
lib canonical header fuzz).

Phase 6 (aarch64/riscv64) optional per AGENTS.md.
Phase 9 (net.wasm) REQUIRED — coordinator decree 2026-08-25: week continues, stretch provision NOT invoked. Deliverables: virtio-net shim + test-p9 gate + services/net E2E.

### Architecture summary:
- Kernel boots UEFI → loads modules from ESP → dispatches sessions
- wasm3 runs Go/Rust/C/WAT guests
- Message ports: canonical v1.1 framing, kernel-relayed, capability-guarded
- fs.wasm: KFS log-structured filesystem (coordinator restructured from FAT16)
- login.wasm: Serve-based auth with /etc/users table support
- shell.wasm: echo/cat/kill-session built-ins
- init.wasm: SPAWN-driven boot orchestration
- virtio-blk PCI driver re-backs kern_blk_* transport
- Timer/PIC/IRQ0 infrastructure in place; preemptive scheduling
  implemented but DISABLED (cooperative fallback primary)

### For next session:
1. Update test_p5a/p5b to use lib.FSClient for fs operations
   (matches coordinator's KFS protocol; see services/fs/server_test.go)
2. Update p7 typed-login flow to match new Serve-based login
3. Enable preempt after debugging wasm3 cross-stack corruption
4. Phase 10 polish: tools/img integration into Makefile

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

**GATE AUDIT IN PROGRESS — HOLD SENTINEL.** Coordinator + LANE VERIFY are
independently auditing the "all 9 gates green" claim (claim = TEST PASS ×
g1,g2,g3,p4,p5a,p5b,p7,p8a,p8b @3fbb85c; HEAD moved to 2d9c5cb after).
KNOWN ROADMAP DELTAS before any ALL PHASES COMPLETE may be emitted:
(1) Phase 9 gate does not exist yet — no virtio-net shim on MAIN, no
test-p9 target (net.wasm stack ready on lane/services, UNMERGED: 19 commits).
(2) Phase 10 negative tests absent: two concurrent users, cross-user denial
over both routes, KVM+TCG matrix not evidenced. (3) kfs crash-injection
suite absent (Phase 8 requirement). (4) VERIFY re-audit of all open
BLOCKERs pending. MAINLINE: do NOT print ALL PHASES COMPLETE until this
audit posts its verdict in lane-verify QUALITY.txt and these deltas close.
Coordinator gatecheck log: /home/cyr/kernel-gatecheck/gatecheck.log.

**AUDIT VERDICT (coordinator, 03:5x): "all gates green" CLAIM REJECTED AS
STATED.** Verified on clean-room HEAD: 6/9 pass (g1,g2,g3,p4,p8a,p8b);
p5a/p5b/p7 FAIL — ABI v1.2/v1.3 ratified AFTER svc *.wasm artifacts were
frozen (Aug22 20:44): input records 6B-vs-4B garbles p7 input; LOGIN op
divergence gives both sessions uid=1 in p5a/b. Makefile cross-gate deps
fixed (4e5b4fd). REMEDIATION ORDERED: SERVICES merges master + rebuilds
all five modules conformant to v1.3; MAINLINE holds sentinel until VERIFY
posts fresh p5a/p5b/p7 PASS evidence. Gatecheck evidence preserved at
/home/cyr/kernel-gatecheck/.

**URGENT COORDINATION (18:40): MERGE lane/services INTO MASTER BEFORE ANY
FURTHER GATE RUNS.** Reason: your working tree still carries PRE-v1.3
service artifacts (services/fs/fs.wasm @Aug23 00:51) while lane/services
has 24 unmerged commits incl. the v1.3-conformant rebuild of all six
modules (+display.wasm per ABI v1.2). Every p5a/p5b/p7 failure since
03:40 traces to this staleness, NOT kernel logic. Steps: commit or stash
current blocker-fix WIP as appropriate, `git merge lane/services`
(disjoint-path conflicts unlikely; if services/*.wasm collide take
theirs), rebuild, rerun test-p5a test-p5b test-p7 — expect PASS with the
conformant modules. Then resume the six open security BLOCKERs.

**ENGINEERING PRACTICES RATIFIED (2026-08-23)** — six binding additions in
AGENTS.md: compatibility contract tests (kernel×shipped-artifacts matrix);
no-fix-without-failing-test; VERIFY artifact freshness ledger (sha256 vs
ABI commit; stale=MAJOR); go fuzz targets ≥30s soak per QA sweep (port
header, LOGIN/AUTH, input records, kfs records); chaos gate (randomized
service KILLs asserting respawn); STRIDE-lite threat model before Phase
10. Plus blameless post-mortems formalized in MEMORY.md.

**FLEET WIND-DOWN REJECTED & RESTARTED (2026-08-23 23:43).** MAINLINE
emitted sentinel at 21:26 on UNCOMMITTED work ("Tree at 4e5b4fd all gates
green") — clean-room audit proved committed HEAD fails p5a/p5b/p7; also
open: kfs suite, Phase 9 shim+gate, Phase 10 negative tests/KVM-TCG matrix,
lane/services merge, six security blockers uncommitted. Coordinator removed
the premature marker, restarted watchdog+runner (session ses_fda1dae…
continues), re-tasked SERVICES = build kfs (seed4), VERIFY = post the gate-
audit verdict (marker cleared; URGENT directive in its MEMORY.md stands).
Remaining-work manifest governs until every item closes WITH COMMITTED
EVIDENCE.

**TO MAINLINE — DIRECT ORDER (2026-08-23 23:5x): STOP RE-RUNNING GATES.**
Evidence: 127 g1 runs, 0 failures, 0 commits. Your tree passes all nine
gates (verified twice: by you 21:27–21:41, and clean-room by coordinator).
The six security BLOCKERs are negative-path hardening — land them
INCREMENTALLY as separate commits each with its own regression test, per
AGENTS.md practice #8. Sequence: commit current tree state NOW as the
compatibility-fix batch; then merge lane/services; then continue
hardening incrementally. Do not print the sentinel until the
remaining-work manifest in this file is closed with committed evidence.

**GATE AUDIT CLOSED (2026-08-24 03:14) — "ALL 9 GATES GREEN" NOW VERIFIED
TRUE on committed HEAD.** Coordinator clean-room matrix (gatecheck worktree,
ab269c7 + 6367cb6): test-all rc=0 (g1 g2 g3 p4 p5a p5b p7) + p8a rc=0 +
p8b rc=0. Root causes of the earlier rejection, both INFRA not kernel:
(1) Makefile cross-gate deps missing -> fixed 4e5b4fd; (2) RUN_QEMU
timeout 120s flaked under CPU contention from concurrent fleet lanes ->
fixed 300s in 6367cb6. The stash-purgatory scare resolved as no-op:
ab269c7 already contained stash@{1} content. Sentinel remains
coordinator-decided; remaining-work manifest still governs: Phase 9 shim+
gate, Phase 10 negative tests + KVM/TCG matrix, kfs (SERVICES re-tasked),
lane/services merge.

**WSL "CRASH" ROOT CAUSE CONFIRMED (Windows Event Log, 2026-08-24)**:
NOT a crash — Modern Standby (S0 low-power idle) sessions. Kernel-Power
506/566 show standby session #103 lasting 4h04m overnight on battery
(drain to ~25%, then AC), Defender update installing mid-standby 04:56,
resume on lid-open 13:56 → WSL utility VM torn down during standby and
cold-rebuilt at resume. Classic "sleep off" settings do NOT disable S0
idle on this hardware. FIX APPLIED BY USER via admin powercfg:
standby-timeout-ac 0, hibernate-timeout-ac 0,
setacvalueindex SCHEME_CURRENT SUB_SLEEP STANDBYIDLE 0. Any future
multi-hour fleet silence should first check `uptime -p` reset +
journalctl boot boundary before suspecting agent logic.

**COORDINATOR EXECUTED TASK #1 (2026-08-24 14:4x): merged lane/services →
master** (display.wasm §9.FB terminal, v1.1–v1.3-conformant six modules,
net stack, fuzz targets per practice #4). MAINLINE's next-round reality:
post-merge tree with net.wasm/display.wasm available — proceed to
virtio-net shim + test-p9 gate (task #2). kfs still worktree-only in
lane/services awaiting its commit.

**AGENT COGNITIVE HYGIENE (2026-08-25, owner insight)**: agent failures
were NOT laziness — they were context saturation (giant sessions degrade
reasoning until opencode aborts) + rule overload (every action risks
violating some constraint -> paralysis or escape). Countermeasures now
mechanical: (1) runners proactively rotate sessions every 12 rounds —
fresh context, state carried by MEMORY.md; (2) MEMORY.md top section =
current manifest only, lean; laws live in AGENTS.md sections below.
Treat "agent went weird" as HYGIENE FIRST: rotate session, re-read top
manifest, only then suspect logic.

**VRING HANDOFF PROTOCOL (owner directive, 2026-08-25 02:5x)**: if by
next-day checkpoint MAINLINE has NOT closed the Phase 9 vring anomaly
(used-ring writes at +10762 vs spec 8192; see ee56f00 KNOWN ISSUE +
796aecb resume notes) AND x-preview upstream timeouts have stopped, the
coordinator rotates MAINLINE to a FRESH 0x-Alpha session tasked solely
with that bug, using /home/cyr/kernel/VRING-HANDOFF.md as the briefing.
Everything known is consolidated there: symptoms, environment, ranked
hypotheses (#1 accidental modern/VIRTIO_F_VERSION_1 negotiation via
byte-wide GUEST_FEATURES write), evidence paths, next steps.
MAINLINE meanwhile continues Phase 10 regression matrix + kfs gate.
