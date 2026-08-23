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

## 4. Multiuser rooting (IMPLEMENTED, extended mandate)

§4 INPUT RECORD TRANSITION NOTE (v1.3): the ratified v1.3 record is
6 bytes {u8 kind, u8 mods, u16 scan, u16 codepoint}; the DEPLOYED
kernel still emits the 4-byte v1 form. guests/lib decodes BOTH
(DecodeInputEvent / RecvBufLen) so lib-built guests survive the
kernel-side flip without recompilation; Encode stays at the deployed
4-byte form (input records are kernel→guest only). When MAIN lands
v1.3 emission, no SVC change is required — verify via shell input.


With v1.1's kernel-stamped uid in every canonical header, fs.wasm now
enforces per-uid policy server-side:

- REGISTER (op 8, lane-local): `{u32 uid, u16 nLen, name, u64 capmask}`
  — issued by login/init after auth; feeds the uid→(name,capmask) table.
  ISSUER GATE: accepted only from the privileged session
  (kernel-stamped uid 0); any other caller receives FSAccess (-10).
  Names are the rooting key (/home/<name>), so registration authority
  is a security boundary — a guest self-registering with another user's
  name must not inherit its root.
- uid 0 = admin: unrestricted.
- registered user: relative paths rooted at /home/<name>; own subtree
  fully writable; /tmp world-writable; /etc + /boot writes need
  CAP_FS_ADMIN (bit4 of the registered mask); /boot read-only;
  OTHER users' homes return FSNoEntry (existence hidden).
- unregistered uid: guest — reads on /etc,/boot,/tmp; /tmp writes only.
- New status FSAccess = -10 ("permission denied").

Standard skeleton (/etc,/home,/tmp,/boot/modules) is auto-provisioned on
first mount of a fresh volume.

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

## 10. "net" socket service protocol v0 (extended-mandate lane contract)

Served by net.wasm on the well-known "net" port over the canonical
header. Payload integers LE; IP addresses raw 4-byte arrays.

	OPEN   {u16 kind(0=tcp,1=udp), u16 port}  -> {i32 st, u16 sock}
	       port=0 → outbound TCP socket (CONNECT next); nonzero → listen/bind
	CONNECT{u16 sock, u8 ip[4], u16 rport}    -> {i32 st}
	SEND   {u16 sock, u16 len, data}          -> {i32 st}
	RECV   {u16 sock, u16 max}                -> {i32 st, u16 got, data[got]}
	CLOSE  {u16 sock}                         -> {i32 st}

st: 0 ok, -1 no-such-socket, -2 bad-op, -3 state. RECV with an empty
buffer is st=0/got=0 (poll semantics). CLOSE returns immediately; the
TCP FIN is deferred until all queued/unacked stream bytes have drained
(no silent tail loss on window-limited sends), and a FIN carrying data
delivers that data before remote-close surfaces via RECV=got 0.
UDP OPEN on an already-bound port shares the existing receive queue
(v1 semantics: no exclusive-bind error); TCP LISTEN on a bound port
fails with st=-3. Guests use `kern.NetClient`
(guests/lib) instead of hand-framing.

Wire fidelity record: guests/lib/frame.go implements the ratified v1.1
canonical header exactly ({op@0,seq@2,uid@4,rname[16]@8}, payload@24,
all integers LE) — audited against abi/ABI.md §1 this cycle, including
LE multi-byte decode order and NUL-termination bounds of char[16].

§6 window instantiation: devman ENUM class=net records ordered by
instance — [0]=RX ring, [1]=TX ring; each mapped into the session's own
linear memory at win_off (same unsafe-slice rationale as pre-v1.1 block;
a managed-runtime-safe import pair is proposed for §11/v2 alongside the
reply-cap work). Address assignment via argv[1]="<mac> <ip>".

## 11. Display face (extended mandate)

services/display implements the §9.FB consumer: an 8×16-glyph text-mode
terminal (80×25 default grid) over the framebuffer window — public-domain
8×8 font doubled vertically, ANSI-lite SGR (0 reset, 1 bright,
30-37/40-47 colors), scroll-on-overflow, tab stops, backspace, and
damage-tracked Flush() issuing one UPDATE_RECT (FLIP only when
double-buffering is negotiated). Headless boots (width==0) degrade every
op to a silent no-op.

Two consumers share the terminal package
(kernel.lane/services/display/terminal):
- display.wasm — standalone: binds the "console" relay and renders.
- console.wasm — when devman ENUM reports a §9.FB device, every relayed
  line is mirrored into a Terminal via Options.Mirror (the "display"
  output face); headless boots skip it entirely.

Host harness: terminal.NewFake(w,h,caps) completes the §9.FB mailbox in
a goroutine exactly like the kernel shim, so go test -race exercises
identical window semantics; pixel-level assertions verify glyph bits,
EGA palette values and UPDATE_RECT arguments.
