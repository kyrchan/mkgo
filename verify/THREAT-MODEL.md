# THREAT-MODEL.md — STRIDE-lite pass (VERIFY lane)
# Scope per AGENTS.md practice #6: registry + port routing + fs rooting.
# Produced 2026-08-23 against MAIN@ab269c7 (+dirty). Consumed by MAINLINE.
# Legend: [F#] = existing FINDINGS.txt entry; NEW = gap with no finding yet.

## Assets & trust boundaries
- Capability registry: session→(uid,capmask), sole issuance via §7 op5 LOGIN.
- Kernel port table: 24 ports × 32-slot queues; kernel endpoints registry/devman/power.
- Block backends: RAM disk + virtio-blk persistent disk behind kern_blk_* / §3 window.
- fs.wasm policy layer: uid→root routing, CAP_FS_ADMIN gates.
- Boundary 1: guest wasm linear memory ↔ kernel (raw imports).
- Boundary 2: session ↔ session (ports, block device, filesystem namespace).

## Spoofing
- S1 [F32·BLOCKER] Direct-port requests carry client-authored uid; ratified
  kernel-stamp ("OVERWRITES uid on send") NOT implemented in port_send → any
  session forges uid=0 (admin root "/") or another user's uid at fs.
- S2 [F13·BLOCKER] LOGIN gate uses ports_name_owned_by (any handle to the
  name passes) → any session can bind "login" and mint arbitrary capmasks.
- S3 [NEW·low] kernsvc_reply enqueues by rname with from_sid=0; a spoofer who
  guesses/polls another session's inbox name receives that session's replies
  (reply confusion). Mitigated by create-fails-if-taken, but binder fan-in is
  not excluded.

## Tampering
- T1 [F12·BLOCKER] §3 mailbox bounds checks wrap at u64 (lba+cnt, off+cnt*512)
  → wild read/write outside guest linear memory; op=write copies kernel-heap
  bytes INTO the shared RAM disk (cross-session disclosure).
- T2 [F45·MAJOR] virtio-blk completion path ignores the device status byte and
  skips waiting after the first request → silent persistence of stale/corrupt
  sectors; also missing NEXT flag truncates the descriptor chain.

## Repudiation
- R1 [F18·MAJOR-class] Rejected §7 ops are audited but never answered
  (contract requires status -1): clients cannot distinguish denial from loss;
  audit trail exists but has no consumer-visible ack semantics.
- R2 [NEW·low] LOGIN success/failure transitions identity silently beyond one
  console_puts line; no durable record of old→new (uid,capmask) pairs for
  Phase-10 audits.

## Information disclosure
- I1 [F31·BLOCKER] kern_blk_read/write ungated ((void)sid) → any session reads/
  overwrites raw sectors of the shared disk incl. other users' /home and /etc;
  now reaches PERSISTENT storage post-virtio-blk.
- I2 [F16·BLOCKER] routed_rw copies min(n,len) from static fsresp without the
  reply-length clamp → kernel .bss overread into guest buffers, including
  stale bytes of OTHER sessions' earlier fs replies.
- I3 [F34/F25·MINOR] FAT allocClus never zeroes recycled clusters → POSIX-zero
  gaps disclose prior file contents cross-file.
- I4 [F29a·MINOR] Ungated serial debug prints expose kernel heap pointers.

## Denial of service
- D1 [F44·MINOR] quantum_us<1000 underflows quantum_ticks→0: preemption every
  IRQ tick (scheduler thrash).
- D2 [NEW·med] fsroute_wait spins up to 100M scheduler yields on missing
  reply; combined with F28's unconditional interception consumption, a guest
  can pin the CPU by issuing routed ops whose replies it suppresses.
- D3 [NEW·low] port queues bounded at 32 but rt_malloc'd msgs are freed only
  on recv; a session that floods then dies leaks its queue (msgs never freed,
  queue never drained) — bounded by MAX_PORTS*MAX_Q but permanent until reboot.

## Elevation of privilege
- E1 = S2 (LOGIN escalation) — full capmask minting.
- E2 = I1 (blk imports) — persistence-level write access equals FS_ADMIN.
- E3 [F37·MINOR] Gate payload "ppa" spawned with KILL|DEVMAN|POWER|SPAWN —
  any module placed in /vm/app gains admin-grade rights by boot convention.
- E4 [NEW·med] SPAWN privilege check uses caller capmask correctly, but the
  spawned session's argv/env come from the caller payload unchecked; combined
  with E1-style uid forgery pre-F32, init-spawned children can be aimed at
  other users' roots.

## Priority order (merge-blocking first)
1. F32 stamp (S1) — kills the whole spoofing class at one point.
2. F13 creator-gate (E1/S2) — closes capability minting.
3. F31 gate (I1/E2) — restrict blk imports to fs session or CAP_FS_ADMIN.
4. F12 overflow checks (T1) — restore memory-safety invariant.
5. F16 clamp (I2) + F45 virtio completion (T2) — data-integrity pair.
6. F18 status -1 (R1/D-prevent) — conformance + fast-fail.
7. D2/D3/R2/I3/I4/S3/E3/E4 — hardening backlog (Phase 10 natural home).
