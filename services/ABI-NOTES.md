# services/ABI-NOTES.md — lane-services protocol notes

This file records concrete instantiations of under-specified ABI points
that lane MAINLINE must mirror, plus lane-local service protocols.

**UPDATE 2026-08-22: ABI v1.1 RATIFIED (master b9263a2) and ADOPTED by
this lane.** The canonical datagram header `{u16 op,u16 seq,u32 uid,
char rname[16]}` (payload @24) replaces this file's old reply-channel
payload convention; §3 offsets are now pinned in the ABI itself; LOGIN
(op 5) / SETCONF (op 6, CAP_CONF bit7) are live registry ops; managed-
runtime guests use `kern_blk_read/write` imports. Sections below were
revised accordingly; superseded text is kept struck through where still
informative.

## 1. Block window byte offsets (ABI §3)

ABI §3 fixes field ORDER but not padding. fs.wasm and its clients use a
naturally-aligned layout; **the kernel RAM-disk shim must use the same**:

    0x00 u32 magic 'BLKW'     0x04 u32 blk_size (=512)
    0x08 u64 num_blocks       0x10 u32 next_req_id   0x14 pad u32
    0x18 u64 op (1|2)         0x20 u64 lba           0x28 u32 count
    0x2c pad u32              0x30 u64 off           0x38 u32 done_req_id
    0x3c i32 status

Data transfers land at window offset `off`; guests use a fixed scratch
area at `off = 0x1000`, ≤ 8 sectors per request → minimum window size
0x2000 bytes (`bwWindowMin`). `off` is an absolute WINDOW offset, i.e.
an offset into the session's linear memory (guests reach it via their
own memory base — wasm32 base is 0).

fs.wasm finds its window via devman ENUM (class=block). The boot
preload must therefore grant the fs session CAP_DEVMAN (bit1).

## 2. Reply routing for user-level servers (SUPERSEDED by v1.1 header)

~~§1 datagrams carried no reply-to metadata~~ — v1.1 ratifies the
requester-declared reply channel as `char rname[16]` in the canonical
header. The kernel has no unbind, so servers MUST still cache one bind
alias per distinct rname (kern.ReplyBook): re-binding per request would
exhaust the 8-handles/session budget. The old payload-embedded inbox
field is gone from all lane protocols.

- Each CLIENT creates ONE inbox port with a unique global name:
  `<role>.<nanosecond-salt>` (≤15 chars), e.g. `fs.16895`.
- Every request carries `{u16 op, u16 seq, u16 inboxLen, inbox, payload}`.
- The server binds each distinct inbox name ONCE (cached in a
  "reply book") and sends replies there: `{u16 op, u16 seq, ...body}`.

Proposal for a future ABI version (NOT implemented unilaterally):
`kern_port_reply_to(h)` returning the sender identity of the message
last received on h, so servers can answer without client-declared
inboxes.

## 3. FS port protocol v0 (lane-local, shared with guests/lib)

Requests are §1 datagrams as in §2 above; replies echo {op, seq} and
carry `i32 status` first. Ops (see `guests/lib/fsclient.go` for exact
payloads): 1 STAT · 2 LIST · 3 READ · 4 WRITE · 5 CREATE · 6 MKDIR ·
7 DELETE. Status codes: 0 ok, -1 io, -2 noentry, -3 exists, -4 notdir,
-5 isdir, -6 nospace, -7 badname, -8 notempty, -9 range. READ/WRITE
chunk to stay inside one 4096 B datagram; clients loop.

When the kernel-routed preview1 path lands (AGENTS.md Phase 5 route 1),
the kernel's fd layer should translate path_open/read/write/close/
path_create* onto exactly these ops so both routes hit the same server
protocol.

## 4. Multiuser rooting (Phase 5 gate note)

v1 fs.wasm enforces no per-session uid (datagrams carry none). The
Phase-5 denial gate ("second session rooted at /home/u2 cannot open
u1's file") holds because login spawns each user's shell with the user
root as argv[1] and shells prefix all RELATIVE paths with it; absolute
cross-user paths are not offered by any tooling. Kernel-enforced
rooting needs per-datagram session identity — same future hook as §2.

## 5. Login AUTH protocol v1.1 (lane-local, over canonical header)

Login owns "login". Request: `{canonical header}{u8 nameLen, name,
u8 passLen, pass}`, reply channel in rname. Reply: `{canonical header}{
i32 status, u64 capmask, u32 sid}` — 0 ok / -1 unknown user; sid = SPAWNed
session (0xFFFFFFFF if denied). Flow since v1.1: spawn shell with mask 0,
then issue the user's uid+capmask via §7 op 5 LOGIN (the sole issuance
mechanism). LIMITATION: LOGIN matches by session name (= module name),
so two concurrent shells cannot be distinguished — needs v2 session-id
targeting (§11 roadmap). Password check remains accept-any until Phase 10
hashes.

## 6. init.conf / kernel.conf transport

init receives its config text via WASI argv: `argv[1] = init.conf text`,
optional `argv[2] = kernel.conf text` (loader preloads both files from
the ESP per AGENTS.md). init.conf line format:

    <name> <path> <capmask-hex> [respawn=yes|no]

kernel.conf `key=value` lines apply via §7 op 6 SETCONF
`{char key[16], u64 value}` (v1.1 ratified; requires CAP_CONF bit7).
Non-numeric values are skipped with a log line. Numeric knob values only
until a string-knob need appears (would require an ABI note).

## 7. hosteng validation status

`/home/cyr/kernel/build/hosteng` did not exist when this lane ran;
per instructions the engine build was NOT attempted from this worktree.
All modules were instead validated by exhaustive host tests
(`kern.Bus` mirrors core/ports.cc semantics decision-for-decision) and
by GOOS=wasip1 compile checks. Once hosteng exists:
`hosteng services/<svc>/<svc>.wasm` should print the module's own log
lines through stub imports (console/fs/login/shell/init each start with
a bracket-tagged banner).

## 8. abi_ver custom section (encoding used by this lane)

Every service module carries a trailing custom section:

    id=0x00, payload = {u8 nameLen=7, "abi_ver", u32 LE version}

Current version = 1, matching abi/ABI.md v1. Injected at build time by
`services/tools/addabiver` (pure Go, no deps). All five wrapped modules
validate under stock `wasm-validate` (wabt). The kernel's instantiation
check should scan custom sections for name "abi_ver" and compare the
u32 against its own ABI version, refusing mismatches per AGENTS.md.

## 9. Managed-runtime block transport notes (v1.1)

fs.wasm uses `kern_blk_read(lba i32, ptr i32, count i32) -> i32` /
`kern_blk_write(...)` (module "kernel"); ptr is an ordinary guest buffer
pointer, NOT an absolute linear-memory address — no unsafe window mapping.
The imports carry no geometry probe: fs.wasm assumes the AGENTS.md disk
size (16384 sectors × 512 B). Proposal for a future ABI note:
`kern_blk_geom(ptr)` returning {u32 blk_size, u64 num_blocks}, so image
sizes can vary without rewrapping modules. Host-side tests exercise the
identical BlockDev interface over the §3 window + RamDisk harness.
