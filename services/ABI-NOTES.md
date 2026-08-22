# services/ABI-NOTES.md — lane-services protocol notes

`abi/ABI.md` v1 is FROZEN and untouched. This file records (a) the
concrete instantiations of under-specified ABI points that lane MAINLINE
must mirror on the kernel side, and (b) lane-local service protocols.
Nothing here relaxes or extends the ABI.

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

## 2. Reply routing for user-level servers (ABI §1 gap)

§7 kernel endpoints reply onto the sending handle because dispatch is
inline (core/kernsvc.cc). User servers have no such visibility: §1
datagrams carry no reply-to metadata, and the kernel has no unbind —
binding a fresh alias per request would exhaust the 8-handles/session
budget. Convention adopted by fs/login/shell:

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

## 5. Login AUTH protocol v0 (lane-local)

Login owns "login". Request: `{u16 op=1, u16 seq, u16 inboxLen, inbox,
{u8 nameLen, name, u8 passLen, pass}}`. Reply: `{op, seq, i32 status,
u64 capmask, u32 sid}` — status 0 ok / -1 unknown user; sid is the SPAWNed
shell session (0xFFFFFFFF if spawn was denied). Password check is stubbed
(accept-any) until Phase 10 hashes. Login requests registry SPAWN of the
"shell" module with the USER's capmask; the kernel's never-more-than-
caller rule means the boot-preloaded login session itself needs
CAP_SPAWN plus at least the masks it hands out.

## 6. init.conf / kernel.conf transport

init receives its config text via WASI argv: `argv[1] = init.conf text`,
optional `argv[2] = kernel.conf text` (loader preloads both files from
the ESP per AGENTS.md). init.conf line format:

    <name> <path> <capmask-hex> [respawn=yes|no]

kernel.conf `key=value` lines are applied via a proposed registry op 5
SETCONF `{u16 kLen,key,valLen,val}`. The v1 kernel has no SETCONF —
init logs `[init] knob <k>=<v> rejected (registry lacks SETCONF)` and
continues; MAINLINE may adopt op 5 in a later ABI note. Nothing else in
this file depends on it.

## 7. hosteng validation status

`/home/cyr/kernel/build/hosteng` did not exist when this lane ran;
per instructions the engine build was NOT attempted from this worktree.
All modules were instead validated by exhaustive host tests
(`kern.Bus` mirrors core/ports.cc semantics decision-for-decision) and
by GOOS=wasip1 compile checks. Once hosteng exists:
`hosteng services/<svc>/<svc>.wasm` should print the module's own log
lines through stub imports (console/fs/login/shell/init each start with
a bracket-tagged banner).
