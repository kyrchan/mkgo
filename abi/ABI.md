# abi/ABI.md — guest-facing interface contracts (v2.2)

## v2.2 changelog (2026-09-04 — Phase 19 supervision & config, wire-superset of v2.1)

- §7 registry gains op 11 = KNOBS_GET `{u8 idx}` → `{u32 status=0,
  u64 value, char key[16]}` and op 12 = KNOBS_SET `{u8 idx, u64 value}`
  → `{u32 status}` (CAP_CONF required; audited on use and on denial).
  Fixed 8-entry knob store, kernel-side: 0=quantum_us, 1=log_level,
  2=audit_mask, 3..7 reserved. Setting knob 0 reprograms the scheduler
  quantum live (same clamp as SETCONF: 100..200000us).
- §7 op 6 = SETCONF routes known keys to live subsystems (CAP_CONF
  required, unchanged): quantum_us/quantum/quantum_ms → scheduler
  quantum (ms forms ×1000), preempt → preemption switch, log_level /
  audit_mask → knob store. Unknown keys are accepted + logged (forward
  compat). The kernel still never parses /etc/kernel.conf — init owns
  the file and pushes entries through this op.
- §7 op 1 = LIST records grow one trailing byte: `{u32 sid, u32 uid,
  u8 state, char name[16], u8 source}` (26 B now, was 25). source:
  0=login-issued, 1=chcaps-granted, 2=init-issued. Old parsers that
  read the v2.1 prefix are unaffected (trailing byte ignored).
- §7.4 (new): initctl supervision protocol — NOT a kernel op. Shells
  send canonical-framed datagrams to the well-known "init" port with
  op = subop (1=restart, 2=reload-conf, 3=apply-knobs, 4=respawn-policy)
  and service-name payloads; init replies `{u32 status, detail bytes}`
  (0=ok, 1=not_found, 2=bad_name, 3=already, 4=unavailable).
- Module `abi_ver` stays `0x02`: v2.2 is a pure registry-op superset —
  no WASI/import/window changes, no module rebuild required. Only
  shell.wasm (new built-ins) and init.wasm (initctl server) were rebuilt.

## v2.1 changelog (2026-09-03 — Phase 15 observability, wire-superset of v2.0)

- §7 registry gains op 8 = SYSSTAT → `{u32 status=0, u64 mem_total,
  u64 mem_used, u32 quantum_us, u8 preempt_on, u32 ncpus}`. Read-only
  bump-allocator accounting + scheduler config for top/memstat. No
  capability required (v1; hardening may gate later — see MEMORY.md).
- §7 registry gains op 9 = LOGDUMP `{u64 off}` → `{u32 status=0,
  u64 total, u64 begin, bytes...}` (≤4000 B per reply; poll with
  increasing off). Read-only v1 syslog path: everything emitted via
  console_putc (kernel boot trail, [audit] denials, panics, guest
  fd_write bytes). Backs dmesg/audit shell built-ins. No capability
  required (v1).
- Module `abi_ver` stays `0x02`: v2.1 is a pure registry-op superset —
  no WASI/import/window changes, no module rebuild required. Only
  shell.wasm was rebuilt (it calls the new ops).

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

**Capability gate (F31, binding):** `kern_blk_read/write` are whole-disk
raw access and therefore require the caller's session to hold
`CAP_FS_ADMIN` (§7 bit 4). The kernel audits and rejects (`-1`) any other
caller; fs.wasm is spawned holding the bit via its init.conf capmask.
This is the substrate-side complement to fs.wasm's uid rooting: neither
route to the disk is capability-free.

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
                                     char name[16], u8 source} per session
                                     (source: 0=login, 1=chcaps, 2=init;
                                     v2.2 trailing byte, see §7.4)
              2=CAPS    {u32 sid} -> {u32 n; rec{n u32 cap_id, u64 rights}}
              3=KILL    {u32 sid} -> status      (needs CAP_KILL right)
              4=SPAWN   {char name[16], char path[64], u32 capmask,
                         u16 argc; args bytes} -> {u32 sid}
                                                   (needs CAP_SPAWN right)
              5=LOGIN   {char name[16], u32 uid, u32 capmask} -> status
                          (login-name owner only; see changelog)
               6=SETCONF {char key[16], u64 value} -> status
                           (needs CAP_CONF right; intended: init.wasm;
                           known keys route live — quantum_us/quantum/
                           quantum_ms to the scheduler quantum, preempt
                           to the preemption switch, log_level/audit_mask
                           to the knob store; unknown keys logged+accepted)
                11=KNOBS_GET {u8 idx} -> {u32 status=0, u64 value,
                             char key[16]}          (v2.2, no cap required)
                12=KNOBS_SET {u8 idx, u64 value} -> status
                             (v2.2, needs CAP_CONF; idx 0 reprograms the
                             scheduler quantum live; audited)
    devman:    1=ENUM   -> {u32 n; rec{u32 class(1=block,2=net,3=input,
                                    4=timer,5=console), u32 inst,
                                    u64 win_off}}   (needs CAP_DEVMAN)
    power:     1=REBOOT 2=OFF                 (needs CAP_POWER right)

Capability bits (u64 mask, enforced by kernel registry):
bit0 KILL, bit1 DEVMAN, bit2 POWER, bit3 FOCUS, bit4 FS_ADMIN, bit5 NET_ADMIN,
bit6 SPAWN, bit7 CONF, bit8 PCI, bit9 FB, bit10 DOORBELL, bit11 VMWARE,
bit12 PORTBIND. Login maps users→masks; `admin` gets all bits, regular
users get none of 0-2 and scoped others. Unknown op / insufficient bit =>
status -1, audited (see §10). LIST/CAPS gain self-or-admin gating at Phase
10 (v2 roadmap); unguarded today by design of the stub phase.

### §7.1 Well-known port names (CAP_PORTBIND required)

The following names require CAP_PORTBIND to bind:
  registry, devman, power (kernel endpoints)
  console, login, fs, net, init, shell (core services)
All other names are bindable by any session (dynamic namespace).
init.wasm holds CAP_PORTBIND; driver sessions spawned by init
receive it; user shells do not.

### §7.2 LOGIN capmask minting

The LOGIN op (§7 op 5) sets a session's capmask to the mask
provided in the payload. The kernel enforces: the mask must be
a subset of the caller's capmask, OR the caller must be the
"init" session (which is trusted to mint any mask). This prevents
a compromised login.wasm from granting capabilities it does not
itself hold.

### §7.3 Audit logging (KERN_AUDIT_LEVEL)

KERN_AUDIT_LEVEL=0: log denials only.
KERN_AUDIT_LEVEL=1 (default): log denials + successful use of
  high-value caps (KILL, DEVMAN, POWER, SPAWN, PCI, FB, PORTBIND).
Audit records: [audit] sid=X uid=Y op=<tag> reason=<cap|use>
  target=wasi
KNOBS_SET (op 12) and CHCAPS (op 10) audit every use via their own records
(use + denials), independent of KERN_AUDIT_LEVEL.

SPAWN semantics: kernel instantiates the named module from `/boot/modules/`
as a fresh session with exactly the requested capmask (never more than the
caller's own rights allow — privilege escalation rejected). This is how
shells launch programs; there is no fork/exec anywhere in the system.

### §7.4 initctl supervision protocol (v2.2, Phase 19)

initctl is NOT a kernel op — the kernel only relays these datagrams like
any other port traffic. Shells send canonical-framed (§1) requests to the
well-known "init" port with op = subop and a service-name payload; init
replies on the requester's rname inbox with `{u32 status, detail bytes}`:

    1=restart       {name bytes} -> status + "sid=N" (kill if alive, respawn
                                                     at once, backoff reset)
    2=reload-conf   {} -> status + "N services" (init re-reads
                        /etc/init.conf itself; new names spawn, dropped
                        names stop being supervised, dead ones respawn)
    3=apply-knobs   {} -> status (init re-applies its kernel.conf text
                        through SETCONF)
    4=respawn       {name bytes, 0x00, '0'|'1'} -> status (set respawn flag)

Status: 0=ok, 1=not_found, 2=bad_name, 3=already, 4=unavailable (e.g. no
conf source). Any session may message init in v1 (no cap check at init;
hardening may gate later — see MEMORY.md). Shell built-ins: `initctl
restart <svc> | reload-conf | apply-knobs | respawn <svc> yes|no`.

Knob indexes (ops 11/12): 0=quantum_us, 1=log_level, 2=audit_mask,
3..7 reserved. Shell alias: quantum_ms/quantum = knob 0 in milliseconds.

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

## v2.0 changelog (RATIFIED by project owner, 2026-08-27)

- ABI bump to v2 (`abi_ver` custom section payload starts with byte `0x02`).
  v1 modules are NOT supported on v2 kernels — pre-release, clean break for
  a better microkernel architecture. v2 is a superset of v1 semantics where
  they overlap (ports, windows, registry ops 1-6 unchanged).
- §12 PCI/VFIO: new imports for device passthrough (config access, BAR
  mapping, busmaster enable, IRQ binding). Foundation for all future PCIe
  drivers — zero new kernel code per device after this lands.
- §13 Framebuffer control: modesetting + cursor imports for VFIO-mapped
  LFB. Guest writes pixels directly (zero-copy), kernel controls display
  timing only.
- §14 Doorbell: `kern_doorbell_wait` replaces yield-polling for IRQ-bearing
  devices. Kernel routes MSI/line interrupts to session doorbells.
- §15 Block service protocol: port-based block I/O for userspace block
  drivers (ahci.wasm) to back the §3 window without kernel AHCI code.
- §8 driver model: VFIO passthrough layer added alongside existing
  per-device windows. Two paths: virtio windows (paravirtualized) and
  VFIO (physical passthrough). Both expose identical class windows.
- §7 registry gains op 7=ASSIGN_PCI for dynamic device assignment.
- §9 class 10 PCI reserved for VFIO-passthrough devices.
- Capability bits 8=PCI, 9=FB added.
- Kernel-routed preview1 (fsroute/fstransport) RETAINED as v2 — provides
  stock Go `os.Open` cross-platform compatibility. Not part of the frozen
  WASI profile; a kernel-provided convenience layer on top.

## 12. PCI/VFIO (Phase 11, binding)

Device passthrough foundation. All future PCIe drivers (GPU, NIC, storage,
USB) reuse this with zero new kernel code.

    kern_pci_read32(u32 bus, u32 dev, u32 fn, u32 offset) -> i32
        // Read PCI config dword. Returns value, or -1 on err.
        // Requires CAP_PCI.

    kern_pci_write32(u32 bus, u32 dev, u32 fn, u32 offset, u32 val) -> i32
        // Write PCI config dword. 0 ok, -1 err. Requires CAP_PCI.

    kern_pci_map_bar(u32 bus, u32 dev, u32 fn, u32 bar) -> i64
        // Map PCI BAR into guest linear memory. Returns window offset,
        // or -1 on err. Guest accesses BAR MMIO through this window.
        // Kernel maps with appropriate caching (WC for framebuffer,
        // uncached for device registers). Requires CAP_PCI.

    kern_pci_unmap_bar(u32 bus, u32 dev, u32 fn, u32 bar) -> i32
        // Unmap a previously-mapped BAR. 0 ok, -1 err. Requires CAP_PCI.

    kern_pci_enable_busmaster(u32 bus, u32 dev, u32 fn) -> i32
        // Enable PCI bus mastering (DMA). 0 ok, -1 err. Requires CAP_PCI.

    kern_pci_bind_irq(u32 bus, u32 dev, u32 fn, u32 type) -> i32
        // Bind device IRQ to a session doorbell. type: 0=INTX, 1=MSI,
        // 2=MSI-X. Returns doorbell handle (pass to kern_doorbell_wait),
        // or -1 on err. Kernel programs MSI-X table internally — guest
        // never touches raw APIC addresses. Requires CAP_PCI.

    kern_pci_flr(u32 bus, u32 dev, u32 fn) -> i32
        // Issue Function Level Reset. 0 ok, -1 err/unsupported.
        // Used by stub session for GPU crash recovery. Requires CAP_PCI.

Semantics:
- IOMMU restricts all guest DMA to assigned pages — compromised guest
  cannot DMA outside its scope.
- BAR window offsets follow the same convention as §2-§6: guest never
  computes absolute addresses, only window offsets.
- One doorbell handle per IRQ source. Multiple devices → multiple handles.
- `kern_pci_map_bar` may fail if the device is not assigned to the caller's
  session (kernel registry check).

## 13. Framebuffer control (Phase 11, binding)

Modesetting and cursor for VFIO-mapped Linear Framebuffer. Guest writes
pixels directly to the mapped LFB (zero-copy). Kernel controls display
timing only — guests cannot program CRTC registers directly.

    kern_fb_set_mode(u32 width, u32 height, u32 bpp) -> i32
        // Set display mode. Programs CRTC/scaler hardware. 0 ok, -1 err.
        // bpp must be 32 (XRGB). Kernel validates mode against EDID.
        // Requires CAP_FB.

    kern_fb_set_cursor(u32 x, u32 y) -> i32
        // Set hardware cursor position. -1 disables cursor.
        // Requires CAP_FB.

The LFB is mapped via `kern_pci_map_bar` (the framebuffer BAR). Guest
writes pixel data directly at the returned window offset. stride =
width * 4. No kernel copy — writes reach the hardware directly.

VSYNC notification arrives via the doorbell handle bound to the display
controller's interrupt. Guest calls `kern_doorbell_wait` to synchronize
page flips.

The §9.FB window mailbox (SET_MODE/FLIP/UPDATE_RECT) is RETAINED for v1
backward compat with Bochs/SVGA shims. VFIO framebuffer uses the new
imports + direct LFB writes. Both paths converge on the same hardware.

## 14. Doorbell (Phase 11, binding)

Replaces yield-polling for IRQ-bearing devices. Each session has a doorbell
bitmap (one bit per IRQ source). When a device interrupt fires, the kernel
sets the bit and wakes the session.

    kern_doorbell_wait(u32 handle, u32 timeout_ms) -> i32
        // Block until doorbell `handle` fires, or timeout.
        // Returns: 0 = fired, 1 = timeout, -1 = err.
        // timeout_ms = 0 → poll once (non-blocking).
        // No capability requirement — any session with a bound handle
        // can wait on it.

Semantics:
- `handle` is obtained from `kern_pci_bind_irq`.
- Multiple handles can be waited on via sequential calls (no multi-wait
  in v1 — guest loops over its handles).
- For v1 compat, `sched_yield` still works; guests that don't use
  `kern_doorbell_wait` fall back to polling.
- Doorbell is level-triggered: if the interrupt fired while the guest
  was not waiting, the next `kern_doorbell_wait` returns immediately.

## 15. Block service protocol (Phase 11, binding)

Port-based block I/O for userspace block drivers. Lets ahci.wasm (or any
block driver) back the §3 window without kernel AHCI code.

A block driver session binds a well-known name (e.g. "blk1") and serves
this protocol over §1 ports:

    Request  (client → driver): {u16 op, u16 seq, u32 uid, char rname[16],
                                  u64 lba, u32 count, u32 pad}
    Reply    (driver → client): {u16 op, u11 seq, u32 uid, char rname[16],
                                  i32 status, u32 pad}
    Data     (client ↔ driver): transferred via §1 port payload (≤4096 B)
                                  or via a shared window for larger ops.

Ops: 1=READ, 2=WRITE, 3=FLUSH, 4=GEOMETRY (returns {u64 sectors, u32 blk_size}).

The kernel's "userspace block backend" router (part of VFIO foundation)
bridges §3 window operations to this protocol, so fs.wasm is unchanged —
it still sees §3, but the backend is now a userspace driver.

## 8. Device driver model (two-layer rule, UPDATED for v2)

Layer 1a — native shim (substrate, mechanism): owns real hardware, exposes
exactly ONE class window (§2–§6) per instance. Budget ≤300 LOC/shim.
Lives in `arch/x86_64/dev/` (machine probing) + `core/dev/` (window
plumbing, registry, portable). Used for paravirtualized devices (virtio-net,
virtio-blk) and legacy devices (PS/2, PIT, PIC, UART).

Layer 1b — VFIO passthrough (substrate, mechanism): maps PCI device BARs
into guest memory with IOMMU protection. No per-device code — generic
infrastructure reused by ALL PCIe devices. Budget ~2,000 LOC one-time
investment in `core/vfio.cc` + `core/pci.cc`. After this lands, new PCIe
devices need zero kernel code.

Layer 2 — wasm session (policy): consumes class windows/port names; never
touches hardware. fs.wasm, net.wasm, graphics.wasm, e1000.wasm, ahci.wasm
are layer-2 consumers.

Two paths per class:
- **Virtio path** (paravirtualized): kernel shim owns hardware, guest sees
  §3/§6 windows. Used on QEMU/VMware/VBox without PCIe passthrough.
- **VFIO path** (physical): guest drives hardware directly via PCI BAR
  windows + doorbell. Used on real hardware or hypervisors with IOMMU.

Both paths expose identical class windows — guests cannot tell the
difference. devman ENUM reports class/instance/window only.

Adding new hardware = exactly three steps:
  1. define its class window layout here (version bump),
  2. EITHER write a shim (≤300 LOC) OR assign via VFIO (zero LOC),
  3. register instance in devman table (+ capability grant template).
No kernel-wide changes permitted (except VFIO foundation, one-time).

IRQ policy: v1 fully polled (windows only). v2 adds doorbell (§14) for
IRQ-bearing devices. Never direct guest IRQs.

## 7. Kernel-owned service ports (binding from Phase 4, UPDATED)

[Previous ops 1-6 unchanged]

               7=ASSIGN_PCI {u8 bus, u8 dev, u8 fn, u32 target_sid} -> status
                           // Dynamically assign a PCI device to a session.
                           // Requires CAP_DEVMAN. Used by init.wasm at boot
                           // and for hot-plug reassignment.
                8=SYSSTAT   {} -> {u32 status=0, u64 mem_total,
                            u64 mem_used, u32 quantum_us, u8 preempt_on,
                            u32 ncpus}             (v2.1, no cap required)
                9=LOGDUMP   {u64 off} -> {u32 status=0, u64 total,
                            u64 begin, bytes...}   (v2.1, no cap required;
                            off is an absolute stream offset; begin is the
                            oldest retained offset — clamp upward; poll
                            with off=total for tails)
               10=CHCAPS  {u32 sid, u32 clear, u32 set} -> status
                            // Grant/revoke cap bits on a live session.
                            // Requires CAP_POWER. Clears (clear mask) then
                            // sets (set mask). Self-modification of
                            // non-admin caps is denied. Audited.

## Capability bits (§7, UPDATED)

  bit0 KILL       bit1 DEVMAN     bit2 POWER      bit3 FOCUS
  bit4 FS_ADMIN   bit5 NET_ADMIN  bit6 SPAWN       bit7 CONF
  bit8 PCI        bit9 FB
  bit10 DOORBELL  bit11 VMWARE    bit12 PORTBIND  (v2.0 additive)

## 9. Reserved device classes (UPDATED)

Class IDs reserved so early code doesn't squat them:

    6 = WLAN      7 = BLUETOOTH      8 = USB-HC      9 = FRAMEBUFFER (DEFINED v1.2)
   10 = PCI       [VFIO passthrough device]

### 9.FB — FRAMEBUFFER window layout (v1.2, binding, RETAINED)

[Unchanged — Bochs/SVGA shim path]

## Capability bits (§7, UPDATED)

```
bit0 KILL, bit1 DEVMAN, bit2 POWER, bit3 FOCUS, bit4 FS_ADMIN, bit5 NET_ADMIN,
bit6 SPAWN, bit7 CONF, bit8 PCI, bit9 FB
```

CAP_PCI (bit 8): grants kern_pci_* access (config, BAR map, busmaster, IRQ
bind, FLR). Granted to device driver sessions (graphics.wasm, e1000.wasm,
ahci.wasm, usb.wasm).

CAP_FB (bit 9): grants kern_fb_set_mode / kern_fb_set_cursor. Granted to
graphics.wasm only.
