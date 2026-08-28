#include "pci.h"
#include "lib.h"
#include "io.h"
#include "mm.h"
#include "plat.h"

static uint32_t pci_cfg_addr(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset) {
    return 0x80000000u | (bus << 16) | (dev << 11) | (fn << 8) | (offset & 0xFCu);
}

// --- Virtual framebuffer (VFB) PCI device ---
// Headless VFIO test device at BDF 0:1:0. Provides a memory BAR backed by
// an allocated framebuffer buffer so graphics.wasm can map and render.
// Class 0x03 (display controller), vendor 0x1234, device 0x5678.
static constexpr uint32_t VFB_BUS = 0;
static constexpr uint32_t VFB_DEV = 1;
static constexpr uint32_t VFB_FN = 0;
static constexpr uint32_t VFB_VENDOR = 0x1234;
static constexpr uint32_t VFB_DEVICE = 0x5678;
static constexpr uint32_t VFB_CLASS = 0x030000; // display controller, VGA-compatible
static constexpr uint32_t VFB_BAR_SIZE = 0x400000; // 4 MB (1024x768x4 = 3 MB, round up)

static struct {
    bool enabled;
    uint32_t bar0;      // current BAR0 value (physical base + flags)
    uint32_t base;      // framebuffer physical base address
    bool probe;         // size-probe mode: next read returns mask
} g_vfb;

void pci_vfb_init(uint32_t width, uint32_t height) {
    (void)width; (void)height;
    if (g_vfb.enabled) return;
    // Allocate framebuffer buffer from the mm pool (identity-mapped).
    void *buf = mm_alloc(VFB_BAR_SIZE, 0x1000);
    if (!buf) {
        console_puts("[pci] vfb: alloc failed\n");
        return;
    }
    g_vfb.base = (uint32_t)(uintptr_t)buf;
    g_vfb.bar0 = g_vfb.base; // memory BAR, 32-bit, non-prefetchable (flags=0)
    g_vfb.probe = false;
    g_vfb.enabled = true;
    console_puts("[pci] vfb: fb base=");
    console_hex64(g_vfb.base);
    console_puts(" size=");
    console_hex64(VFB_BAR_SIZE);
    console_puts("\n");
}

bool pci_is_vfb(uint32_t bus, uint32_t dev, uint32_t fn) {
    return g_vfb.enabled && bus == VFB_BUS && dev == VFB_DEV && fn == VFB_FN;
}

static uint32_t vfb_rd32(uint32_t offset) {
    switch (offset) {
    case 0x00: return VFB_VENDOR | (VFB_DEVICE << 16); // vendor | device
    case 0x04: return 0x00000007; // status=0, command=IO|MEM|BUSMASTER
    case 0x08: return VFB_CLASS; // class code (display) at bits 24:31
    case 0x10: // BAR0
        if (g_vfb.probe) {
            g_vfb.probe = false;
            uint32_t size_m1 = (uint32_t)(VFB_BAR_SIZE - 1);
            uint32_t inverted = (uint32_t)(~size_m1);
            uint32_t mask = inverted & 0xFFFFFFF0u;
            return mask;
        }
        return g_vfb.bar0;
    case 0x14: case 0x18: case 0x1C: case 0x20: case 0x24:
        return 0; // no BARs 1-5
    case 0x28: return 0; // cardbus CIS
    case 0x2C: return 0xFFFF0000; // subsys vendor 0, subsys id 0
    case 0x30: return 0; // expansion ROM
    case 0x34: return 0; // capabilities pointer
    case 0x3C: return 0; // interrupt line/pin
    default: return 0;
    }
}

static void vfb_wr32(uint32_t offset, uint32_t val) {
    switch (offset) {
    case 0x04: // command register — ignore writes
        break;
    case 0x10: // BAR0
        if (val == 0xFFFFFFFFu) {
            g_vfb.probe = true; // size probe: next read returns mask
        } else {
            g_vfb.bar0 = val & 0xFFFFFFF0u;
            g_vfb.probe = false;
        }
        break;
    default:
        break; // ignore writes to other registers
    }
}

int32_t pci_read32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset) {
    if (bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset & 3))
        return -1;
    if (pci_is_vfb(bus, dev, fn))
        return (int32_t)vfb_rd32(offset);
    // Bounds: offset must be 4-aligned, we mask inside
    outl(0xCF8, pci_cfg_addr(bus, dev, fn, offset));
    return (int32_t)inl(0xCFC);
}

int32_t pci_write32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset, uint32_t val) {
    if (bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset & 3))
        return -1;
    if (pci_is_vfb(bus, dev, fn)) {
        vfb_wr32(offset, val);
        return 0;
    }
    outl(0xCF8, pci_cfg_addr(bus, dev, fn, offset));
    outl(0xCFC, val);
    return 0;
}

int pci_bar_info(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar, uint64_t *out_phys, uint64_t *out_size, bool *out_is_mem) {
    if (bar > 5 || !out_phys || !out_size || !out_is_mem)
        return -1;
    uint32_t off = 0x10 + bar * 4;
    int32_t v = pci_read32(bus, dev, fn, off);
    if (v == -1 && bar == 0) {
        // Check vendor read to see if device exists
        int32_t vend = pci_read32(bus, dev, fn, 0);
        if (vend == -1 || (vend & 0xFFFF) == 0xFFFF)
            return -1;
    }
    uint32_t bar_val = (uint32_t)v;
    if (bar_val == 0)
        return -1;
    bool is_mem = !(bar_val & 1);
    *out_is_mem = is_mem;
    *out_phys = 0;
    *out_size = 0;
    if (!is_mem) {
        // I/O BAR
        *out_phys = bar_val & 0xFFFFFFFCu;
        // Size probing: write all 1s, read back, restore
        pci_write32(bus, dev, fn, off, 0xFFFFFFFFu);
        uint32_t sz = (uint32_t)pci_read32(bus, dev, fn, off);
        pci_write32(bus, dev, fn, off, bar_val);
        if (sz == 0 || sz == 0xFFFFFFFFu)
            return -1;
        uint32_t mask = sz & 0xFFFFFFFCu;
        *out_size = (~mask) + 1;
        if (*out_size == 0)
            *out_size = 0x100;
        return 0;
    }
    // MEM BAR
    bool is64 = (bar_val & 0x6) == 0x4;
    uint64_t phys = bar_val & 0xFFFFFFF0u;
    uint64_t size = 0;
    if (is64) {
        if (bar >= 5)
            return -1;
        uint32_t high = (uint32_t)pci_read32(bus, dev, fn, off + 4);
        phys |= ((uint64_t)high << 32);
        // Size probe 64-bit
        uint32_t orig_low = bar_val;
        uint32_t orig_high = high;
        pci_write32(bus, dev, fn, off, 0xFFFFFFFFu);
        pci_write32(bus, dev, fn, off + 4, 0xFFFFFFFFu);
        uint32_t sz_low = (uint32_t)pci_read32(bus, dev, fn, off);
        uint32_t sz_high = (uint32_t)pci_read32(bus, dev, fn, off + 4);
        pci_write32(bus, dev, fn, off, orig_low);
        pci_write32(bus, dev, fn, off + 4, orig_high);
        uint64_t sz = ((uint64_t)sz_high << 32) | sz_low;
        sz &= 0xFFFFFFFFFFFFFFF0ULL;
        if (sz == 0 || sz == 0xFFFFFFFFFFFFFFF0ULL)
            return -1;
        size = (~sz) + 1;
        if (size == 0) size = 0x1000;
    } else {
        pci_write32(bus, dev, fn, off, 0xFFFFFFFFu);
        uint32_t sz = (uint32_t)pci_read32(bus, dev, fn, off);
        pci_write32(bus, dev, fn, off, bar_val);
        sz &= 0xFFFFFFF0u;
        if (sz == 0 || sz == 0xFFFFFFF0u)
            return -1;
        // Invert in 32-bit space, then cast to 64-bit. Casting before
        // inverting would zero-extend and produce a corrupted size.
        size = (uint64_t)(~(uint32_t)sz) + 1;
        if (size == 0) size = 0x1000;
    }
    *out_phys = phys;
    *out_size = size;
    return 0;
}

int pci_enable_busmaster(uint32_t bus, uint32_t dev, uint32_t fn) {
    int32_t cmd = pci_read32(bus, dev, fn, 0x04);
    if (cmd == -1)
        return -1;
    uint32_t v = (uint32_t)cmd;
    // CMD bits: 0 IO, 1 MEM, 2 BusMaster
    v |= 0x7;
    return pci_write32(bus, dev, fn, 0x04, v);
}

int pci_flr(uint32_t bus, uint32_t dev, uint32_t fn) {
    // Find PCIe cap
    int32_t cap_ptr = pci_read32(bus, dev, fn, 0x34);
    if (cap_ptr == -1)
        return -1;
    uint8_t ptr = cap_ptr & 0xFF;
    for (int i = 0; i < 16 && ptr >= 0x40; i++) {
        int32_t hdr = pci_read32(bus, dev, fn, ptr);
        if (hdr == -1) break;
        uint8_t cap_id = hdr & 0xFF;
        uint8_t next = (hdr >> 8) & 0xFF;
        if (cap_id == 0x10) { // PCIe
            int32_t cap = pci_read32(bus, dev, fn, ptr + 8);
            if (cap == -1) return -1;
            // Device Capabilities has FLR bit 28?
            // FLR is initiated via Device Control bit 15
            int32_t ctrl = pci_read32(bus, dev, fn, ptr + 8);
            // Actually Device Control at offset +8, FLR bit 15
            // Write 1<<15
            uint32_t v = (uint32_t)ctrl;
            v |= (1u << 15);
            pci_write32(bus, dev, fn, ptr + 8, v);
            // Poll for completion? Assume ok
            return 0;
        }
        ptr = next;
    }
    return -1; // No PCIe cap / no FLR
}

int pci_enumerate(struct pci_dev *out, int max, int *count) {
    if (!out || !count || max <= 0) return -1;
    int n = 0;
    for (uint32_t bus = 0; bus < 256 && n < max; bus++) {
        for (uint32_t dev = 0; dev < 32 && n < max; dev++) {
            int32_t vend = pci_read32(bus, dev, 0, 0);
            if (vend == -1 || (vend & 0xFFFF) == 0xFFFF)
                continue;
            for (uint32_t fn = 0; fn < 8 && n < max; fn++) {
                int32_t v = pci_read32(bus, dev, fn, 0);
                if (v == -1 || (v & 0xFFFF) == 0xFFFF) {
                    if (fn == 0) break;
                    continue;
                }
                int32_t c = pci_read32(bus, dev, fn, 0x08);
                if (c == -1) continue;
                struct pci_dev *d = &out[n];
                d->bus = (uint8_t)bus;
                d->dev = (uint8_t)dev;
                d->fn = (uint8_t)fn;
                d->vendor = v & 0xFFFF;
                d->device = (v >> 16) & 0xFFFF;
                d->class_code = (c >> 24) & 0xFF;
                d->subclass = (c >> 16) & 0xFF;
                d->prog_if = (c >> 8) & 0xFF;
                for (int b = 0; b < 6; b++) {
                    int32_t bv = pci_read32(bus, dev, fn, 0x10 + b*4);
                    d->bar[b] = bv == -1 ? 0 : (uint32_t)bv;
                }
                n++;
                // Single function?
                int32_t hdr = pci_read32(bus, dev, fn, 0x0C);
                if (fn == 0 && (hdr & 0x800000) == 0)
                    break;
            }
        }
    }
    *count = n;
    return 0;
}

// --- MSI-X ---
#define PCI_CAP_MSI_X 0x11

int pci_msix_find(uint32_t bus, uint32_t dev, uint32_t fn, struct pci_msix *out) {
    if (!out) return -1;
    // Capabilities pointer at 0x34 (bit 0-7), but only if status reg bit 4 set
    int32_t status = pci_read32(bus, dev, fn, 0x06);
    if (status == -1) return -1;
    if (!((status >> 16) & 0x10)) return -1; // no capabilities
    int32_t cap_ptr = pci_read32(bus, dev, fn, 0x34);
    if (cap_ptr == -1) return -1;
    uint8_t ptr = cap_ptr & 0xFF;
    for (int i = 0; i < 16 && ptr >= 0x40; i++) {
        int32_t hdr = pci_read32(bus, dev, fn, ptr);
        if (hdr == -1) break;
        uint8_t cap_id = hdr & 0xFF;
        uint8_t next = (hdr >> 8) & 0xFF;
        if (cap_id == PCI_CAP_MSI_X) {
            int32_t tbl = pci_read32(bus, dev, fn, ptr + 4);
            if (tbl == -1) return -1;
            out->cap_off = ptr;
            out->bir = tbl & 0x7;
            out->table_off = (tbl & ~0x7u) >> 3;
            int32_t ctrl = pci_read32(bus, dev, fn, ptr + 2);
            if (ctrl == -1) return -1;
            out->num_vecs = (uint16_t)((ctrl & 0x7FF) + 1);
            out->table_phys = 0;
            return 0;
        }
        ptr = next;
    }
    return -1;
}

int pci_msix_enable(uint32_t bus, uint32_t dev, uint32_t fn, const struct pci_msix *m, uint16_t num_vecs) {
    if (!m || m->cap_off < 0x40) return -1;
    int32_t ctrl = pci_read32(bus, dev, fn, m->cap_off + 2);
    if (ctrl == -1) return -1;
    // Table size = num_vecs - 1, keep function mask clear, set enable bit 15
    uint32_t v = ((uint32_t)(num_vecs - 1) & 0x7FF) | (1u << 15);
    // Preserve function mask bit 14 clear (0 = enabled)
    v &= ~(1u << 14);
    if (pci_write32(bus, dev, fn, m->cap_off + 2, v) != 0) return -1;
    // Resolve table physical address from BAR
    uint64_t bar_phys, bar_size;
    bool is_mem;
    if (pci_bar_info(bus, dev, fn, m->bir, &bar_phys, &bar_size, &is_mem) != 0)
        return -1;
    if (!is_mem) return -1;
    (void)bar_size;
    return 0;
}

int pci_msix_set_vector(uint32_t bus, uint32_t dev, uint32_t fn, const struct pci_msix *m, uint16_t vec, uint64_t addr, uint16_t data) {
    if (!m || m->cap_off < 0x40) return -1;
    if (vec >= m->num_vecs) return -1;
    // Resolve table physical address
    uint64_t bar_phys, bar_size;
    bool is_mem;
    if (pci_bar_info(bus, dev, fn, m->bir, &bar_phys, &bar_size, &is_mem) != 0)
        return -1;
    if (!is_mem) return -1;
    uint64_t tbl_base = bar_phys + m->table_off;
    uint64_t entry = tbl_base + (uint64_t)vec * 16;
    if (entry + 16 > bar_phys + bar_size) return -1;
    // Write MSI-X table entry: {u64 addr_lohi, u16 data, u16 ctrl}
    // Identity-mapped: physical == virtual in this kernel
    volatile uint32_t *p = (volatile uint32_t *)(uintptr_t)entry;
    p[0] = (uint32_t)addr;
    p[1] = (uint32_t)(addr >> 32);
    p[2] = (uint32_t)data;
    p[3] = 0; // unmasked
    return 0;
}
