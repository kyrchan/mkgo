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
