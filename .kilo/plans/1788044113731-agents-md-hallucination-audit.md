# Hallucination Audit — AGENTS.md vs. Codebase Reality

Audit date: 2026-08-80
Scope: Cross-check every factual claim in AGENTS.md against the actual filesystem, git history, MEMORY.md, and ABI.md.

## Hallucinations found

### H1 — `capreg` (line 482) — **FABRICATED FILE**
AGENTS.md target repo map lists `core/ ... capreg ...` as if `capreg.cc`/`capreg.h` exist.
**Reality:** No file named `capreg` exists anywhere in the repo — not in `core/`, not in git
history, not in any `.cc`/`.h`/`.md`. The capability registry lives entirely in
`core/kernsvc.cc` (registry/devman/power endpoints) and `core/ports.h`. `capreg` is a
phantom; the target map should read `kernsvc` or simply drop the token.

### H2 — `boot.S` (lines 54, 481) — **FABRICATED FILE**
AGENTS.md lists `boot.S` as an `arch/x86_64/` shim ("`boot.S · uart · timer · traps ·
bootinfo`").
**Reality:** No `boot.S` has ever existed in git history. The actual boot/entry asm in
`arch/x86_64/` is: `ctx.S` (context switch), `ctxswap.S`, `traps.S` (IDT/exception
stubs), `irq0_stub.S` (timer IRQ wrapper). The target map is wrong — there is no boot.S.

### H3 — `bootinfo` as an arch file (lines 54, 481) — **FABRICATED FILE**
Same claim as H2: `bootinfo` listed as an `arch/x86_64/` shim.
**Reality:** `boot_info` is a *struct* defined in `core/boot.h:7`, produced by
`core/main.cc:18` (`static struct boot_info g_bi;`). It is not an arch file and never was.
The arch directory has no `bootinfo` anything.

### H4 — `kernel/*.cpp` (line 57) — **STALE PATH / WRONG EXTENSION**
AGENTS.md rule 3: "kernel/*.cpp does mechanism only".
**Reality:** `kernel/` contains only `link.ld`. There are zero `.cpp` files in the repo.
The C++ substrate lives in `core/*.cc` (CXXFLAGS compile `core/%.cc`). Rule 3 should read
`core/*.cc`.

### H5 — `kernel/vm/` in Phase-5 retirement (line 155) — **WRONG PATH**
"Retire 8-opcode artifacts if not already: `kernel/vm/` tools/vasm programs/demo.*".
**Reality:** The VM was renamed `kernel/vm/` → `core/vm/` in Phase 2 (commit 1d92083),
then retired from `core/vm/` in Phase 5 (commit da43b41: "RETIRED: core/vm, tools/vasm,
program*"). The AGENTS.md path `kernel/vm/` was already stale at Phase-5 time — should be
`core/vm/`.

### H6 — `kernel/main.c` referenced as current (line 84) — **STALE PATH**
Phase-1 step: "`kernel/main.c`: delete Go marker/handoff".
**Reality:** Was `kernel/main.c` in Phase 0; renamed `core/main.cc` in Phase 2. The
Phase-1 instructions are historical (correct for their time) but the AGENTS.md text
presents the phase plan as a flat checklist without noting that the `kernel/` → `core/`
rename in Phase 2 makes later-phase `kernel/` references (H4, H5) wrong.

### H7 — `gdt_idt` in MEMORY.md "Survives" list (MEMORY.md:163) — **STALE REFERENCE**
"Survives: ... `kernel/{main,serial,cpu,mm,gdt_idt,lib,loader}`".
**Reality:** `kernel/gdt_idt.c` was merged into `arch/x86_64/traps.cc` in Phase 2
(`kernel/gdt_idt.c => arch/x86_64/traps.cc`, commit 1d92083). `gdt_idt.c` no longer
survives as a separate file. (Note: this is in MEMORY.md, not AGENTS.md, but MEMORY.md is
the paired document and the same agent that wrote AGENTS.md wrote MEMORY.md.)

### H8 — `goshim` deletion claimed in Phase 2 (lines 96–98) — **ALREADY DONE / REDUNDANT**
Phase-2 step: "Replace `tools/goshim` (Plan 9 IDT bank) with GNU as `.S` vectors" and
"Delete: ... `tools/goshim/`".
**Reality:** `tools/goshim/` was already deleted in Phase 2 (commit 1d92083). The
Phase-2 steps in AGENTS.md describe work that is already complete, but unlike H6 this one
isn't actively misleading — it's a historical instruction. Flagging as low severity.

## Non-hallucinations (verified correct)

- `gokernel/`, `scripts/goaddr.sh`, `GO_MAGIC`/`go_entry_fn` markers — existed in Phase 0,
  deleted in Phase 1/2. Correctly described as retired.
- `kernel.elf`/`goaddr.mk` — existed in Phase 0 Makefile, removed in Phase 1. Correct.
- `tools/vasm`, `programs/demo.*` — existed in Phase 0, retired in Phase 5. Correct.
- ABI.md §1–§15 exist and match the section references in AGENTS.md (§1 ports, §3 block,
  §4 input, §5 timer, §6 net, §7 service ports, §8 driver model, §9.FB framebuffer).
- ABI v2.0 ratification date (2026-08-27) and content match ABI.md.
- `core/` is arch-blind: confirmed zero `#ifdef __x86_64__` / `__aarch64__` in `core/*.cc`.
- `arch/x86_64/` contains only shims (uart, cpu, traps, paging, timer, vector, ctx).
- The frozen WASI profile (`fd_write proc_exit clock_time_get random_get args_get
  args_sizes_get environ_* sched_yield`) matches `core/wasi_glue.cc:1-3`.
- Guest ladder (C/Rust/Go → wasm) all present under `guests/`.
- Services present: console, login, fs, init, shell, net, graphics, display, usb — all
  build to `.wasm`. `bt/`, `wlan/`, `e1000/`, `ahci/` services do NOT exist yet
  (Phase 12/13), which is consistent with them being future phases.
- `mkpefi.py` is still used in the Makefile (line 187), NOT replaced by `tools/img` —
  AGENTS.md line 487 says "replaces mkpefi glue later", which is a forward-looking claim
  (Phase 10 / tools/img), not a current fact. Not a hallucination, but `tools/img` has
  not retired mkpefi.py yet.

## Recommended fixes (priority order)

1. **H1** — Replace `capreg` with `kernsvc` in the target repo map (line 482), or drop it.
2. **H2/H3** — Replace `boot.S · bootinfo` with the actual arch entry files
   (`ctx.S · traps.S · cpu.cc`) in both the rule-1 bullet (line 54) and the repo map
   (line 481).
3. **H4** — Change `kernel/*.cpp` → `core/*.cc` in rule 3 (line 57).
4. **H5** — Change `kernel/vm/` → `core/vm/` in the Phase-5 retirement step (line 155).
5. **H7** — Update MEMORY.md:163 to drop `gdt_idt` from the "Survives" list (it's in
   `arch/x86_64/traps.cc` now).
6. **H6/H8** — Low severity; consider adding a note that Phase-1/2 `kernel/` paths
   were renamed to `core/` in Phase 2, so readers don't go looking for `kernel/`.
