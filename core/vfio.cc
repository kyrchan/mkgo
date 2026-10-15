#include "vfio.h"
#include "pci.h"
#include "sched.h"
#include "lib.h"
#include "mm.h"
#include "plat.h"
#include "arch_iommu.h"
#include "io.h"
#include "boot.h"
#include "console_font.h"

extern "C" {
}
extern uint64_t wasi_now_ns(void);
extern void sched_yield_current(void);

// VFIO foundation — IOMMU domains, MSI-X programming, BAR mapping, doorbells.
// Each session has up to 8 BAR mappings and 4 doorbells. IOMMU restricts
// guest DMA to assigned pages (security boundary).

static constexpr int MAX_BAR_MAPS = 8;
static constexpr int MAX_DOORBELLS = 4;
static constexpr int MAX_PCI_DEVS = 16;
static constexpr int MAX_IOMMU_PAGES = 8192; // 32 MB of tracked pages per domain

// Virtual VGA device (BDF 0:1:0) — provides a framebuffer BAR for headless
// VFIO testing. Allocates a backing buffer; guests map BAR 0 and render
// into their linear window. Class 0x03 (display controller).
static constexpr uint32_t VVGA_BUS = 0;
static constexpr uint32_t VVGA_DEV = 1;
static constexpr uint32_t VVGA_FN = 0;

struct bar_map {
    bool used;
    uint32_t sid;
    uint32_t bus, dev, fn, bar;
    uint64_t phys, size;
    uint64_t win_off; // guest linear window offset
    bool is_mem;
    bool is_wc; // write-combining (framebuffer)
};

struct doorbell {
    bool used;
    uint32_t sid;
    uint32_t handle;
    uint32_t bus, dev, fn, type;
    bool pending;
    uint64_t last_fire;
    uint32_t msi_vector; // allocated MSI/MSI-X vector
    uint64_t msi_addr;   // APIC address programmed
    uint32_t msi_data;   // APIC data programmed
};

struct pci_assign {
    bool used;
    uint32_t target_sid;
    uint8_t bus, dev, fn;
};

// IOMMU page ownership table — one per session domain.
// Tracks which physical pages a session is allowed to DMA to/from.
struct iommu_domain {
    bool used;
    uint32_t sid;
    struct {
        uint64_t phys;
        uint32_t size;
    } pages[MAX_IOMMU_PAGES];
    int num_pages;
};

static bar_map bar_maps[MAX_BAR_MAPS];
static doorbell doorbells[MAX_DOORBELLS];
static pci_assign assigns[MAX_PCI_DEVS];
static iommu_domain domains[MAX_SESSIONS]; // one per possible session
static uint32_t next_handle = 1;

// Framebuffer state (single display, §13)
static uint32_t fb_w = 0, fb_h = 0, fb_bpp = 32;
static uint32_t fb_stride = 0; /* bytes per scanline in the display fb */
static uint32_t fb_cursor_x = 0, fb_cursor_y = 0;
static bool fb_has_display = false;

// MSI-X vector allocation (simple bitmap for 32 vectors)
static uint32_t msi_vector_bitmap = 0;

// Scanout target: real PCI display framebuffer (set if a display device is
// present), otherwise the kernel-allocated headless buffer.
static uint32_t scanout_phys = 0;
static uint32_t scanout_size = 0;
static bool scanout_enabled = false;
static bool scanout_bochs = false;

void vfio_init(const struct boot_info *bi) {
    for (auto &m : bar_maps) m.used = false;
    for (auto &d : doorbells) d.used = false;
    for (auto &a : assigns) a.used = false;
    for (auto &d : domains) d.used = false;
    next_handle = 1;
    msi_vector_bitmap = 0;
    fb_w = 1024; fb_h = 768; fb_bpp = 32;
    fb_has_display = false;
    // Prefer a real PCI display device (e.g. bochs-display) for scanout
    // because QEMU scans its framebuffer to the display window. The GOP
    // framebuffer is a firmware memory region that no display device
    // scans out. Fall back to GOP only when no PCI display is present.
    scanout_enabled = false;
    scanout_phys = 0;
    scanout_size = 0;
    {
        uint32_t disp_phys = 0, disp_size = 0;
        if (pci_find_display(&disp_phys, &disp_size) == 0 && disp_size >= (fb_w * fb_h * 4)) {
            scanout_phys = disp_phys;
            scanout_size = disp_size;
            scanout_enabled = true;
            scanout_bochs = true;
            fb_has_display = true;
            console_puts("[vfio] scanout: PCI display fb phys=");
            console_hex64(scanout_phys);
            console_puts(" size=");
            console_hex64(scanout_size);
            console_puts("\n");
        } else if (bi && bi->fb_phys != 0 && bi->fb_size >= (fb_w * fb_h * 4)) {
            scanout_phys = (uint32_t)bi->fb_phys;
            scanout_size = (uint32_t)bi->fb_size;
            scanout_enabled = true;
            fb_has_display = true;
            if (bi->fb_width != 0) fb_w = bi->fb_width;
            if (bi->fb_height != 0) fb_h = bi->fb_height;
            if (bi->fb_bpp != 0) fb_bpp = bi->fb_bpp;
            if (bi->fb_stride != 0) fb_stride = bi->fb_stride;
            console_puts("[vfio] scanout: GOP fb phys=");
            console_hex64(scanout_phys);
            console_puts(" size=");
            console_hex64(scanout_size);
            console_puts(" ");
            console_hex64(fb_w);
            console_puts("x");
            console_hex64(fb_h);
            console_puts("\n");
        } else {
            console_puts("[vfio] scanout: headless (no display)\n");
        }
    }
    pci_vfb_init(1024, 768);
}

// --- Guest window allocator ---
static uint64_t next_win_off = 0x5000000; // 80M (after net windows at 64M)
static constexpr uint64_t WIN_ALIGN = 4096;

static uint64_t alloc_window(uint64_t size) {
    uint64_t off = (next_win_off + WIN_ALIGN - 1) & ~(WIN_ALIGN - 1);
    next_win_off = off + ((size + WIN_ALIGN - 1) & ~(WIN_ALIGN - 1));
    if (next_win_off > 0x10000000)
        return (uint64_t)-1;
    return off;
}

static bool has_cap(uint32_t sid, uint64_t cap) {
    return (sched_capmask_of(sid) & cap) != 0;
}

// --- IOMMU domain management ---

static iommu_domain *domain_of(uint32_t sid) {
    if (sid >= MAX_SESSIONS) return nullptr;
    if (!domains[sid].used) {
        domains[sid].used = true;
        domains[sid].sid = sid;
        domains[sid].num_pages = 0;
    }
    return &domains[sid];
}

// Grant a session ownership of a physical page range (IOMMU map).
// In a real implementation, this programs VT-d/AMD-Vi page tables.
// Here we track ownership for the security boundary.
static int iommu_map_pages(uint32_t sid, uint64_t phys, uint32_t size) {
    iommu_domain *d = domain_of(sid);
    if (!d) return -1;
    /* F-AUDIT-10: round start UP and end UP so we never grant a page
     * the caller did not ask for. Previously `start = phys & ~0xFFF`
     * rounded DOWN, over-granting one page below phys on unaligned
     * input (tracking-only today; would be a real DMA over-grant
     * against a real IOMMU). */
    if (size == 0) return 0;
    uint64_t start = (phys + 0xFFF) & ~0xFFFULL;
    uint64_t end = (phys + size + 0xFFF) & ~0xFFFULL;
    if (start >= end) return 0;
    for (uint64_t p = start; p < end; p += 0x1000) {
        if (d->num_pages >= MAX_IOMMU_PAGES) {
            console_puts("[vfio] iommu: domain full\n");
            return -1;
        }
        d->pages[d->num_pages].phys = p;
        d->pages[d->num_pages].size = 0x1000;
        d->num_pages++;
    }
    console_puts("[vfio] iommu: mapped ");
    console_hex64(end - start);
    console_puts(" bytes for sid=");
    console_hex64(sid);
    console_puts("\n");
    return 0;
}

// Revoke all pages owned by a session (IOMMU unmap / domain teardown).
static void iommu_unmap_all(uint32_t sid) {
    if (sid >= MAX_SESSIONS) return;
    domains[sid].used = false;
    domains[sid].num_pages = 0;
}

// Revoke a specific physical range from a session's IOMMU domain.
static void iommu_unmap_range(uint32_t sid, uint64_t phys, uint32_t size) {
    if (sid >= MAX_SESSIONS || !domains[sid].used) return;
    iommu_domain *d = &domains[sid];
    uint64_t start = phys & ~0xFFFULL;
    uint64_t end = (phys + size + 0xFFF) & ~0xFFFULL;
    // Compact: keep pages that fall outside [start, end).
    int j = 0;
    for (int i = 0; i < d->num_pages; i++) {
        if (d->pages[i].phys >= start && d->pages[i].phys < end)
            continue; // revoke this page
        if (j != i)
            d->pages[j] = d->pages[i];
        j++;
    }
    d->num_pages = j;
}

// Check if a session is allowed to DMA to a physical address.
bool vfio_iommu_permits(uint32_t sid, uint64_t phys, uint32_t size) {
    if (sid >= MAX_SESSIONS || !domains[sid].used) return false;
    iommu_domain *d = &domains[sid];
    uint64_t start = phys & ~0xFFFULL;
    uint64_t end = (phys + size + 0xFFF) & ~0xFFFULL;
    for (uint64_t p = start; p < end; p += 0x1000) {
        bool found = false;
        for (int i = 0; i < d->num_pages; i++) {
            if (d->pages[i].phys == p) { found = true; break; }
        }
        if (!found) return false;
    }
    return true;
}

// --- MSI-X programming ---

static uint32_t alloc_msi_vector(void) {
    for (int i = 0; i < 32; i++) {
        if (!(msi_vector_bitmap & (1u << i))) {
            msi_vector_bitmap |= (1u << i);
            return (uint32_t)(0x40 + i); // vector base 0x40 (post-PIC remap)
        }
    }
    return 0; // none available
}

static void free_msi_vector(uint32_t vec) {
    if (vec >= 0x40 && vec < 0x60) {
        msi_vector_bitmap &= ~(1u << (vec - 0x40));
    }
}

// Program MSI-X table in PCI config space.
// Returns 0 on success, -1 on error.
static int program_msix(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t vector) {
    // Find MSI-X capability
    int32_t cap_ptr = pci_read32(bus, dev, fn, 0x34);
    if (cap_ptr == -1) return -1;
    uint8_t ptr = cap_ptr & 0xFF;
    for (int i = 0; i < 16 && ptr >= 0x40; i++) {
        int32_t hdr = pci_read32(bus, dev, fn, ptr);
        if (hdr == -1) break;
        uint8_t cap_id = hdr & 0xFF;
        uint8_t next = (hdr >> 8) & 0xFF;
        if (cap_id == 0x11) { // MSI-X
            /* Message Control is 16-bit at ptr+2; read DWORD at ptr
             * (DWORD-aligned) and extract lower half. */
            int32_t msg_ctrl = pci_read32(bus, dev, fn, ptr);
            if (msg_ctrl == -1) return -1;
            // Table offset/BIR at ptr+4
            int32_t table_off = pci_read32(bus, dev, fn, ptr + 4);
            if (table_off == -1) return -1;
            (void)table_off; // used below
            uint32_t table_offset = table_off & ~0x7u;
            // For simplicity, use BIR 0 (BAR0 must be mapped)
            // Program the first MSI-X table entry
            // Table entry: addr_lo(0), addr_hi(4), data(8), vector_ctrl(12)
            // Get BAR0 physical address
            uint64_t bar0_phys;
            uint64_t bar0_size;
            bool bar0_is_mem;
            if (pci_bar_info(bus, dev, fn, 0, &bar0_phys, &bar0_size, &bar0_is_mem) != 0)
                return -1;
            uint64_t table_phys = bar0_phys + table_offset;
            // Write MSI-X table entry (4 DWORDs = 16 bytes for first entry)
            uint32_t addr_lo = 0xFEE00000u;
            uint32_t addr_hi = 0;
            uint32_t data = vector;
            volatile uint32_t *table = (volatile uint32_t *)(uintptr_t)table_phys;
            table[0] = addr_lo;
            table[1] = addr_hi;
            table[2] = data;
            table[3] = 0; // unmask
            // Enable MSI-X: read-modify-write DWORD at ptr, set bit 15 of lower half
            uint32_t ctrl = (uint32_t)msg_ctrl;
            ctrl |= (1u << 15);
            pci_write32(bus, dev, fn, ptr, ctrl);
            console_puts("[vfio] msi-x programmed vector=");
            console_hex64(vector);
            console_puts("\n");
            return 0;
        }
        ptr = next;
    }
    return -1; // no MSI-X capability
}

// --- BAR mapping ---

int64_t vfio_map_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar) {
    if (!has_cap(sid, SCHED_CAP_PCI)) {
        console_puts("[vfio] map_bar: no CAP_PCI sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] map_bar: BDF not assigned to sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (bar > 5) return -1;
    // Check already mapped
    for (auto &m : bar_maps) if (m.used && m.sid==sid && m.bus==bus && m.dev==dev && m.fn==fn && m.bar==bar)
        return (int64_t)m.win_off;
    /* Assign the BAR. An unassigned BAR decodes nothing, so this is not
     * optional: without it every register read returns whatever the
     * machine has at an address the device never agreed to. Addresses come
     * from a bump allocator in the 32-bit PCI hole, well above guest RAM
     * (512 MiB) and clear of the GOP framebuffer at 0x80000000. */
    /* Assign in the same neighbourhood the firmware uses: on this machine
     * OVMF put a working HBA BAR at 0x82061000, while 0xE0000000 (a
     * plausible-looking PCI hole) decoded nothing and every register read
     * came back as the same byte repeated. Guessing an address outside the
     * hole the machine actually routes is how the xHCI's registers read as
     * 0x83838383. */
    static uint64_t next_bar_base = 0x82100000ull;
    uint64_t phys, size;
    bool is_mem;
    if (pci_bar_info(bus, dev, fn, bar, &phys, &size, &is_mem) != 0) {
        /* No address yet. An unassigned BAR decodes nothing, so a device
         * cannot be used until one is programmed; pci_assign_bar does that
         * and reports the address the device actually took. */
        if (pci_assign_bar(bus, dev, fn, bar, next_bar_base, &phys, &size, &is_mem) != 0) {
            console_puts("[vfio] map_bar: assign failed\n");
            return -1;
        }
        next_bar_base += (size + 0xFFFFF) & ~0xFFFFFull;
    }
     // Allocate guest window
     uint64_t saved_win_off = next_win_off;
     uint64_t win = alloc_window(size);
     if (win == (uint64_t)-1) return -1;

    // Determine if this is a framebuffer BAR (large memory BAR)
    bool is_fb = is_mem && size >= (1024*768*4);

    // Ensure guest linear memory covers window by extending it. This MUST
    // go through the session-scoped accessor: sched_runtime_of returns the
    // wasm3 IM3Runtime, which is null for an owned-engine session, and a
    // silently-skipped extension leaves the BAR window outside guest memory.
    // The guest then derives a window pointer from garbage and hands a
    // garbage length to memory.copy -- a substrate bug presenting as a guest
    // OOB trap thousands of instructions later.
    {
        uint32_t mem_sz = 0;
        sched_session_memory(sid, &mem_sz);
        uint64_t need = win + size;
        if (need > mem_sz) {
            uint32_t target = (uint32_t)need;
            if (sched_session_ensure_bytes(sid, target) != 0) {
                next_win_off = saved_win_off; // release consumed window
                console_puts("[vfio] map_bar: guest memory grow failed need=");
                console_hex64(need);
                console_puts("\n");
                return -1;
            }
            console_puts("[vfio] map_bar: memory extended to ");
            console_hex64(target);
            console_puts("\n");
        }
    }

     /* Enable PCI decoding before anything touches the BAR. A device with
      * the command register's memory-enable bit clear ignores every
      * access to its BARs and reads back zeroes, which is exactly what a
      * real e1000 or HBA did until this was set: the emulated framebuffer
      * masked it, because it answers from a table instead of hardware. */
     if (is_mem) {
         int32_t cmd = pci_read32(bus, dev, fn, 0x04);
         if (cmd != -1)
             pci_write32(bus, dev, fn, 0x04, (uint32_t)cmd | 0x7);
     }

     // Grant IOMMU ownership for this BAR's physical pages
     if (iommu_map_pages(sid, phys, (uint32_t)size) != 0) {
         next_win_off = saved_win_off; // release consumed window
         console_puts("[vfio] map_bar: IOMMU mapping failed, window released\n");
         return -1;
     }

    // Record mapping
    for (auto &m : bar_maps) if (!m.used) {
        m.used = true;
        m.sid = sid; m.bus = bus; m.dev = dev; m.fn = fn; m.bar = bar;
        m.phys = phys; m.size = size; m.win_off = win; m.is_mem = is_mem;
        m.is_wc = is_fb;
        console_puts("[vfio] map_bar sid=");
        console_hex64(sid);
        console_puts(" bdf=");
        console_hex64(bus); console_puts(":");
        console_hex64(dev); console_puts(":");
        console_hex64(fn);
        console_puts(" bar=");
        console_hex64(bar);
        console_puts(" win=");
        console_hex64(win);
        console_puts(" phys=");
        console_hex64(phys);
        console_puts(" size=");
        console_hex64(size);
        if (is_fb) console_puts(" [FB]");
        console_puts("\n");
        if (is_fb) fb_has_display = true;
        return (int64_t)win;
    }
    /* F-AUDIT-9: all bar_maps slots used. Roll back the IOMMU grant
     * and the window allocation that succeeded above so we don't leak
     * domain entries or permanently consume window offsets. */
    iommu_unmap_range(sid, phys, (uint32_t)size);
    next_win_off = saved_win_off;
    console_puts("[vfio] map_bar: bar_maps full, rolled back\n");
    return -1;
}

/* --- Mediated BAR access (ABI v2.5) ---
 *
 * A guest cannot hold a pointer to device MMIO here. Rule 2 keeps one flat
 * identity mapping with no per-session page tables, so the window returned
 * by vfio_map_bar (a range of the guest's own linear memory) cannot be
 * aliased onto the BAR's physical range: one virtual address, one physical
 * one. Anything the guest stores in that window stays in the window --
 * which is why a driver that dereferences the window pointer is talking to
 * a shadow buffer and the device never sees the access.
 *
 * So register access is forwarded: the guest names BDF + BAR + offset and
 * the kernel performs the access at the physical address. Two things are
 * enforced on every access, not just at map time:
 *
 *   - vfio_bdf_assigned_to(): CAP_PCI alone is not enough to touch a
 *     device; it has to be assigned to the caller's session.
 *   - vfio_iommu_permits(): the address must be inside the caller's IOMMU
 *     domain. This is what makes the domain table enforcement instead of
 *     bookkeeping -- it was populated by iommu_map_pages and read by
 *     nobody until here (MEMORY.md GATE-UNFALSIFIABLE / F243).
 *
 * Returns -1 on any denial, so a guest cannot distinguish "refused" from
 * "device said nothing" by timing alone; the audit line says which.
 */
static bool vfio_bar_resolve(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                             uint32_t bar, uint32_t off, uint32_t width,
                             uint64_t *out_phys, uint64_t *out_size, bool *out_is_mem) {
    if (bar > 5 || width == 0 || width > 4)
        return false;
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] bar access: BDF not assigned sid=");
        console_hex64(sid);
        console_puts("\n");
        return false;
    }
    bool is_mem = false;
    uint64_t phys = 0;
    uint64_t size = 0;
    for (auto &m : bar_maps) {
        if (m.used && m.sid == sid && m.bus == bus && m.dev == dev && m.fn == fn &&
            m.bar == bar) {
            phys = m.phys;
            size = m.size;
            is_mem = m.is_mem;
            break;
        }
    }
    if (!size) {
        /* Not mapped through this path: resolve the BAR directly. Access
         * is still gated on assignment and on the IOMMU domain below. */
        if (pci_bar_info(bus, dev, fn, bar, &phys, &size, &is_mem) != 0)
            return false;
    }
    if (static_cast<uint64_t>(off) + width > size) {
        console_puts("[vfio] bar access: out of range bar=");
        console_hex64(bar);
        console_puts(" off=");
        console_hex64(off);
        console_puts(" width=");
        console_hex64(width);
        console_puts(" size=");
        console_hex64(size);
        console_puts("\n");
        return false;
    }
    if (!vfio_iommu_permits(sid, phys + off, width)) {
        console_puts("[vfio] bar access: IOMMU denied sid=");
        console_hex64(sid);
        console_puts(" phys=");
        console_hex64(phys + off);
        console_puts("\n");
        return false;
    }
    *out_phys = phys;
    *out_size = size;
    *out_is_mem = is_mem;
    return true;
}

/* --- Device-visible memory (ABI §12 v2.5) ---
 *
 * A transfer ring needs a buffer the *device* may read and write. Under the
 * flat identity map (rule 2) the device-visible address is the guest's own
 * physical address -- the guest keeps programming the address it already
 * has, and the IOMMU's job is to make exactly those frames reachable for the
 * device and nothing else. That is why this needs no new guest addressing
 * and no per-session page tables.
 *
 * The refusal is the interesting part: if the platform has no IOMMU (or has
 * one this build cannot program), no frame is mapped and the caller gets -1.
 * Handing out an unmapped buffer would let a device DMA anywhere, which is
 * the failure mode this whole path exists to prevent.
 */
/* Which sessions have a *hardware* IOMMU domain. Deliberately separate from
 * domains[sid].used, which means "this session has software-tracked
 * pages" and is set by iommu_map_pages during BAR mapping -- conflating
 * the two let a session with a mapped BAR skip the platform check and fail
 * later at attach, with a much less obvious message. */
static uint32_t g_hw_domains = 0;

static bool vfio_domain_ready(uint32_t sid) {
    if (sid >= MAX_SESSIONS)
        return false;
    /* Ask the platform every time: a domain this build cannot program
     * must refuse, whatever the software table says. */
    if (arch_iommu_present() != IOMMU_PRESENT)
        return false;
    if (!(g_hw_domains & (1u << sid))) {
        if (arch_iommu_domain_create(sid) != 0)
            return false;
        g_hw_domains |= (1u << sid);
        if (domains[sid].used) {
            domains[sid].used = true;
            domains[sid].sid = sid;
        }
    }
    return true;
}

int vfio_dma_map(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                 uint64_t guest_off, uint32_t size) {
    if (!has_cap(sid, SCHED_CAP_PCI)) {
        console_puts("[vfio] dma_map: no CAP_PCI sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] dma_map: BDF not assigned sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (size == 0 || (size % 0x1000) != 0) {
        console_puts("[vfio] dma_map: size must be a non-zero multiple of 4 KiB\n");
        return -1;
    }
    if (!vfio_domain_ready(sid)) {
        console_puts("[vfio] dma_map: no IOMMU on this platform, refusing device DMA\n");
        console_puts("[audit] sid=");
        console_hex64(sid);
        console_puts(" op=DMA_MAP reason=noiommu target=pci\n");
        return -1;
    }
    /* Translate the guest range to physical frames. The session's linear
     * memory is one contiguous rt_malloc allocation and the map is flat, so
     * the guest offset *is* the low bits of a physical address. */
    uint32_t mem_size = 0;
    uint8_t *base = sched_session_memory(sid, &mem_size);
    if (!base || guest_off + size > mem_size) {
        console_puts("[vfio] dma_map: guest range outside session memory\n");
        return -1;
    }
    uint64_t phys_base = (uint64_t)(uintptr_t)base + guest_off;
    iommu_domain &d = domains[sid];
    uint32_t frames = size / 0x1000;
    if (static_cast<uint32_t>(d.num_pages) + frames > MAX_IOMMU_PAGES) {
        console_puts("[vfio] dma_map: domain page table full\n");
        return -1;
    }
    if (arch_iommu_attach(sid, (uint8_t)bus, (uint8_t)dev, (uint8_t)fn) != 0) {
        console_puts("[vfio] dma_map: attach refused\n");
        return -1;
    }
    /* Map into the device domain at the *guest* address, so the guest can
     * keep using it, and record it in the domain table that
     * vfio_iommu_permits consults on every device access. */
    for (uint32_t i = 0; i < frames; i++) {
        uint64_t dev_addr = guest_off + (uint64_t)i * 0x1000;
        uint64_t phys = phys_base + (uint64_t)i * 0x1000;
        if (arch_iommu_map(sid, dev_addr, phys) != 0) {
            for (uint32_t j = 0; j < i; j++)
                arch_iommu_unmap(sid, guest_off + (uint64_t)j * 0x1000);
            arch_iommu_detach(sid, (uint8_t)bus, (uint8_t)dev, (uint8_t)fn);
            console_puts("[vfio] dma_map: mapping refused by the IOMMU\n");
            return -1;
        }
        d.pages[d.num_pages].phys = phys;
        d.pages[d.num_pages].size = 0x1000;
        d.num_pages++;
    }
    if (arch_iommu_invalidate(sid) != 0) {
        console_puts("[vfio] dma_map: invalidate refused\n");
        return -1;
    }
    return 0;
}

int vfio_dma_unmap(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                   uint64_t guest_off, uint32_t size) {
    (void)bus;
    (void)dev;
    (void)fn;
    if (sid >= MAX_SESSIONS || !domains[sid].used)
        return -1;
    if (size == 0 || (size % 0x1000) != 0)
        return -1;
    uint32_t mem_size = 0;
    uint8_t *base = sched_session_memory(sid, &mem_size);
    if (!base || guest_off + size > mem_size)
        return -1;
    uint64_t phys_base = (uint64_t)(uintptr_t)base + guest_off;
    iommu_domain &d = domains[sid];
    for (uint32_t i = 0; i < size / 0x1000; i++) {
        arch_iommu_unmap(sid, guest_off + (uint64_t)i * 0x1000);
        uint64_t phys = phys_base + (uint64_t)i * 0x1000;
        for (int p = 0; p < d.num_pages; p++) {
            if (d.pages[p].phys == phys) {
                d.pages[p] = d.pages[d.num_pages - 1];
                d.num_pages--;
                break;
            }
        }
    }
    arch_iommu_invalidate(sid);
    return 0;
}

int64_t vfio_bar_read(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                      uint32_t bar, uint32_t off, uint32_t width) {
    uint64_t phys = 0;
    uint64_t size = 0;
    bool is_mem = false;
    if (!vfio_bar_resolve(sid, bus, dev, fn, bar, off, width, &phys, &size, &is_mem))
        return -1;
    uint32_t value = 0;
    if (is_mem) {
        /* Identity map: the BAR's physical range is directly addressable.
         * volatile is enough here -- rule 1 keeps core/ free of inline asm,
         * and x86 does not reorder strongly-ordered memory accesses, which
         * is what device MMIO is. */
        const volatile uint8_t *p = reinterpret_cast<const volatile uint8_t *>(
            static_cast<uintptr_t>(phys + off));
        for (uint32_t i = 0; i < width; ++i)
            value |= static_cast<uint32_t>(p[i]) << (i * 8);
    } else {
        uint16_t port = static_cast<uint16_t>(phys + off);
        if (width == 1)
            value = inb(port);
        else if (width == 2)
            value = inw(port);
        else
            value = inl(port);
    }
    return static_cast<int64_t>(value);
}

int32_t vfio_bar_write(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                       uint32_t bar, uint32_t off, uint32_t width, uint32_t value) {
    uint64_t phys = 0;
    uint64_t size = 0;
    bool is_mem = false;
    if (!vfio_bar_resolve(sid, bus, dev, fn, bar, off, width, &phys, &size, &is_mem))
        return -1;
    if (is_mem) {
        /* One access of the requested width -- never a byte loop. Device
         * MMIO regions declare min_access_size (QEMU's xHCI, virtio and
         * e1000 all want 4), and a narrower access to such a region is an
         * unassigned access: QEMU drops it silently and the register never
         * changes, so the guest sees a write that "succeeded" and read
         * back its old value. Reads did not have this problem, which is why
         * capability registers read true values while every write vanished. */
        volatile uint8_t *p = reinterpret_cast<volatile uint8_t *>(
            static_cast<uintptr_t>(phys + off));
        if (width == 1) {
            *p = static_cast<uint8_t>(value);
        } else if (width == 2) {
            *reinterpret_cast<volatile uint16_t *>(p) = static_cast<uint16_t>(value);
        } else {
            *reinterpret_cast<volatile uint32_t *>(p) = value;
        }
    } else {
        uint16_t port = static_cast<uint16_t>(phys + off);
        if (width == 1)
            outb(port, static_cast<uint8_t>(value));
        else if (width == 2)
            outw(port, static_cast<uint16_t>(value));
        else
            outl(port, value);
    }
    return 0;
}

int vfio_unmap_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar) {
    if (!has_cap(sid, SCHED_CAP_PCI)) return -1;
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] unmap_bar: BDF not assigned to sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    for (auto &m : bar_maps) if (m.used && m.sid==sid && m.bus==bus && m.dev==dev && m.fn==fn && m.bar==bar) {
        iommu_unmap_range(sid, m.phys, (uint32_t)m.size);
        m.used = false;
        return 0;
    }
    return -1;
}

int vfio_bind_irq(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t type) {
    if (!has_cap(sid, SCHED_CAP_PCI)) return -1;
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] bind_irq: BDF not assigned to sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (type > 2) return -1;
    // Check device exists
    int32_t vend = pci_read32(bus, dev, fn, 0);
    if (vend == -1 || (vend & 0xFFFF) == 0xFFFF) return -1;

    // Allocate MSI/MSI-X vector
    uint32_t vec = alloc_msi_vector();
    if (vec == 0) {
        console_puts("[vfio] bind_irq: no vectors available\n");
        return -1;
    }

    // Program MSI-X table if type==2 (MSI-X)
    uint64_t msi_addr = 0;
    uint32_t msi_data = 0;
    if (type == 2) {
        if (program_msix(bus, dev, fn, vec) != 0) {
            free_msi_vector(vec);
            console_puts("[vfio] bind_irq: MSI-X programming failed\n");
            return -1;
        }
        msi_addr = 0xFEE00000u;
        msi_data = vec;
    } else if (type == 1) {
        // MSI: simpler, single vector
        msi_addr = 0xFEE00000u;
        msi_data = vec;
    }
    // type==0 (INTX) uses legacy interrupt, no programming needed

    for (auto &d : doorbells) if (!d.used) {
        d.used = true;
        d.sid = sid; d.bus = bus; d.dev = dev; d.fn = fn; d.type = type;
        d.handle = next_handle++;
        d.pending = false;
        d.msi_vector = vec;
        d.msi_addr = msi_addr;
        d.msi_data = msi_data;
        console_puts("[vfio] bind_irq sid=");
        console_hex64(sid);
        console_puts(" handle=");
        console_hex64(d.handle);
        console_puts(" vec=");
        console_hex64(vec);
        console_puts("\n");
        return (int)d.handle;
    }
    free_msi_vector(vec);
    return -1;
}

int vfio_doorbell_wait(uint32_t sid, uint32_t handle, uint32_t timeout_ms) {
    for (auto &d : doorbells) if (d.used && d.sid==sid && d.handle==handle) {
        if (d.pending) {
            d.pending = false;
            return 0; // fired
        }
        if (timeout_ms == 0) return 1;
        uint64_t start = wasi_now_ns();
        uint64_t timeout_ns = (uint64_t)timeout_ms * 1000000ULL;
        while (true) {
            if (d.pending) { d.pending = false; return 0; }
            uint64_t now = wasi_now_ns();
            if ((int64_t)(now - start) >= (int64_t)timeout_ns) return 1;
            sched_yield_current();
        }
    }
    return -1;
}

// Called from timer ISR or device poll to fire doorbell
void vfio_fire_doorbell(uint32_t bus, uint32_t dev, uint32_t fn) {
    for (auto &d : doorbells) if (d.used && d.bus==bus && d.dev==dev && d.fn==fn) {
        d.pending = true;
    }
}

// --- Framebuffer control (§13) ---

int vfio_fb_set_mode(uint32_t sid, uint32_t w, uint32_t h, uint32_t bpp) {
    if (!has_cap(sid, SCHED_CAP_FB)) {
        console_puts("[vfio] fb_set_mode: no CAP_FB\n");
        return -1;
    }
    if (bpp != 32) return -1;
    if (w == 0 || h == 0 || w > 4096 || h > 4096) return -1;
    fb_w = w; fb_h = h; fb_bpp = bpp;
    fb_has_display = true;
    // Program Bochs DISPI for real display hardware (0x01CE index, 0x01CF data).
    // Only when a real Bochs display device is present (scanout_bochs).
    // GOP framebuffer must NOT have Bochs DISPI registers programmed.
    if (scanout_bochs) {
        outw(0x01CE, 0x0000); (void)inw(0x01CF); // ID (read to ack)
        outw(0x01CE, 0x0004); outw(0x01CF, 0x0041); // ENABLE: enable + LFB + clear
        outw(0x01CE, 0x0001); outw(0x01CF, (uint16_t)w);   // XRES
        outw(0x01CE, 0x0002); outw(0x01CF, (uint16_t)h);   // YRES
        outw(0x01CE, 0x0003); outw(0x01CF, (uint16_t)bpp); // BPP
        outw(0x01CE, 0x0006); outw(0x01CF, (uint16_t)w);   // VIRT_WIDTH
        outw(0x01CE, 0x0007); outw(0x01CF, (uint16_t)h);   // VIRT_HEIGHT
        outw(0x01CE, 0x0008); outw(0x01CF, 0);            // X_OFFSET
        outw(0x01CE, 0x0009); outw(0x01CF, 0);            // Y_OFFSET
        console_puts("[vfio] fb_set_mode hw ");
    } else if (scanout_enabled) {
        console_puts("[vfio] fb_set_mode ");
    } else {
        console_puts("[vfio] fb_set_mode ");
    }
    console_hex64(w);
    console_puts("x");
    console_hex64(h);
    console_puts(" bpp=");
    console_hex64(bpp);
    console_puts("\n");
    return 0;
}

int vfio_fb_set_cursor(uint32_t sid, uint32_t x, uint32_t y) {
    if (!has_cap(sid, SCHED_CAP_FB)) return -1;
    fb_cursor_x = x; fb_cursor_y = y;
    return 0;
}

// Present/flip: copy a session's framebuffer BAR window to the physical LFB.
// For wasm guests this is the only way pixels reach hardware — the guest
// writes into its linear memory at win_off, and we copy to the physical
// framebuffer address. When a real PCI display device is present (scanout
// enabled), pixels go to the hardware framebuffer so QEMU scans them out to
// a window. Otherwise we copy to the headless pool buffer for test verification.
// Returns 0 on success, -1 if no FB BAR mapped for this session.
int vfio_fb_present(uint32_t sid) {
    if (!has_cap(sid, SCHED_CAP_FB)) {
        console_puts("[vfio] fb_present: no CAP_FB sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    // Find the framebuffer BAR mapping for this session
    for (auto &m : bar_maps) {
        if (m.used && m.sid == sid && m.is_wc) {
            uint64_t size = (uint64_t)fb_w * fb_h * 4;
            if (size > m.size) size = m.size;
            if (size == 0) return -1;
            // Source: guest linear memory at win_off. Resolved per call, and
            // never cached: growing the session's memory can relocate the
            // buffer, so a cached pointer would read freed memory.
            uint8_t *src = nullptr;
            uint32_t mem_sz = 0;
            uint8_t *mem = sched_session_memory(sid, &mem_sz);
            if (mem && m.win_off + size <= mem_sz) {
                src = mem + m.win_off;
            }
            // Destination: real hardware framebuffer if a display is present,
            // otherwise the identity-mapped headless pool buffer.
            volatile uint8_t *dst = nullptr;
            if (scanout_enabled && scanout_phys != 0) {
                if (size > scanout_size) size = scanout_size;
                dst = (volatile uint8_t *)(uintptr_t)scanout_phys;
            } else {
                dst = (volatile uint8_t *)(uintptr_t)m.phys;
            }
            if (src && dst) {
                /* Scanline-by-scanline copy handles stride mismatch between
                 * the guest render buffer (width*4) and the display fb. */
                uint64_t src_stride = (uint64_t)fb_w * 4;
                uint64_t lines = fb_h;
                uint64_t copy_w = src_stride;
                if (fb_stride < copy_w) copy_w = fb_stride;
                for (uint64_t y = 0; y < lines; y++) {
                    uint64_t so = y * src_stride;
                    uint64_t doff = y * fb_stride;
                    if (so + copy_w > (uint64_t)mem_sz) break;
                    if (doff + copy_w > scanout_size) break;
                    for (uint64_t x = 0; x < copy_w; x++)
                        dst[doff + x] = src[so + x];
                }
            } else if (dst) {
                for (uint64_t i = 0; i < size; i++)
                    dst[i] = 0;
            }
            console_puts("[vfio] fb_present sid=");
            console_hex64(sid);
            console_puts(" size=");
            console_hex64(size);
            console_puts("\n");
            return 0;
        }
    }
    return -1;
}

/* --- Framebuffer console: render kernel text output to the GOP scanout --- */
static uint32_t term_cols = 0, term_rows = 0, term_x = 0, term_y = 0;
static bool term_ok = false;
static const uint32_t term_fg = 0xCCFFCC;
static const uint32_t term_bg = 0x101020;

static void term_putpixel(uint32_t x, uint32_t y, uint32_t rgb) {
    if (!scanout_enabled || scanout_phys == 0 || fb_stride == 0) return;
    uint64_t off = (uint64_t)y * fb_stride + x * 4;
    if (off + 4 > scanout_size) return;
    volatile uint8_t *p = (volatile uint8_t *)(uintptr_t)(scanout_phys + off);
    p[0] = (uint8_t)rgb; p[1] = (uint8_t)(rgb >> 8);
    p[2] = (uint8_t)(rgb >> 16); p[3] = 0xFF;
}

static void term_clear(void) {
    if (!scanout_enabled || scanout_phys == 0 || fb_stride == 0) return;
    for (uint32_t y = 0; y < term_rows * FONT_H; y++)
        for (uint32_t x = 0; x < term_cols * FONT_W; x++)
            term_putpixel(x, y, term_bg);
}

static void term_drawchar(uint32_t cx, uint32_t cy, char ch) {
    uint8_t idx = (uint8_t)ch;
    if (idx < FONT_FIRST || idx >= FONT_FIRST + FONT_COUNT) idx = '?';
    idx -= FONT_FIRST;
    uint32_t px = cx * FONT_W, py = cy * FONT_H;
    for (uint32_t row = 0; row < FONT_H; row++) {
        uint8_t bits = font8x8[idx][row];
        for (uint32_t col = 0; col < FONT_W; col++)
            term_putpixel(px + col, py + row, (bits & (0x80 >> col)) ? term_fg : term_bg);
    }
}

void term_init(void) {
    if (!scanout_enabled || scanout_phys == 0 || fb_stride == 0) { term_ok = false; return; }
    term_cols = (fb_stride / 4) / FONT_W;
    term_rows = (uint32_t)(scanout_size / fb_stride) / FONT_H;
    if (!term_cols || !term_rows) { term_ok = false; return; }
    term_x = 0; term_y = 0;
    term_clear();
    term_ok = true;
}

void fb_console_puts(const char *s) {
    if (!term_ok) term_init();
    if (!term_ok) return;
    while (*s) {
        char c = *s++;
        if (c == '\n') { term_x = 0; term_y++; if (term_y >= term_rows) term_y = term_rows - 1; continue; }
        if (c == '\r') { term_x = 0; continue; }
        if (term_x >= term_cols) { term_x = 0; term_y++; if (term_y >= term_rows) term_y = term_rows - 1; }
        term_drawchar(term_x, term_y, c);
        term_x++;
    }
}

bool fb_console_active(void) { return term_ok; }

// --- FLR recovery: re-bind after GPU reset ---

int vfio_recover_after_flr(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn) {
    if (!has_cap(sid, SCHED_CAP_PCI)) return -1;
    if (!vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        console_puts("[vfio] flr: BDF not assigned\n");
        return -1;
    }
    console_puts("[vfio] recover_after_flr sid=");
    console_hex64(sid);
    console_puts(" for ");
    console_hex64(bus);
    console_puts(":");
    console_hex64(dev);
    console_puts(".");
    console_hex64(fn);
    console_puts("\n");

    // Unmap all BARs for this device owned by sid
    for (auto &m : bar_maps) {
        if (m.used && m.sid==sid && m.bus==bus && m.dev==dev && m.fn==fn) {
            m.used = false;
        }
    }
    // Unbind all doorbells for this device owned by sid
    for (auto &d : doorbells) {
        if (d.used && d.sid==sid && d.bus==bus && d.dev==dev && d.fn==fn) {
            free_msi_vector(d.msi_vector);
            d.used = false;
        }
    }
    // Re-issue FLR to ensure device is in clean state
    return pci_flr(bus, dev, fn);
}

// --- Devman helpers ---

int vfio_dev_count(void) {
    struct pci_dev devs[MAX_PCI_DEVS];
    int n = 0;
    if (pci_enumerate(devs, MAX_PCI_DEVS, &n) != 0) return 0;
    return n;
}

int vfio_enumerate(struct vfio_pci_info *out, int max) {
    struct pci_dev devs[MAX_PCI_DEVS];
    int n = 0;
    if (pci_enumerate(devs, MAX_PCI_DEVS, &n) != 0) return 0;
    int o = 0;
    for (int i = 0; i < n && o < max; i++) {
        out[o].bus = devs[i].bus;
        out[o].dev = devs[i].dev;
        out[o].fn = devs[i].fn;
        out[o].vendor = devs[i].vendor;
        out[o].device = devs[i].device;
        o++;
    }
    return o;
}

// Check that a PCI BDF is assigned to the calling session (or caller is admin).
// This closes the assignment gap: CAP_PCI alone is not enough to touch a device;
// the device must be explicitly assigned to the caller's session.
bool vfio_bdf_assigned_to(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn) {
    // Admin (init) can always access any device
    if (has_cap(sid, SCHED_CAP_DEVMAN)) return true;
    // Virtual framebuffer: kernel-internal, no assignment needed
    if (pci_is_vfb(bus, dev, fn)) return true;
    for (auto &a : assigns) {
        if (a.used && a.target_sid == sid && a.bus == bus && a.dev == dev && a.fn == fn)
            return true;
    }
    return false;
}

int vfio_assign_pci(uint32_t target_sid, uint8_t bus, uint8_t dev, uint8_t fn, uint32_t caller_sid) {
    if (!has_cap(caller_sid, SCHED_CAP_DEVMAN)) return -1;
    int32_t v = pci_read32(bus, dev, fn, 0);
    if (v == -1 || (v & 0xFFFF) == 0xFFFF) return -1;
    for (auto &a : assigns) if (!a.used) {
        a.used = true;
        a.target_sid = target_sid; a.bus = bus; a.dev = dev; a.fn = fn;
        console_puts("[vfio] assign ");
        console_hex64(bus);
        console_puts(":");
        console_hex64(dev);
        console_puts(".");
        console_hex64(fn);
        console_puts(" -> sid=");
        console_hex64(target_sid);
        console_puts("\n");
        return 0;
    }
    return -1;
}

// --- Session cleanup: called when a session dies ---

void vfio_session_cleanup(uint32_t sid) {
    // Unmap all BARs owned by this session
    for (auto &m : bar_maps) {
        if (m.used && m.sid == sid) m.used = false;
    }
    // Unbind all doorbells owned by this session
    for (auto &d : doorbells) {
        if (d.used && d.sid == sid) {
            free_msi_vector(d.msi_vector);
            d.used = false;
        }
    }
    // Tear down IOMMU domain
    iommu_unmap_all(sid);
    // Remove PCI assignments targeting this session
    for (auto &a : assigns) {
        if (a.used && a.target_sid == sid) a.used = false;
    }
}
