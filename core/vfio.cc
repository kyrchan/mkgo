#include "vfio.h"
#include "pci.h"
#include "sched.h"
#include "lib.h"
#include "mm.h"
#include "plat.h"

extern "C" {
#include "wasm3.h"
}
extern uint64_t wasi_now_ns(void);
extern void sched_yield_current(void);

// Minimal VFIO foundation — single-ABI, no IOMMU enforcement yet (identity map).
// Each session has up to 4 BAR mappings and 4 doorbells. Real IOMMU page tables
// and MSI-X programming will be added incrementally; current stub validates
// capability gating, PCI enumeration, BAR existence, and guest window allocation.

static constexpr int MAX_BAR_MAPS = 4;
static constexpr int MAX_DOORBELLS = 4;
static constexpr int MAX_PCI_DEVS = 16;

struct bar_map {
    bool used;
    uint32_t sid;
    uint32_t bus, dev, fn, bar;
    uint64_t phys, size;
    uint64_t win_off; // guest linear window offset
    bool is_mem;
};

struct doorbell {
    bool used;
    uint32_t sid;
    uint32_t handle;
    uint32_t bus, dev, fn, type;
    bool pending;
    uint64_t last_fire;
};

struct pci_assign {
    bool used;
    uint32_t target_sid;
    uint8_t bus, dev, fn;
};

static bar_map bar_maps[MAX_BAR_MAPS];
static doorbell doorbells[MAX_DOORBELLS];
static pci_assign assigns[MAX_PCI_DEVS];
static uint32_t next_handle = 1;

// Framebuffer state (single display, §13)
static uint32_t fb_w = 0, fb_h = 0, fb_bpp = 32;
static uint32_t fb_cursor_x = 0, fb_cursor_y = 0;
static bool fb_has_display = false; // set after first set_mode

void vfio_init(void) {
    for (auto &m : bar_maps) m.used = false;
    for (auto &d : doorbells) d.used = false;
    for (auto &a : assigns) a.used = false;
    next_handle = 1;
    fb_w = 1024; fb_h = 768; fb_bpp = 32; // default QEMU stdvga
}

// Simple guest window allocator — bump at 64M+ like net windows.
// Real implementation will grow wasm memory via m3_ExtendMemory.
static uint64_t next_win_off = 0x5000000; // 80M (after net windows at 64M)
static constexpr uint64_t WIN_ALIGN = 4096;

static uint64_t alloc_window(uint64_t size) {
    uint64_t off = (next_win_off + WIN_ALIGN - 1) & ~(WIN_ALIGN - 1);
    next_win_off = off + ((size + WIN_ALIGN - 1) & ~(WIN_ALIGN - 1));
    // Cap at 256M to stay within wasm32 4G
    if (next_win_off > 0x10000000)
        return (uint64_t)-1;
    return off;
}

static bool has_cap(uint32_t sid, uint64_t cap) {
    return (sched_capmask_of(sid) & cap) != 0;
}

static bool is_assigned_to(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn) {
    // If no explicit assigns, allow any PCI-cap holder (permissive v1).
    // Once assigns exist, enforce.
    bool any = false;
    for (auto &a : assigns) if (a.used) { any = true; break; }
    if (!any) return has_cap(sid, SCHED_CAP_PCI);
    for (auto &a : assigns) if (a.used && a.bus==bus && a.dev==dev && a.fn==fn)
        return a.target_sid == sid;
    return false;
}
// used in future assignment check
[[maybe_unused]] static bool _check_assign(uint32_t s,uint32_t b,uint32_t d,uint32_t f){return is_assigned_to(s,b,d,f);}

int64_t vfio_map_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar) {
    if (!has_cap(sid, SCHED_CAP_PCI)) {
        console_puts("[vfio] map_bar: no CAP_PCI sid=");
        console_hex64(sid);
        console_puts("\n");
        return -1;
    }
    if (bar > 5) return -1;
    // Check already mapped
    for (auto &m : bar_maps) if (m.used && m.sid==sid && m.bus==bus && m.dev==dev && m.fn==fn && m.bar==bar)
        return (int64_t)m.win_off;
    uint64_t phys, size;
    bool is_mem;
    if (pci_bar_info(bus, dev, fn, bar, &phys, &size, &is_mem) != 0) {
        console_puts("[vfio] map_bar: bar_info failed\n");
        return -1;
    }
    // Allocate guest window
    uint64_t win = alloc_window(size);
    if (win == (uint64_t)-1) return -1;
    // Try to ensure guest linear memory covers window (best effort).
    void *rt = sched_runtime_of(sid);
    if (rt) {
        // wasm3: try to extend memory if needed
        uint32_t mem_sz = 0;
        uint8_t *base = m3_GetMemory((IM3Runtime)rt, &mem_sz, 0);
        (void)base;
        uint64_t need = win + size;
        if (need > mem_sz) {
            uint32_t pages = (uint32_t)((need - mem_sz + 65535) / 65536);
            // m3_ExtendMemory returns new size or 0
            // If not available, just log — guest may fault on access but we return win
            // to let wasi glue grow via its own path.
            // We use weak symbol check via dlsym? Just skip for now.
            (void)pages;
        }
    }
    // Record mapping
    for (auto &m : bar_maps) if (!m.used) {
        m.used = true;
        m.sid = sid; m.bus = bus; m.dev = dev; m.fn = fn; m.bar = bar;
        m.phys = phys; m.size = size; m.win_off = win; m.is_mem = is_mem;
        console_puts("[vfio] map_bar sid=");
        console_hex64(sid);
        console_puts(" bar=");
        console_hex64(bar);
        console_puts(" win=");
        console_hex64(win);
        console_puts(" phys=");
        console_hex64(phys);
        console_puts(" size=");
        console_hex64(size);
        console_puts("\n");
        // For framebuffer BAR, remember LFB
        if (is_mem && size >= (1024*768*4)) {
            fb_has_display = true;
        }
        return (int64_t)win;
    }
    return -1;
}

int vfio_unmap_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar) {
    if (!has_cap(sid, SCHED_CAP_PCI)) return -1;
    for (auto &m : bar_maps) if (m.used && m.sid==sid && m.bus==bus && m.dev==dev && m.fn==fn && m.bar==bar) {
        m.used = false;
        return 0;
    }
    return -1;
}

int vfio_bind_irq(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t type) {
    if (!has_cap(sid, SCHED_CAP_PCI)) return -1;
    if (type > 2) return -1;
    // Check device exists
    int32_t vend = pci_read32(bus, dev, fn, 0);
    if (vend == -1 || (vend & 0xFFFF) == 0xFFFF) return -1;
    for (auto &d : doorbells) if (!d.used) {
        d.used = true;
        d.sid = sid; d.bus = bus; d.dev = dev; d.fn = fn; d.type = type;
        d.handle = next_handle++;
        d.pending = false;
        console_puts("[vfio] bind_irq sid=");
        console_hex64(sid);
        console_puts(" handle=");
        console_hex64(d.handle);
        console_puts("\n");
        // For now, program MSI-X table stub: write MSI message address/data via pci_write32
        // Real IOMMU IR remapping will be here.
        return (int)d.handle;
    }
    return -1;
}

int vfio_doorbell_wait(uint32_t sid, uint32_t handle, uint32_t timeout_ms) {
    for (auto &d : doorbells) if (d.used && d.sid==sid && d.handle==handle) {
        if (d.pending) {
            d.pending = false;
            return 0; // fired
        }
        // For v1 polled: check if any virtio/net IRQ would have fired via polling
        // For stub, we just return timeout if not pending.
        // If timeout_ms == 0, poll once (non-blocking)
        if (timeout_ms == 0) return 1;
        // Simple yield loop for timeout: each yield ~ quantum, approximate
        uint64_t start = wasi_now_ns();
        uint64_t timeout_ns = (uint64_t)timeout_ms * 1000000ULL;
        while (true) {
            if (d.pending) { d.pending = false; return 0; }
            uint64_t now = wasi_now_ns();
            if (now - start >= timeout_ns) return 1;
            sched_yield_current();
        }
    }
    return -1;
}

// Called from timer ISR or device poll to fire doorbell (stub)
void vfio_fire_doorbell(uint32_t bus, uint32_t dev, uint32_t fn) {
    for (auto &d : doorbells) if (d.used && d.bus==bus && d.dev==dev && d.fn==fn) {
        d.pending = true;
    }
}

int vfio_fb_set_mode(uint32_t sid, uint32_t w, uint32_t h, uint32_t bpp) {
    if (!has_cap(sid, SCHED_CAP_FB)) {
        console_puts("[vfio] fb_set_mode: no CAP_FB\n");
        return -1;
    }
    if (bpp != 32) return -1;
    if (w == 0 || h == 0 || w > 4096 || h > 4096) return -1;
    fb_w = w; fb_h = h; fb_bpp = bpp;
    fb_has_display = true;
    console_puts("[vfio] fb_set_mode ");
    console_hex64(w);
    console_puts("x");
    console_hex64(h);
    console_puts(" bpp=");
    console_hex64(bpp);
    console_puts("\n");
    // Real hardware: program CRTC via Bochs DISPI 0x01CE/0x01CF or via BAR
    // QEMU stdvga: we would write to 0x1CE index 0x00/0x01 etc. For stub, just store.
    // If we have a BAR mapping for LFB, we could reallocate window size.
    return 0;
}

int vfio_fb_set_cursor(uint32_t sid, uint32_t x, uint32_t y) {
    if (!has_cap(sid, SCHED_CAP_FB)) return -1;
    fb_cursor_x = x; fb_cursor_y = y;
    return 0;
}

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

int vfio_assign_pci(uint32_t target_sid, uint8_t bus, uint8_t dev, uint8_t fn, uint32_t caller_sid) {
    if (!has_cap(caller_sid, SCHED_CAP_DEVMAN)) return -1;
    // Check device exists
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
