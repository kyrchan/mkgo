# abi/ABI.md — guest-facing interface contracts (v1.3)

Binding on both kernel substrate and all services/guests. Changes require a
version bump here + note in MEMORY.md. All integers little-endian (wasm
native). No NUL-terminated strings anywhere: lengths are explicit.
"Window" = a region of a session's linear memory the kernel assigns at
instantiation; guests never compute absolute addresses, only window offsets.

## v1.3 changelog (RATIFIED by project owner, 2026-08-23)

- §4 input records gain `u16 scan` — raw i8042 scancode (set 1, as
  emitted untranslated by the shim). Record layout becomes:
  `{u8 kind, u8 mods, u16 scan, u16 codepoint}` (6 bytes). `codepoint`
  remains the kernel's US-layout mapping for backward compatibility;
  keyboard LAYOUTS become userland policy: sessions load keymap tables
  from `/etc/keymaps/<layout>` and translate from `scan` when a non-US
  layout is active. Rationale: layouts are userland data, not kernel
  policy.

## v1.2 changelog (RATIFIED by project owner, 2026-08-22)

- §9 class 9 FRAMEBUFFER is now DEFINED (layout in §9). Backends: Bochs
  DISPI LFB first (QEMU `-device bochs-display`, VirtualBox), VMware
  SVGA II FIFO second (Phase 12). Consumers: `display.wasm` text
  terminal / future compositor — layer-2 policy per §8.

## v1.1 changelog (RATIFIED by project owner, 2026-08-22 — all lanes adopt)

- §7 registry gains op 5 = LOGIN {char name[16], u32 uid, u32 capmask}
  -> status. Callable ONLY by the session that owns the "login"
  well-known name (kernel-checked). The named session (matched by its
  unique session name, i.e. argv[0]) receives uid + capmask; this is the
  sole mechanism by which capabilities are issued (at login, never
  otherwise). Motivated by Phase 5 multiuser stub.
- §7 registry gains op 6 = SETCONF {char key[16], u64 value} -> status.
  Applies kernel knobs (quantum_ms, log_level). Requires new capability
  bit7 CONF; init.wasm is the intended caller (`/etc/kernel.conf`).
- Service modules MUST carry a custom section `abi_ver` whose payload
  starts with byte 0x01; the kernel refuses modules without it.
- Block-class transport for MANAGED-RUNTIME guests (Go): instead of the
  §3 in-linear-memory window (whose address range cannot be reserved
  against a compacting/moving guest heap), such guests use imports
    kern_blk_read(lba i32, ptr i32, count i32) -> i32   // 0 ok | -1 err
    kern_blk_write(lba i32, ptr i32, count i32) -> i32
  served by the same kernel backends (RAM-disk now, virtio-blk in Phase
  8). Backend swap remains invisible; the §3 window stays the contract
  for raw/native guests. request datagrams to services carry
  `{u16 op, u16 seq, u32 uid, char rname[16], ...}`; the responder sends
  its reply into the existing port named `rname` (created by the
  requester). Empty rname = synchronous transport (kernel-routed calls),
  no port reply. Prevents same-queue request/reply interleave.
- Canonical datagram header RATIFIED for ALL port traffic (§1): every
  datagram starts `{u16 op, u16 seq, u32 uid, char rname[16]}`, payload
  at byte offset 24. On SEND the kernel OVERWRITES `uid` with the sending
  session's registry uid — clients can never spoof identity. `rname` may
  be empty (synchronous kernel-routed calls). Kernel-owned endpoints
  (§7) reply inline on the sending handle and ignore `rname`.
- §3 block-window offsets PINNED to naturally-aligned layout (see §3);
  guest scratch area at window offset 0x1000; minimum window size 0x2000;
  fs session receives CAP_DEVMAN at boot so devman ENUM can locate its
  block instance.
- v2 roadmap recorded in §11: one-shot reply capabilities
  (`kern_port_reply(h, ptr, len)`), LIST/CAPS capability gating at Phase
  10, IRQ arm flags post-Phase-9, class layouts per §9.

## 1. Message ports (binding from Phase 4)

Extra imports appended to the mini-WASI profile (prefix `kern_`):

    kern_port_create(name_ptr, name_len) -> i32   // own endpoint; -1 on err
    kern_port_bind(name_ptr, name_len)   -> i32   // attach to existing name
    kern_port_send(h, ptr, len)          -> i32   // 0 ok | -2 would-block | -1 err
    kern_port_recv(h, ptr, cap)          -> i32   // >0 len | 0 none | -1 err

Semantics:
- Datagram style, FIFO per sender→receiver pair, kernel-mediated copy only
  (sessions never share memory). Max payload 4096 bytes per message.
- `recv` never blocks; poll with `sched_yield`.
- One name may have many binders (fan-in); a name has exactly one owner.
- Well-known names: `"console"` `"fs"` `"net"` `"login"` `"shell"`.

## 2. Console window (carried from 8-opcode era, unchanged)

    +0xF000  u64  write: value printed as hex ("out 0x...")
    +0xF008  u64  write: low byte emitted as raw char

Output only. Input arrives via §4.

## 3. Block window (RAM-disk backend in Phase 5, virtio-blk backend in Phase 8)

Layout at window offset 0 — offsets PINNED (v1.1), naturally aligned,
little-endian (single outstanding request, polled):

    0x00 u32 magic 'BLKW'      0x04 u32 blk_size (=512)
    0x08 u64 num_blocks        0x10 u32 next_req_id (guest increments)
    0x14 u32 pad
    -- request mailbox (guest writes) --
    0x18 u64 op (1=read, 2=write)
    0x20 u64 lba               0x28 u32 count       0x2c u32 pad
    0x30 u64 off  (window offset of data area, = count*blk_size aligned up)
    -- completion slot (kernel writes) --
    0x38 u32 done_req_id       0x3c i32 status (0 ok, <0 err)

Guest scratch for data transfers: window offset **0x1000**; ≤8 sectors per
request; **minimum window size 0x2000**.

Guest: fill request, bump `next_req_id`, poll until `done_req_id` matches,
then touch `off..off+count*blk_size`. Backend swap (RAM↔virtio-blk) is
invisible to guests — same window, same semantics. Managed-runtime guests
(Go) do NOT use this window: they call `kern_blk_read/write` imports
(see changelog); the same backends serve both transports.

## 4. Input events (Phase 7)

    kern_input_recv(ptr, cap) -> i32   // >0 len | 0 none

Record stream into `ptr`, each record (v1.3 layout):

    u8 kind (1=key_down, 2=key_up)   u8 mods (bit0 shift bit1 ctrl bit2 alt)
    u16 scan  (raw i8042 scancode set 1, untranslated)
    u16 codepoint (kernel's US-layout mapping; consumers may re-map via scan)

Delivered ONLY to the focused session. Focus is a kernel-owned attribute;
`kern_focus_set(port_handle)` moves it (login/shell use this after auth).

## 5. Timer window (Phase 8)

    u64 nanos_since_boot        // read-only, monotonic
    u64 deadline_abs            // guest writes; kernel uses for wakeups
    u32 arm (0/1)               // write 1 after setting deadline

Preemption itself is kernel-internal (scheduler quantum checked in the
timer ISR); guests need no cooperation for it.

## 6. Net packet windows (Phase 9)

Two identically-shaped windows, RX and TX, each:

    u32 head  (consumer index)   u32 tail (producer index)
    N slots of: u32 len; u8 data[1526]     (N=256, slot stride 1536)

Producer writes slot at `tail % N`, increments `tail`; consumer mirrors
with `head`. TX drained by kernel shim into virtio-net; RX filled by shim.
L2/L3/L4 parsing lives entirely in `services/net`.

## Gate assertion conventions (all phases)

`make test` targets assert literal substrings on stripped ANSI serial output;
hex values may be zero-padded (`out 0x0000000000000028`). Each phase adds
`make test-pN`. Budget ≥150 s wall clock per run when the kernel halts.

## 7. Kernel-owned service ports (binding from Phase 4)

Three well-known names are served BY THE KERNEL itself (native endpoints;
no extra imports beyond §1 needed — admin tools are ordinary clients):

    "registry"  session & capability control
    "devman"    device enumeration
    "power"     reboot / poweroff

Request framing: single §1 datagram `{u16 op, u16 seq, payload}`; replies
carry the same `seq`. Ops:

    registry: 1=LIST    -> records {u32 sid, u32 uid, u8 state,
                                    char name[16]} per session
              2=CAPS    {u32 sid} -> {u32 n; rec{n u32 cap_id, u64 rights}}
              3=KILL    {u32 sid} -> status      (needs CAP_KILL right)
              4=SPAWN   {char name[16], char path[64], u32 capmask,
                         u16 argc; args bytes} -> {u32 sid}
                                                   (needs CAP_SPAWN right)
              5=LOGIN   {char name[16], u32 uid, u32 capmask} -> status
                          (login-name owner only; see changelog)
              6=SETCONF {char key[16], u64 value} -> status
                          (needs CAP_CONF right; intended: init.wasm)
    devman:    1=ENUM   -> {u32 n; rec{u32 class(1=block,2=net,3=input,
                                    4=timer,5=console), u32 inst,
                                    u64 win_off}}   (needs CAP_DEVMAN)
    power:     1=REBOOT 2=OFF                 (needs CAP_POWER right)

Capability bits (u64 mask, enforced by kernel registry):
bit0 KILL, bit1 DEVMAN, bit2 POWER, bit3 FOCUS, bit4 FS_ADMIN, bit5 NET_ADMIN,
bit6 SPAWN, bit7 CONF. Login maps users→masks; `admin` gets all bits, regular
users get none of 0-2 and scoped others. Unknown op / insufficient bit =>
status -1, audited (see §10). LIST/CAPS gain self-or-admin gating at Phase
10 (v2 roadmap); unguarded today by design of the stub phase.

SPAWN semantics: kernel instantiates the named module from `/boot/modules/`
as a fresh session with exactly the requested capmask (never more than the
caller's own rights allow — privilege escalation rejected). This is how
shells launch programs; there is no fork/exec anywhere in the system.

## 8. Device driver model (two-layer rule)

Layer 1 — native shim (substrate, mechanism): owns real hardware, exposes
exactly ONE class window (§2–§6) per instance. Budget ≤300 LOC/shim.
Lives in `arch/x86_64/dev/` (machine probing) + `core/dev/` (window
plumbing, registry, portable).
Layer 2 — wasm session (policy, optional): consumes class windows/port
names; never touches hardware. fs.wasm, net.wasm are layer-2 consumers.

Adding new hardware = exactly three steps:
  1. define its class window layout here (version bump),
  2. write the shim implementing that window,
  3. register instance in devman table (+ capability grant template).
No kernel-wide changes permitted.

Multiple BACKENDS may exist per class (e.g. block: RAM-disk | virtio-blk |
AHCI; net: virtio-net | E1000). Backends are invisible to guests — devman
ENUM exposes only class/instance/window, never the transport. This is what
makes the same disk image portable across QEMU/VirtualBox/VMware (AGENTS.md
Phase 12).

IRQ policy: v1 fully polled (windows only). v2 (post-Phase-9, ABI bump)
may add `arm` flags + kernel-routed wakeups; never direct guest IRQs.

## 9. Reserved device classes

Class IDs reserved so early code doesn't squat them:

    6 = WLAN      7 = BLUETOOTH      8 = USB-HC      9 = FRAMEBUFFER (DEFINED v1.2)

### 9.FB — FRAMEBUFFER window layout (v1.2, binding)

    0x00 u32 magic 'FBW'
    0x04 u32 width            0x08 u32 height      0x0c u32 bpp (=32 XRGB)
    0x10 u64 fb_off           // pixel data; stride = width*4
    0x18 u32 caps             // bit0 double-buffer, bit1 damage-rects
    -- control mailbox (§3 request/completion shape, polled) --
    0x20 u32 op   1=SET_MODE {u32 w,u32 h,u32 bpp @0x30}
                  2=FLIP (present back buffer; no-op if !caps.bit0)
                  3=UPDATE_RECT {u32 x,y,w,h @0x30} (requires caps.bit1)
    0x24 u32 next_req_id      0x28 u32 done_req_id      0x2c i32 status

v1 rule: single-buffer default (FLIP is a no-op unless double-buffer
negotiated); consumers must tolerate width==0 ("no display attached").
Backends MUST expose identical semantics regardless of transport.

Window layouts still undefined on purpose:

- BLUETOOTH: HCI transport window (H4 framing over a UART instance, or
  USB interrupt/bulk pipes once class 8 exists). L2CAP/ATT/GATT live in a
  wasm session — Go is fine at that altitude.
- WLAN: offload-module style only (coprocessor/dongle speaks framed
  Ethernet-ish data + small control channel). Full 802.11 MAC/firmware
  loading per-vendor in the SHIM is explicitly out of model scope — it
  breaks the ≤300 LOC shim budget; such vendors need their own budgeted
  exception documented here before any code.
- USB-HC: xHCI event/command rings behind a simplified transfer-request
  mailbox (same shape philosophy as §3). Prerequisite for mainstream
  dongles of every kind.

## 10. Audit trail (binding from Phase 4)

Every rejected §7 op (unknown op, insufficient capability bit) produces an
audit record the kernel relays to "console":

    [audit] sid=<n> uid=<n> op=<name> reason=<cap|op> target=<port>

v1 destination is serial only; a file sink under /var/log arrives with
post-v1 storage work. Admin tooling for user management (`useradd`,
`passwd`) edits `/etc/users` through fs.wasm using CAP_FS_ADMIN and then
signals "login" to reload — no dedicated user server needed.

## 11. v2 roadmap (scheduled, NOT in force until ratified)

- Reply capabilities: `kern_port_reply(h, ptr, len)` — kernel mints a
  one-shot, consumed-on-use reply right for the sender of the message
  last received on `h`; supersedes the rname inbox convention (removes
  name-squatting surface entirely). Precedent: seL4 reply caps, Mach
  reply ports.
- LIST/CAPS registry ops gain self-or-admin gating (Phase 10).
- IRQ arm flags + kernel-routed wakeups on §3–§6 windows (post-Phase-9).
- Class layouts for §9 reserved ids (USB-HC, WLAN, BLUETOOTH,
  FRAMEBUFFER) as hardware phases demand.

Any of these entering force requires a version bump here plus a note in
MEMORY.md, ratified by the project owner.
