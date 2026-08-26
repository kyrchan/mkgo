VRING-HANDOFF.md — everything known about the Phase 9 blocker
=============================================================
(Updated 2026-08-26 by the session that found root causes #1 and #2.
Previous content preserved below the CURRENT STATE section.)

CURRENT STATE (2026-08-26, commit <see git log "vring: handoff update">)
-----------------------------------------------------------------------
TWO REAL BUGS WERE FOUND AND FIXED; ONE REMAINS.

FIXED #1 -- descriptor NEXT-field encoding (was corrupting every chain):
  Legacy vring desc word = addr u64 | len u32 | flags u16 | NEXT u16.
  The old code packed NEXT into the FLAGS field: `16 | 1<<32` meant
  flags=NEXT, next=0 => desc0 chained TO ITSELF. Device silently drops
  such chains (no used entry, no error). Fixed in core/virtio_blk.cc
  (`<< 48` for next) and core/virtio_net.cc desc_put() now takes
  (flags u16, next u16) separately.

FIXED #2 -- identity map holes + >4G BARs:
  * paging_identity_init only filled PML4[0]; VAs >=512GiB unmapped.
    Now loops one PDPT per PML4 slot (arch/x86_64/paging.cc).
  * top raised to 64GiB so q35's 64-bit MMIO BAR window (virtio modern
    regs at e.g. 0xC_0000_0000) is covered.
  * Page tables were allocated from the GENERAL mm pool and got
    overwritten by later engine/session allocations (verified: PTEs
    written, then zeroed). Tables now come from a private pt_arena.
  * CR4.PGE left set by OVMF: stale GLOBAL TLB entries from firmware-era
    translations survive CR3 reload. PGE is now cleared after wr_cr3.

REMAINING BLOCKER (exact symptom, verified twice independently):
  With legacy transport + correct chains, the device consumes the FIRST
  avail entry or two and then stops forever; used ring stays empty; ISR
  may assert once then clears. Session-side requests (fs via kern_blk_*)
  never complete; kernel-context probe requests behave identically in
  clean builds. Monitor xp confirms our avail idx lands at +4098 and the
  spec used-ring page (+8192) stays ZERO while the device trace shows
  handle_read/req_complete FIRING (traces from -d runs) => completions
  are generated but NOT WRITTEN to our page, i.e. QEMU's queue address
  differs from ours despite PFN readback matching.

STRONGEST REMAINING HYPOTHESIS
------------------------------
QEMU 10 legacy QUEUE_PFN handling: readback of the register returns what
the driver wrote, but the live vq address may not be updated (QEMU10
reworked legacy queue activation; QUEUE_NUM writes already logged as
"unexpected address 0xc"). I.e., REGISTER MIRROR != LIVE STATE.
=> Do NOT trust readbacks. Two viable exits:
  A. MODERN TRANSPORT (80% done on this branch):
     - core/virtio_modern.cc/h: cap-list parser (works: finds common/
       notify/isr/device caps correctly), feature handshake with
       VERSION_1, queue_setup writing desc/avail/used addresses
       individually + queue_enable, notify via notify_off*multiplier.
     - REAL common-config offsets are now coded (feat_sel@0 feat@4
       drv_sel@8 drv@C status@14 qsel@16 qsize@18 qenable@1c
       qnotify@1e desc@20 avail@28 used@30).
     - virtio_blk.cc modern rewrite exists as
       /tmp/opencode/virtio_blk_modern.cc.bak (copy back over
       core/virtio_blk.cc); net equivalent pending (~same shape).
     - Identity map now covers the BAR window, so MMIO access works --
       this was the second crash loop earlier today.
     - RESUME: copy .bak in, port net, run make test-p9.
  B. If modern still misbehaves under TCG, try pinning QEMU to a 8.x/9.x
     binary where legacy behavior matches our expectations.

ENVIRONMENT GOTCHAS THAT COST HOURS (do not relearn)
----------------------------------------------------
* ALWAYS rebuild the DISK after BOOTX64.EFI changes: several false leads
  came from booting stale images. Verify with mdir size match.
* make-driven rebuilds drop -D flags used in manual g++ probes; do full
  touch+make cycles, verify strings in build/BOOTX64.EFI.
* build/VARS.fd gets corrupted by kill -9 during OVMF writes -> boots
  fall into the UEFI shell. Restore from osdev-root template.
* monitor xp reads GUEST PHYSICAL; serial [probe] prints use VA==PA
  identity -- they agree only when the map is correct.
* pml4 index math: va>>39 for va=48GiB is 0 (not 6!). 48G needs
  PML4[0]->PDPT[48].

CURRENT STATE #2 (2026-08-26 later -- after modern transport brought up)
-----------------------------------------------------------------------
LEGACY TRANSPORT REPLACED BY MODERN (virtio 1.x) via
core/virtio_modern.cc/h + rewritten virtio_blk.cc / virtio_net.cc.
Working NOW, verified:
  * probe via PCI cap list (common/notify/isr/device BARs),
  * VERSION_1+FEATURES_OK handshake,
  * queue_setup with explicit desc/avail/used addresses + enable,
  * TX completions, RX deliveries into §6 windows,
  * ARP request/reply round trip on the wire (filter-dump proof),
  * UDP datagrams guest->host AND host->guest on the wire,
  * **[p9] udp ok** achieved end-to-end (driver got its echoed 12 bytes).

REMAINING FOR p9 GATE: TCP establishment timing. Wire shows SYN out and
SYN-ACK back repeatedly, but net.wasm's conn stays SYN-SENT through the
driver's 6 quick Send retries => "socket state". Under TCG each yield is
~1 quantum of real work; budgets tuned for loopback fire too fast.
FIX DIRECTION: lengthen tcp.Dial/SYN-ACK wait (services/net/tcp.go Dial
returns immediately; make Connect retry loop in guests/p9/main.go longer
or give netport NetOpConn an internal wait), then re-run
`python3 scripts/run_p9_once.py 240` (in-process helpers; prints GATE).

DEBUG PRINTS still present on purpose: [net-dbg] in netport.go/tcp.go/
udp.go, [netrx] raw dump in core/virtio_net.cc (helpful until gate is
green; strip when done). scripts/run_p9_once.py = deterministic local
gate runner (in-process echo+http helpers, no shell backgrounding!).


CURRENT STATE #3 (2026-08-26 final this cycle -- MODERN TRANSPORT LIVE)
-----------------------------------------------------------------------
MILESTONE ACHIEVED: **[p9] udp ok** end-to-end (driver receives its
echoed datagram through shim->device->slirp->host echo->back). Wire
proof: filter-dump shows ARP req/reply AND UDP req/reply both ways.
Legacy transport is GONE (replaced by core/virtio_modern.cc/h).

KEY FIXES THAT UNLOCKED IT (all committed):
  * windows moved to 0x4000000 (64 MiB): far above Go heap; 512MiB
    placement failed (wasm3 realloc of 538MB -> 'memory allocation
    failed'), 64MiB works.
  * ParseIP4: accept non-DF unfragmented datagrams (slirp replies carry
    flags=0x0000; old code demanded DF exactly => every inbound IPv4
    from slirp was silently dropped).
  * UDP checksum now includes pseudo-header (slirp validates).
  * Bind(0) allocates an EPHEMERAL port (replies need a real port).
  * ServeNet idle path: runtime.Gosched()+k.Yield() -- k.Yield alone
    starves Go goroutines (wire-pump) forever on wasip1.
  * virtio_net_poll TX is single-outstanding NON-BLOCKING state machine
    (kick once, reap completion on later polls; never spin/yield in
    scheduler context).

REMAINING FOR p9 GATE: TCP establishment. Wire shows SYN out +
SYN-ACK back repeatedly, but conn stays SYN-SENT through driver's 6
quick retries (each ~instant local state check; gaps of 200 yieldGo).
NEXT STEPS: (1) lengthen Connect/Send retry gaps (yieldGo x 2000+) or
add internal SYN-ACK wait in NetOpConn; (2) confirm via [net-dbg] tcp
seg prints whether SYN-ACK reaches tcp.handle (prints already in
services/net/tcp.go); (3) then HTTP GET + gate. Debug prints
([net-dbg], [netrx] raw dump) intentionally left in until gate green.
TCG NOTE: everything works with LONGER windows; run_p9_once.py takes
window seconds arg (use 240+).


CURRENT STATE #4 (2026-08-26 late -- TCP data phase, commit ac49aeb)
-----------------------------------------------------------------------
TRANSPORT LAYER: FULLY WORKING. Modern virtio (virtio_modern.cc) probes,
negotiates, sets up queues correctly. Frames flow BOTH directions on the
wire (filter-dump proof). ARP, UDP echo, TCP SYN/SYN-ACK/ACK all work.

ACHIEVED: [p9] udp ok -- driver receives echoed datagram through the
full stack (driver→net.wasm→shim→device→slirp→host→back).

REMAINING: HTTP response delivery stalls after ESTABLISHED.
  - Host serves GET with 200 ✓ (python log confirms)
  - Response frame arrives at device (should generate rx completion)
  - But net.wasm pump only processes 2 frames total per run:
    frame#1 = SYN-ACK → ESTABLISHED
    frame#2 = ??? (possibly ACK or first data segment)
    The actual HTTP response body frame never reaches tcp.handle.
  
  ROOT CAUSE THEORIES (ranked):
  1. The response arrives while ALL rx buffers are consumed-by-device
     and our repost logic doesn't return them fast enough under TCG.
     FIX: increase VN_RX_BUFS from 8 to 32+.
  2. The tx_pending state machine skips the RX section when it
     shouldn't. VERIFY: print from BOTH sections every poll call.
  3. QEMU slirp drops the response because our TCP ACK checksum is
     wrong. TEST: capture with filter-dump and validate ACK csum.

DEBUG AIDS STILL IN CODE:
  [net-dbg] prints in netport.go + tcp.go; [rxcomp] in virtio_net.cc;
  [hog] detector in sched.cc. scripts/run_p9_once.py takes window secs.

TCG PERFORMANCE NOTE: each yieldGo() ≈ 1 full round of all sessions.
With ~6 sessions × ~50ms/quantum under TCG, a single yield costs
~300ms wall. Budget loops of 2000 yields = 10 minutes! Reduce budgets
or switch to KVM for faster iteration.


SUCCESS CRITERIA (unchanged)
---------------------------
[p9] udp ok AND http ok via make test-p9 on committed code.
Do not emit ALL PHASES COMPLETE (coordinator decides).

=======================================================================
PRE-EXISTING NOTES (2026-08-24 03:0x era, kept for history)
=======================================================================
SYMPTOM
-------
Legacy virtio-net (transitional device, QEMU 10.x q35, -device
virtio-net-pci,disable-legacy=off): after ~2 requests the device STOPS
consuming avail-ring entries. Device-written used-ring completions land
at WRONG offsets: observed writes at vring_buf+10762 / +10768 / +10772
(elem id/len) instead of spec legacy layout offset 8192 (align(4096 +
6 + 2*256, 4096)). RX therefore never advances; TX times out; net.wasm
UDP send fails "socket state" on second+ attempt. First request DOES
complete once (MAC probe + first TX seen).

[NOTE 2026-08-26: the +10762 hits were later explained as NET rx-buffer
frame CONTENT (rx buffers start at +8192 in that layout) plus stale-junk
reads through the wrong poll offset -- not a device used ring.]

ENVIRONMENT
-----------
QEMU 10.x, -machine q35, -device virtio-net-pci,disable-legacy=off,
-netdev user,id=n1. Kernel identity-mapped (VA==PA), freestanding C++20.
Shim: core/virtio_net.cc. Constants: VN_QNUM 256, DESC_TBL 4096 bytes,
AVAIL at DESC_TBL+6, USED at align(DESC+AVAIL,4096)=8192.

HYPOTHERES STATUS (2026-08-26)
------------------------------
1. ACCIDENTAL MODERN NEGOTIATION -- TESTED: host_features=0x79bf8064
   (bit32 cannot appear in legacy view). Full outl(GUEST_FEATURES,0)
   done. Not the cause.
2. Alignment change -- TESTED via monitor xp: completions DO land at
   spec +8192 whenever the device processes at all.
3. u16 wrap -- ruled out.
NEW #4 (current): QEMU10 legacy queue-address registration semantics
(see STRONGEST REMAINING HYPOTHESIS above).

EVIDENCE LOCATIONS
------------------
- /tmp/opencode/{vh*.log,mv*.log,lg*.log} serial captures (this session)
- monitor dumps: /tmp/opencode/xp*.txt, vrpage.bin
- QEMU traces: tr3.log (notify), tr4.log (handle_read/complete)
- commits: ee56f00..HEAD ("vring:" prefix)
