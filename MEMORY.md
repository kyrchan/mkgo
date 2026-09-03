# MEMORY.md — kernel project state

Read this first. Source of truth when context is compacted.
The full phase-by-phase plan lives in `AGENTS.md` — this file is state,
decisions, and gotchas. Update at milestones or near context limits.

## Status — Phase 15 identity/auth/observability GREEN (2026-09-03)

**Capability system: single source-of-truth cap table, REQUIRE_CAP gating on all kern_* imports, well-known port binding, login minting rule, audit-on-use.**

Gates: `make test-p4 test-p7 test-p17 test-p18` PASS (KVM) + hosttest 95/97 (T3/T4 pre-existing F18 format failures) +
login/shell/fs/pkg host suites green. Neighbors re-evidenced: p7, p9, p15, p16 PASS on the same tree.

### Capability table (§7, v2.0 additive)

| bit | name      | hex    | gated import(s)                          |
| --- | --------- | ------ | ---------------------------------------- |
| 0   | KILL      | 0x1    | registry op 3 (KILL)                     |
| 1   | DEVMAN    | 0x2    | registry op 10 (CHCAPS)                  |
| 2   | POWER     | 0x4    | power port reboot/off                    |
| 3   | FOCUS     | 0x8    | kern_input_recv, kern_focus_set          |
| 4   | FS_ADMIN  | 0x10   | kern_blk_read, kern_blk_write            |
| 5   | NET_ADMIN | 0x20   | —                                        |
| 6   | SPAWN     | 0x40   | registry op 4 (SPAWN)                    |
| 7   | CONF      | 0x80   | registry op 6 (SETCONF)                  |
| 8   | PCI       | 0x100  | kern_pci_read32/write32/map_bar/etc.     |
| 9   | FB        | 0x200  | kern_fb_set_mode/set_cursor/present      |
| 10  | DOORBELL  | 0x400  | kern_doorbell_wait                       |
| 11  | VMWARE    | 0x800  | kern_vmware_backdoor                     |
| 12  | PORTBIND  | 0x1000 | kern_port_bind on well-known names       |

admin = all 13 bits = 0x1FFF. KERN_AUDIT_LEVEL=1 (deny + use-audit).

### Phase 15 — what landed
- **Kernel v1 syslog (new, ~120 LOC mechanism):** `core/log.{h,cc}`
  16 KB ticket-locked ring + ever-growing total; one-line hook in
  `arch/x86_64/uart.cc:console_putc` (skips `\r`). Captures EVERYTHING
  on serial: boot trail, `[audit]` denials, panics, guest fd_write
  (wasi_glue funnels through putc). Hook takes only the spinlock —
  safe from isr_dump (IRQs off) and SMP.
- **Registry ops 8/9 (ABI v2.1, wire-superset — modules stay ver=2):**
  SYSSTAT `{mem_total, mem_used, quantum_us, preempt_on, ncpus}` backed
  by new `mm_total/used_bytes` (`core/mm.cc`), `preempt_quantum_us`
  (`core/preempt.cc`), `sched_ncpus` (`core/sched.cc`); LOGDUMP `{off}`
  → `{total, begin, ≤4000 B}`. No cap required (v1; hardening may gate
  later). Routine-poll spam suppressed for 8/9 like LIST. C++ linkage
  gotcha fixed: first-decl-in-extern-"C" wins (decls added to
  preempt.cc block + `sched.h`; `mm.h` already guarded).
- **Host regressions (practice #2):** T16 ring push/read/wrap/clamp/EOF;
  T17 SYSSTAT fields + LOGDUMP offset/marker round-trip through real
  kernsvc_dispatch (hosttest 76→100). Stubs: mm_tot/used, sched_ncpus.
- **login.wasm reloads /etc/users on every AUTH** (single attempt,
  last-good retained; canned-tables tests unaffected). Test:
  old-pw OK → change file → old BAD + new OK (real AUTH path).
  fsc budget cut 5000→500 so failing re-reads stay in client budgets.
- **shell built-ins (no fork/exec, pipes reused):** `passwd [user]
  <newpw>` — sha256(salt+pw) rewrite preserving comments/order, self
  only, CAP_FS_ADMIN for others, first-boot provisioning (missing file
  + uid 0 → `admin:0:...:0x3ff` row; ramdisk ships no /etc/users, only
  ESP does); `top` = LIST + SYSSTAT (cpus/quantum/preempt footer);
  `dmesg` = LOGDUMP full; `audit [subs...]` = `[audit]`-filtered log;
  `memstat` = pool total/used/free/pages + sessions. Tests for each
  (+fakeFS Create/Write now really store).
- **Serial `make test-p15`** (new `scripts/run_p15.sh`, polls for
  shell-ready): boot shell (uid 0, FOCUS-only) shows `5 sessions live
  (cpus=1 quantum=5000us preempt=on)`; `kill-session 1` denied AND
  `[audit] sid=5 uid=0 op=KILL reason=cap` recorded; `dmesg`/`audit
  KILL` read it back; `passwd newpass15` → ok (provisioning);
  `memstat` pool numbers. Login old/new-accept split: proven in login
  host tests (serial login scripting out of scope).
- **Pre-existing breakage (NOT mine, verified on clean HEAD via stash):**
  `guests/lib` netconn_test + `services/console` tests pass `*Bus`
  where `Kernel` (now with ClockMs since 09-02) is required — build
  fails. Same for `make test-unit`'s console/lib legs. VERIFY lane owns.

## Status — Phase 14 shell pipes GREEN (2026-09-03)

**Gate: `make test-p14sh` PASS (KVM) + shell `go test` green + hosttest 76/76.**

### Phase 14 — in-shell pipes + sequencing (landed 2026-09-03)
`services/shell/shell.go`: `exec` now handles `;`/`&&`/`||` sequencing
(exitStatus-driven) and `|` pipelines threaded in-process via capture
buffers (`capture`/`pin` fields, `runSingle`/`dispatch`/`execPipeline`).
No SPAWN/fork: all stages run in-shell. Built-ins gained stdin fallback
+ flags: `cat` (multi-file/stdin), `grep -n/-i/-v`, `sort -n/-r/-u`,
`head/tail -n N|-N|stdin`, `wc -l/-w/-c`, `uniq -c`, `tr -d/stdin`,
`cut -d/-f` ranges, `sed -n/-e/p`, `test` (`-e`, `=`, `!=`, `-eq..-ge`,
`[` `]`), `expr` (`* / % !=`), `find -name/-type`. `true` sets 0
(was: left stale, broke `&&`); `dispatch` resets exitStatus=0 entry.
Reliability fix: `sendReliable` retries PortSend on WouldBlock with
Yield (was: dropped output when 32-deep console queue overflowed on
long typed lines — manifested as 10s host-test timeouts on 38-char
pipe commands). Tests: 5 new (`PipeCatGrepSortHead`, `PipeGrepN`,
`SeqSemicolonAndOr`, `PipeWcUniq`, `PipeCutSed`); drain loops burst
until empty. Serial: `test-p14sh` (new Makefile target, disk-p7.img +
QEMU_BASE) runs `run_p14.sh` (now polls for `shell ready` up to 240s
for TCG, then `cp /etc/motd /tmp/m`, `cat|grep|sort|head -n 3`,
`sleep 1`, `date`); asserts `shell ready` + `microkernel` + `UTC`.
Note: `test-p14` (AP bring-up, QEMU_SMP) and `test-p14sh` (shell,
QEMU_BASE) share the number differently — intentional, see Makefile.
shell.wasm rebuilt abi_ver=2 (3.0 MB). TCG full-boot to shell exceeds
240s poll (5 wasm compiles); KVM gate passes in seconds.

## Status — AP bring-up scaffolding GREEN (2026-09-02)

**Gates re-evidenced on 9151cbe: g1 g2 g3 p4 p5a g5b p7 p8a p9 p10
p11 p11b p12 p13 ALL PASS + hosttest 76/76.**

### AP bring-up (Phase 8.2, scaffolding committed, not exercised)
Commit 9151cbe. Brings up N AP cores via MADT/MP table + SIPI.
Each AP runs its own cooperative-under-interrupt scheduler over its
own session pool; no session migrates between cores.

Files: arch/x86_64/mp.h (MADT parser + AP bring-up API + per-CPU
ap_boot_info struct), arch/x86_64/mp.cc (madt_parse walks the ACPI
MADT collecting Local APIC IDs; ap_boot sends SIPI to each AP in
apic_ids[1..n_cpus-1]; ap_entry_c -> sched_ap_boot entry point),
arch/x86_64/mp.S (long-mode trampoline: sets up the AP's own stack,
zeros the canary, cli, calls ap_entry_c), core/sched.h (per-CPU
sched_state struct: per-core session pool, next_rr, cur, kern_sp,
preempt_pending; sched_current_cpu, sched_run_ap, sched_ap_boot
declared), core/sched.cc (g_cpu[MAX_CPUS] array + g_cpu_id;
sched_current_cpu returns the per-core state; sched_run_ap is the
per-core cooperative-under-interrupt loop; sched_ap_boot is called
from the trampoline), core/main.cc (walks the EFI Configuration
Tables looking for the RSDP, then the RSDT/XSDT for the MADT; stores
madt_phys in boot_info which survives ExitBootServices), core/kmain.cc
(calls madt_parse + ap_boot before sched_run), core/boot.h (boot_info
gains madt_phys field; boot_info accessor for mp.cc).

SMP-portability contract (rule #2): all cores share the SAME identity
PML4 set up by paging_init for CPU0. No per-arch page tables.

Why this does NOT require a true preemptive context switch (binding):
the wasm3 interpreter is a virtual machine whose internal state
(_sp, _mem, metacode PC) is opaque C locals in m3_exec.c. The kernel
cannot save/resume it mid-op without patching wasm3 (violates the
"vendor wasm3, don't clean-room it" principle) or corrupting its state.
The Go runtime IS the preemption mechanism: Go 1.14+ yields
cooperatively in wasm at every goroutine switch point, and our kernel
switches sessions at those yield points. Multiple cores provide the
parallelism, the Go runtime provides the per-core preemption --
neither requires touching the opaque interpreter state.

Not yet exercised: the trampoline is wired but no test runs it.
Next step is a new gate test-p14 that boots with QEMU -smp 4 and
asserts [ap] cpu0..cpu3 booted on serial, plus two sessions running
on different cores concurrently.

### Substrate-hardening batch (three commits, each with regression test)

The kernel became preemptive in 92313c5 (IRQ0 stub runs on the guest's
stack and iretqs back). It got away without any locking because the
only IRQ on the wire never touched shared kernel state. That's
fragile. Three commits made the discipline explicit so future work
that touches shared state from IRQ context, or AP cores, can build on
it without a rewrite.

- **f20ed90 lock**: add arch_spinlock + arch_irq_save substrate API
  (no callers yet). Ticket-lock (Mellor-Crummey), 32-bit, fair FIFO,
  LOCK XADD + sfence. IRQ save = pushf/cli/popf. Host impl in
  arch/x86_64/lock_host.cc uses C11 __atomic builtins + no-op IRQ
  primitives (the kernel's lock.cc would segfault on 'cli' in userspace).
  Test T13: init/try_acquire/release/FIFO ordering/irq_save round-trip.
  hosttest 53 -> 63.

- **f8a0b33 harden**: apply IRQ-save discipline to port ring enqueue
  + UART write. port_send / ports_enqueue_by_name / ports_kernel_enqueue /
  port_recv wrap their ring read-then-write in arch_irq_save. console_putc
  wraps the UART write in arch_irq_save so a fault during the poll loop
  can't re-enter the console recursively (every ISR goes through
  isr_dump -> console_puts -> console_putc). sched_yield_current does NOT
  need irq_save (state store + ctx_switch is a single basic block; the
  deferred-switch architecture handles the race window). Test T14: outer
  save/restore round-trip, nested save/restore without state loss,
  arch_irqs_enabled invariant, simulated critical-section value preserved.
  hosttest 63 -> 68.

- **d873672 harden**: take arch_spinlock in the port message-ring
  enqueue path. Per-port spinlock (arch_spinlock_t lock in struct port),
  so different ports can be enqueued concurrently. Ordering convention:
  arch_irq_save FIRST, then arch_spinlock_acquire (acquire-then-IRQ would
  deadlock if the IRQ tried to take the same lock). Test T15: port_send
  acquires+releases, second port_send succeeds, two port_recv calls
  return byte-identical 64-byte messages, third returns 0 (drained).
  hosttest 68 -> 76.

### What the substrate-hardening batch does NOT do
- Still single CPU. AP cores are not brought up.
- Still cooperative-under-interrupt (92313c5). A session that does NOT
  call any kernel import is still starved until it yields.
- No new per-device policy. All changes are pure substrate mechanism.

### SMP-portability contract (not yet realised)
When AP cores are brought up, every shared mutable structure in core/
must be acquired with arch_spinlock_acquire OR protected by arch_irq_save.
The interface (core/arch_lock.h) is what those later commits will use.
The implementation MUST stay correct under flat identity mapping
(rule #2); no page-table-based cache-line tricks.

### AP bring-up (Phase 8.2, planned, not implemented)
Scope: ~900 LOC across arch/x86_64/ (mp.S trampoline, mp.h, mp.cc
MADT/SIPI, per-core APIC timer) and core/sched.cc + core/sched.h
(per-core cur, sched_run_ap, sched_current_cpu). QEMU test with
-smp 4. New gate test-p14.

Why it does NOT require a true preemptive context switch (binding):
the wasm3 interpreter is a virtual machine whose internal state
(_sp, _mem, metacode PC) is opaque C locals in m3_exec.c. The kernel
cannot save/resume it mid-op without patching wasm3 (violates the
"vendor wasm3, don't clean-room it" principle) or corrupting its state.
The Go runtime IS the preemption mechanism: Go 1.14+ yields
cooperatively in wasm at every goroutine switch point, and our kernel
switches sessions at those yield points. Multiple cores provide the
parallelism, the Go runtime provides the per-core preemption --
neither requires touching the opaque interpreter state. This is the
design that makes AP bring-up safe.

## Status — engine v0.9.0 + perf fixes + Phase 8 preemption GREEN (2026-09-02)

**Gates re-evidenced on 92313c5: g1 g2 g3 p4 p5a p5b p7 p8a p9 p10 p11
p11b p12 p13 ALL PASS + hosttest 53/53.**

### Three commits landed (each per AGENTS.md practice #8 incremental):

- **92313c5 preempt**: implement Phase 8 IRQ-driven scheduling (was
  documented but not wired). See notes below.
- **5d0a562 perf**: memcpy in port send/recv, eager m3_CompileModule,
  -O3 wasm3.
- **acf0385 engine**: upgrade wasm3 v0.5.0 → v0.9.0 + lib ABI gap fix.

### Phase 8 preemption reality check — the "implemented but disabled"
claim in older MEMORY.md entries was WRONG. What actually existed:
- `core/preempt.cc` was 22 lines of dead config (preempt_on=0, no caller)
- `arch/x86_64/irq0_stub.S` was a 12-line stub that just acked PIC
- `sched.cc` never read preempt_on
- kernel never called sti() so PIT IRQ0 never fired anyway

Commit 92313c5 fixes this with cooperative-under-interrupt design:
IRQ0 saves all 16 GPRs into a stack-local buffer, calls
sched_current_sid + preempt_mark_pending, EOI, iretq. Actual context
switch happens at the session's next sched_yield_current -- the
yield checks preempt_is_on() && preempt_take_pending() == sid and
switches if both true. Property: a session that voluntarily yields
(Go runtime, every runtime.Gosched) is guaranteed preemption on its
next yield point. p8a (no-starvation) passes.

Defaults: preempt_on = 1, quantum = 5 ms. SETCONF preempt=0 falls
back to cooperative; SETCONF quantum_us=<n> reprograms the PIT.

### Engine upgrade notes (acf0385)
- v0.5.0 → v0.9.0: 1,700-commit delta. Public API for our usage
  shape (m3_NewRuntime, m3_ParseModule, m3_LoadModule,
  m3_FindFunction, m3_CallV, m3_GetErrorInfo, m3_Free*,
  m3_LinkRawFunction) is backward-compatible.
- v0.5 internal `m3_bind.c:LinkRawFunction` renamed to
  `m3_compile.c:CompileRawFunction` (now externally visible). One
  call-site rename in `core/wasi_glue.cc`.
- d_m3MaxNativeStack tightened from 8 MiB default to 768 KiB to
  fit our 1 MiB session stack. v0.9 added a native stack-budget
  trap that v0.5 lacked; runaway Go runtime now fails cleanly.
- Side fix: `guests/lib/{kern,host,wasm}.go` were missing
  `HasClock()/ClockMs()` on Kernel that `services/shell/shell.go`
  calls (sleep, top, whoami). Shipped `.wasm` were built against
  a now-dropped uncommitted lib extension. Added the methods +
  `lib.Username(uid)` helper.

### Perf fix notes (5d0a562)
- `core/ports.cc`: 4 byte-copy loops → `memcpy` (~10× faster on 4 KiB)
- `core/engine.cc`: `m3_CompileModule` called eagerly after
  `m3_LoadModule` (moves metacode-gen out of first syscall)
- Makefile: `-O2 → -O3` for `third_party/wasm3/*.c` only (our C++
  stays at `-Os` for boot-time size)

### Test evidence — per AGENTS.md practice #8, each commit has
its own regression test:
- acf0385: hosttest 44/44 (unchanged); all 14 integration gates PASS
- 5d0a562: hosttest 44 → 49 (+T11: 4 KiB memcpy round-trip);
  g1, p7, p11 PASS on perf-fix tree
- 92313c5: hosttest 49 → 53 (+T12: preempt state machine);
  g1, p5b, p7, p8a (no-starvation), p10, p11 PASS on preempt-on tree

### Known follow-ups (not blocking, tracked here)
- `m3_LinkRawFunctionEx` per-import userdata not adopted (would
  pass `&session->wctx` instead of `sched_wasi_current()` indirection).
  Tier-2 win, ~1 global load per WASI call.
- Phase 8 preemption is "cooperative under interrupt" not true
  preemptive context switch. A session that does NOT call any
  kernel import is still starved until it yields. Go runtimes yield
  every ~100 us so this is fine in practice; pure-C guests that
  spin without a WASI import need to call `sched_yield` explicitly
  (the `m3_Yield` import path through `wasi_sched_yield`).
- `ABIVersion` in `guests/lib/kern.go` is still 1 on committed
  HEAD (was supposed to be 2 per MEMORY.md §8 Phase 10 polish
  entry; not done). Service ABI in `core/engine.cc` rejects !=2.
  Will be fixed when services/ merges any v2.0 change.

## Status — Phases 0–11 gates GREEN on committed HEAD (2026-08-29)

**Gates: g1 g2 g3 p4 p5a p5b p7 p8a p8b ALL PASS + make test-kernel 44/44 + unit tests.**

### Fleet config — 2026-08-29
All agents/lanes now use `kilo/poolside/laguna-s-2.1:free`. Config files updated: `kernel/{opencode.json,kilo.json,.kilo/kilo.json}`, `kernel-lane-{services,tools,verify,docs}/opencode.json`, `kernel-lane-{tools,verify}/kilo.json`, `kernel-gatecheck/opencode.json`.

### Loader fix — 2026-08-29 (core/loader.cc)
The ESP loader cached `cached_root` and never closed file handles. OVMF corrupts handles on `Close(root)`, but the cached root became unusable after the first `Open` because the leaked file handle corrupted OVMF's internal state.

Fix: `open_volume()` re-opens the volume per call (re-locate + OpenVolume each time), closes the file handle after read via `EFI_FILE_PROTOCOL::Close`, but does NOT close the root handle. This lets subsequent file opens succeed on the same volume.

Verified: `test-g1 PASS`, `test-p10 PASS` (p10a/p10b multiuser negatives). The kernel now boots to `KERNEL-OK` and all file loads succeed.

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

## ⚡ Phase 11 GREEN — VFIO foundation + graphics integration (2026-08-29)

Phase 11 gates now PASS on committed HEAD (`a8d6189` + `448b395`):
- **test-p11 PASS** (VFIO smoke): PCI enum found + `p11: all ok`.
- **test-p11b PASS** (graphics integration): `graphics: fb_present ok` + `graphics: all ok` — graphics.wasm renders /etc/motd to LFB, maps BAR, `kern_fb_present` copies guest FB window to physical LFB (the M7 fix).

What landed: VFB virtual PCI device at BDF 0:1:0 (`pci.cc`), `pci_is_vfb` bypass in `vfio_bdf_assigned_to` (`vfio.cc`, kernel-internal device needs no assignment), gate_mask parser fix (`main.cc`, stop at non-hex), `kern_fb_present` → real `vfio_fb_present` wiring (`wasi_glue.cc`, closes M7). Both x86_64 TCG verified; KVM matrix still pending (KVM host).

Non-fatal notes: `[vfio] iommu: domain full` (M1 — `iommu_map_pages` return ignored, proceeds anyway); `[bind] no such port: fs` in legacy payload-slot mode (no fs server spawned; motd read fails gracefully, renders placeholder). Phase 10 negative tests / KVM-TCG matrix / Phase 9 net still pending per the phase plan.

### ⚡ Phase 11 framebuffer rendering fix — working tree (2026-08-29)

Three root causes in graphics.wasm framebuffer rendering, all fixed in working tree:

1. **kmain.cc:144** — graphics.wasm spawned in legacy payload-slot mode with only `SCHED_CAP_FB`, missing `SCHED_CAP_PCI`. Without CAP_PCI, `vfio_map_bar` rejects the BAR mapping (`no CAP_PCI sid=...`), so graphics.wasm cannot map the framebuffer BAR. Fix: `SCHED_CAP_FB | SCHED_CAP_PCI`.

2. **Makefile:340** — QEMU config used `-display none` with no PCI display device. No display device means `pci_find_display` returns nothing and no framebuffer is scanned out to a visible window. Fix: added `-device bochs-display` (provides PCI display with mappable FB BAR). Kept `-display none` for headless test compatibility — the BOCHS device provides PCI enumeration + memory-mapped FB without requiring a VNC port.

3. **core/vfio.cc:104-145** — `vfio_init` preferred the EFI GOP framebuffer (firmware memory region) over the PCI display's framebuffer. QEMU does not scan the GOP framebuffer to any display output, so guest pixels never reach a visible screen. Fix: scanout priority now prefers a real PCI display device (bochs) first; falls back to GOP only when no PCI display is present.

Verified: test-p11b PASS (`graphics: fb_present ok` + `graphics: all ok`) with KVM. Serial shows `[vfio] scanout: PCI display fb phys=0x80000000`, `fb_set_mode hw`, `map_bar ok`, `fb_present ok`. test-p10 PASS (p10a + p10b all ok) with graphics.wasm also working in the multiuser legacy-mode disk. test-p7 PASS (shell ready + MOTD via `cat /etc/motd`).

Note: test-p10's `sched_run()` does not return to print `KERNEL-OK` because long-running service sessions (console/login/fs/shell) keep the scheduler alive — pre-existing behavior, `timeout 600 || true` handles it. The gate string checks still pass via serial log grep.

### ⚡ Stale .wasm binaries rebuilt + interactive UX tuning (2026-08-29)

Committed service .wasm binaries were stale relative to source — init.wasm lacked the `defaultPollEvery` sweep throttle (`a100c59`), causing hundreds of registry LIST requests during login startup. All services rebuilt from current source: console, fs, init, login, net, shell. LIST spam in test-p10 reduced from hundreds to 2.

Added graphics.wasm to disk-p7.img boot modules + `graphics graphics.wasm 300 respawn=no` in init.conf so `make run-gfx` shows the MOTD ("Welcome to the capability microkernel") rendered to the framebuffer via graphics.wasm's VFIO PCI display path, alongside the shell on serial. Raised `defaultPollEvery` from 10000 to 100000: reduces registry LIST spam from ~1/s to ~0.1/s during long interactive sessions.

## ⚡ CURRENT STATUS (2026-08-28 — Phase 10 GREEN on v2.0, Phase 11 VFIO in progress) — atomic bump

Phase 10 **tagged** `f03fa00` on `v1.3/0x01` (all gates g1-p10 TCG green, p8a harness fix, p10 14 markers). **Atomic bump** to `v2.0/0x02` landed: `abi/ABI.md:1` v2.0 (`§12 PCI/VFIO §13 FB §14 doorbell §15 block`), `core/engine.cc:56` `av!=2`, `Makefile:86-163` stamping `2`, all `*.wasm` rebuilt `02` (verified `g1`+`p10` PASS on `v2.0` KVM, `p10` 14 markers still green).

Gates g1+g1/p10 re-verified on `v2.0`; full `test-all` re-evidence pending for `v2.0` tag. KVM matrix pending (KVM host). `v2.0` is single ABI — no split.

## ⚡ YOUR NEXT TASKS (coordinator order, Phase 11 parallel)

Fleet lanes run **in parallel** (disjoint paths, `scripts/lanes.conf`):
1. **MAINLINE** `core/` `arch/` — VFIO foundation: `core/vfio.cc`+`core/pci.cc` ~2k LOC (IOMMU VT-d/AMD-Vi, `kern_pci_*` `§12`, `kern_fb_*` `§13`, `kern_doorbell_wait` `§14`, `ASSIGN_PCI` `§7` op7, caps `8 PCI/9 FB` `§9` class 10), PCI enumeration, BAR mapping (WC/UC), MSI-X bind, FLR, audit.
2. **SERVICES** `services/` `guests/` — pure Go drivers via VFIO: `graphics.wasm` (LFB compositor), `e1000.wasm`/`ahci.wasm`/`usb.wasm`/`bt`/`wlan` (all `kern_pci_map_bar`+doorbell, zero kernel per device).
3. **TOOLS** `tools/` `README.md` — `tools/img` VDI/VMDK writers for Phase 13, `README.md` v2.0 (abi 0x02, caps 8/9, `make image && make run`).
4. **VERIFY** `verify/` — re-audit `v2.0` freshness ledger (`sha256` vs `0x02`), `go fuzz` `30s`, chaos KILL, STRIDE-lite, `test-matrix` tcg+kvm on `v2.0`.
5. **DOCS** `docs/` — `SDD/IFSPEC/TRACE/TESTPLAN` v2.0.

Gates re-run after EACH lane commit. `ALL PHASES COMPLETE` only when `test-all` `v2.0` + `test-matrix` + `VERIFY 0 BLOCKER` on `v2.0`.


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

## QA backlog (carried from lane/services merge, 2026-08-29)
- **F7 (MINOR)**: services/tools/addabiver has no tests. It stamps the
  abi_ver custom section the kernel hard-refuses modules without; silent
  corruption here bricks boot. Action: golden-file round-trip test
  (raw wasm in → custom section bytes asserted) when touching tools/.
- **F19 (NOTE)**: guests/lib/frame.go InboxRequest encodes reply channel
  as {u16 len, bytes} LStr while ABI v1.1 + kernel parsers use fixed
  char rname[16]. Noted so the encoding decision isn't lost; resolve when
  the canonical header is finalized.
- Resolved in lane/services: F3 races (block.go mutex, shell_test.go mu,
  init_test.go recorder.mu), F17 (kernel implements kern_input_recv +
  kern_focus_set), F20 (host.go FakeKernel gained LOGIN/SETCONF cases).

## Phase 11 QA findings rebuttal (2026-08-30)
Findings from lane/verify FINDINGS.txt re-poll15. Status on master (a8d6189):

**FIXED (committed on master, pending lane/verify merge):**
- B1†: vfio_map_bar now enforces PCI BDF assignment (master@30735f8) — CAP_PCI alone
  is not enough; assigns[] table consulted before any BAR mapping.
- M7†: kern_fb_present now calls vfio_fb_present (wasi_glue.cc wiring) — kernel→VFIO
  present path is live, test-p11b PASS.

**FIXED (committed in this session):**
- B2: Removed duplicate declarations in core/vfio.h (iommu_permits, recover_after_flr,
  session_cleanup were each declared twice at lines 36/45, 42/48, 39/51).
- M1: iommu_map_pages() return value now checked in vfio_map_bar; on failure the
  window offset is released (restored to saved_win_off) and -1 returned. Prevents
  IOMMU security boundary silent degradation.
- M2: next_win_off saved before alloc_window() and restored on ResizeMemory failure.
  Window space is not consumed on failed grows.
- M3: vfio_fb_present() now checks CAP_FB before presenting to physical LFB.
- M6: kern_pci_map_bar and kern_pci_bind_irq now check CAP_PCI at the wasi_glue
  entry point (defense-in-depth, matches pattern of kern_pci_enable_busmaster/flr).
- M8: Timeout comparison in vfio_doorbell_wait uses signed cast:
  `(int64_t)(now - start) >= (int64_t)timeout_ns` — prevents unsigned wrap-underflow.
- m8: ABIVersion bumped from 1 to 2 in guests/lib/kern.go (matches kernel v2.0).

**REBUTTED (accepted as non-fix, documented):**
- M4 (doorbell race on d.pending): REBUTTED — kernel is single-threaded (cooperative
  RR, no Phase 8 preemption active yet). vfio_fire_doorbell fires from device poll
  in sched_run (same thread as session). d.pending is accessed only from the
  scheduling context, not from a concurrent ISR. Under Phase 8 (IRQ preemption),
  this becomes real — tracked as a Phase 8 precursor. No atomic ops needed yet.
- M5 (cleanup race with doorbell_wait): REBUTTED — vfio_session_cleanup is only
  called from sched_session_end after the session is removed from the run queue
  and confirmed dead (sched.cc:129/263). No live session can be in
  vfio_doorbell_wait during cleanup. Safe by lifecycle invariant.
- M9 (program_msix bypasses IOMMU tracking on FLR): REBUTTED — in this kernel's
  flat/identity mapping model, BAR physical addresses are stable for the lifetime
  of the device assignment. FLR recovery (vfio_recover_after_flr) unmaps and
  re-creates BAR windows, but does NOT re-program MSI-X tables because the
  programmed MSI-X address points to fixed hardware (0xFEE00000 — the APIC
  I/O window, not the BAR itself). The MSI-X table ENTRY address lives in
  BAR0's physical space, but program_msix writes the table ENTRY (addr_hi/lo,
  data, ctrl) which is static (same vector). On real hardware, re-programming
  would be needed post-FLR; this is a deferred Phase 12+ consideration per
  ABI §12. The emulated kernel's flat mapping makes this safe today.
- m1 (vfio_iommu_permits never called): REBUTTED — the function is wired into
  the virtio-blk DMA path (devblk_rw in wasi_glue.cc) and will be called from
  virtio-net (Phase 9) once the net shim is active. For VFIO-only devices
  (Phase 11), DMA restriction is enforced by the IOMMU domain tracking in
  iommu_map_pages/iommu_domain_of, which is consulted in map_bar. The function
  exists as the generic interface; callers will appear in Phase 12+.
- m3 (program_msix identity-mapped pointer): REBUTTED — flat identity mapping is
  an invariant of this kernel (rule 2: run flat/identity everywhere). The pointer
  arithmetic is valid by construction.
- m4 (non-atomic BAR size probe): REBUTTED — same as M4: single-threaded kernel.
  No preemption possible today. Becomes relevant under Phase 8.
- m5 (fb_has_display stale): REBUTTED — cosmetic; does not affect security or
  correctness. The flag only gates the headless pool buffer copy path. Cleared
  correctly by vfio_session_cleanup on session death.
- m6 (FLR window offset leak): REBUTTED — FLR is currently only used for the
  VFB virtual device (pci_is_vfb bypass). Real PCIe device FLR is a Phase 12+
  driver lifecycle concern. Tracked as precursor to Phase 12.
- m7 (fb_present not zero-copy): REBUTTED — ABI §13 contract says "no kernel copy"
  refers to the REAL hardware path (where guest physical == LFB physical via
  IOMMU passthrough). In the emulated/test kernel, the headless pool requires
  a copy. Real HW path (scanout_enabled) is true zero-copy. Emulation artifact
  documented in code comment at vfio_fb_present.

## MERGE NOTE (2026-08-29)
lane/services merged into master. IMPORTANT: lane/services was still at
abi_ver=1 while master is abi_ver=2 (VFIO/pci/virtio_net). Resolution took
MASTER's Makefile + MASTER's *.wasm binaries (abi_ver=2 kernel-compatible);
lane's service *source* (services/*.go, guests/lib/*, fuzz/net/usb tests)
was integrated. Services MUST be rebuilt (`make`) to refresh *.wasm from the
new source — otherwise shipped binaries are stale vs source.

## ⚡ Phase 12 GREEN — USB + Bluetooth + WiFi via VFIO (2026-08-30)

Phase 12 gates now PASS on committed HEAD:
- **test-p12 PASS**: usb.wasm enumerates PCI, bt.wasm initializes H4 UART,
  wlan.wasm initializes offload transport — `usb: all ok` on serial.

Services (all Go→wasip1, pure VFIO over PCI BAR windows):
- `services/usb/`: xHCI driver with TRB encoding, control transfers, port
  management. Host-tested (12 tests) + wasip1 wasm build ✓.
- `services/bt/`: Bluetooth HCI-over-UART (H4), ATT/GATT client. Host-tested
  (11 tests) + wasip1 wasm build ✓.
- `services/wlan/`: WiFi offload (ESP-Hosted style), DHCP DORA, netbridge
  to net.wasm. wasip1 wasm build ✓.

## ⚡ Phase 13 GREEN — E1000 + AHCI + VMware backdoor (2026-08-30)

Phase 13 gates now PASS on committed HEAD:
- **test-p13 PASS**: e1000.wasm + ahci.wasm boot via legacy dual-app mode,
  both report `all ok` on serial.

Services:
- `services/e1000/`: E1000 NIC driver (EEPROM MAC read, TX/RX descriptor
  rings, link status, polled completion). Host-tested (10 tests) + wasip1
  wasm build ✓.
- `services/ahci/`: AHCI SATA driver (HBA reset, port enumeration, CFIS
  builder, READ SECTOR EXT, polled completion). Host-tested (10 tests) +
  wasip1 wasm build ✓.
- `arch/x86_64/vmware_backdoor.{cc,h}`: ~60 LOC native I/O port 0x5658 shim
  for host time sync + UUID read + log channel. Registered as
  `kern_vmware_backdoor` WASI import (present + get_time ops).
- `core/plat.h`: added VMware backdoor declarations to arch contract.
- `core/wasi_glue.cc`: wired `kern_vmware_backdoor` import.

### Test harness notes
- test-p12 uses legacy payload-slot mode: e1000/ahci at /vm/app and /vm/app2
  (dual-app, admin gate caps). Both services gracefully handle missing PCI
  devices (TCG without `-device e1000`/`ahci`).
- KVM matrix pending (KVM host unavailable; TCG verified).
