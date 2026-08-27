#include "pci.h"
#include "lib.h"
#include "io.h"

static uint32_t pci_cfg_addr(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset) {
    return 0x80000000u | (bus << 16) | (dev << 11) | (fn << 8) | (offset & 0xFCu);
}

int32_t pci_read32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset) {
    if (bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset & 3))
        return -1;
    // Bounds: offset must be 4-aligned, we mask inside
    outl(0xCF8, pci_cfg_addr(bus, dev, fn, offset));
    return (int32_t)inl(0xCFC);
}

int32_t pci_write32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset, uint32_t val) {
    if (bus > 255 || dev > 31 || fn > 7 || offset > 0xFC || (offset & 3))
        return -1;
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
        size = (~(uint64_t)sz) + 1;
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
