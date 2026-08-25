/* Virtio MODERN (virtio 1.x) transport helper -- shared by the block and
 * net shims. Discovers the vendor-capability BARs (common config, notify,
 * ISR, device config) from the PCI capability list, drives the required
 * feature/status handshake (VERSION_1 + FEATURES_OK) and sets queues up
 * through the common configuration structure.
 *
 * Split-vring memory layout is identical to legacy; only ENABLING differs
 * (explicit desc/avail/used addresses + enable bit instead of one PFN).
 * All register access is 8/16/32-bit little-endian MMIO via volatile. */
#include "io.h"
#include "plat.h"
#include <stdint.h>

namespace {

constexpr uint16_t PCI_VENDOR_VIRTIO = 0x1AF4;
constexpr uint16_t PCI_DEVID_MODERN_BASE = 0x1040;

constexpr uint8_t CAP_VENDOR = 9; /* PCI_CAP_ID_VNDR */

#pragma pack(push, 1)
struct virtio_pci_cap {
    uint8_t cap_vndr;
    uint8_t cap_next;
    uint8_t cap_len;
    uint8_t cfg_type;    /* 1 common, 2 notify, 3 ISR, 4 device, 5 pci */
    uint8_t bar;
    uint8_t padding[3];
    uint32_t offset;     /* within BAR */
    uint32_t length;
};
struct virtio_pci_notify_cap {
    virtio_pci_cap cap;
    uint32_t notify_off_multiplier;
};
#pragma pack(pop)

enum cfg_type {
    CFG_COMMON = 1,
    CFG_NOTIFY = 2,
    CFG_ISR = 3,
    CFG_DEVICE = 4,
};

constexpr uint8_t S_ACK = 1, S_DRIVER = 2, S_FEAT_OK = 8, S_DRIVER_OK = 4;

} /* namespace */

extern "C" {

struct vmod_dev {
    volatile uint8_t *common;
    volatile uint8_t *notify;
    volatile uint8_t *isr;
    volatile uint8_t *device;
    uint32_t notify_mult;
    uint16_t queues;
    int ready;
};

static uint32_t pci_read32(uint8_t bus, uint8_t dev, uint8_t fn, uint8_t off) {
    outl(0xCF8, 0x80000000UL | ((uint32_t)bus << 16) | ((uint32_t)dev << 11) |
                    ((uint32_t)fn << 8) | (off & 0xFC));
    return inl(0xCFC);
}
static uint16_t pci_read16(uint8_t bus, uint8_t dev, uint8_t fn, uint8_t off) {
    uint32_t v = pci_read32(bus, dev, fn, off & 0xFC);
    return (uint16_t)(v >> ((off & 2) * 8));
}

/* 64-bit MMIO read avoiding u64 MMIO semantics (two 32-bit reads) */
static inline uint64_t mmio_rd64(volatile uint8_t *p) {
    uint32_t lo = *(volatile uint32_t *)(p);
    uint32_t hi = *(volatile uint32_t *)(p + 4);
    return (uint64_t)lo | ((uint64_t)hi << 32);
}
static inline void mmio_wr64(volatile uint8_t *p, uint64_t v) {
    *(volatile uint32_t *)(p) = (uint32_t)v;
    *(volatile uint32_t *)(p + 4) = (uint32_t)(v >> 32);
}

/* locate an IO/MEM BAR base (firmware-programmed) */
static uint64_t bar_base(uint8_t bus, uint8_t dev, uint8_t fn, uint8_t bar) {
    uint8_t off = 0x10 + bar * 4;
    uint32_t low = pci_read32(bus, dev, fn, off);
    if (low & 1)
        return low & ~3ull; /* I/O BAR */
    /* 64-bit BAR: read high word */
    uint32_t high = pci_read32(bus, dev, fn, off + 4);
    return ((uint64_t)high << 32) | (low & ~0xFull);
}

int vmod_probe(uint16_t want_devid, vmod_dev *out) {
    for (uint8_t d = 0; d < 32; d++) {
        if (pci_read16(0, d, 0, 0x00) == 0xFFFF)
            continue;
        uint16_t vendor = pci_read16(0, d, 0, 0x00);
        uint16_t devid = pci_read16(0, d, 0, 0x02);
        if (vendor != PCI_VENDOR_VIRTIO)
            continue;
        /* modern-only devices use 0x1040+type; transitional keep their
         * legacy ids but still expose modern caps when disable-modern=n */
        console_puts("[vmod] cand vid=");
        console_hex64(vendor);
        console_puts(" did=");
        console_hex64(devid);
        console_puts(" caps@");
        console_hex64(pci_read16(0, d, 0, 0x34) & 0xFF);
        console_puts("\n");
        bool id_ok = (devid == want_devid) ||
                     (devid >= PCI_DEVID_MODERN_BASE &&
                      (devid & 0xFFFC) == PCI_DEVID_MODERN_BASE &&
                      (want_devid == 0x1000 ? (devid & 0xFF) == 0
                                            : (devid & 0xFF) == 1));
        if (!id_ok)
            continue;

        /* walk capability list */
        uint8_t cap_ptr = (uint8_t)(pci_read16(0, d, 0, 0x34) & 0xFF);
        bool have_common = false, have_notify = false, have_isr = false,
             have_dev = false;
        uint64_t cbase = 0, nbase = 0, ibase = 0, dbase = 0;
        uint32_t cmul = 1;
        int guard = 0;
        while (cap_ptr && guard++ < 24) {
            uint32_t hdr32 =
                pci_read32(0, d, 0, (uint8_t)(cap_ptr & 0xFC));
            uint8_t sh = (cap_ptr & 2) ? 16 : 0;
            uint8_t vndr = (uint8_t)(hdr32 >> sh);
            uint8_t next = (uint8_t)(hdr32 >> (sh + 8));
            if (vndr != CAP_VENDOR) {
                cap_ptr = next;
                continue;
            }
            /* cap layout: [0]=vndr [1]=next [2]=len [3]=cfg_type
             *             [4]=bar  [5..7]=pad  [8]=offset u32 */
            uint32_t dw1 =
                pci_read32(0, d, 0, (uint8_t)((cap_ptr + 4) & 0xFC));
            uint8_t cfg_type = (uint8_t)(hdr32 >> (sh + 24));
            uint8_t bar = (uint8_t)(dw1 >> sh);
            uint32_t off_lo = pci_read32(0, d, 0, (uint8_t)(cap_ptr + 8));
            uint64_t base = bar_base(0, d, 0, bar) + off_lo;
            switch (cfg_type) {
            case CFG_COMMON: cbase = base; have_common = true; break;
            case CFG_NOTIFY:
                nbase = base;
                cmul = pci_read32(0, d, 0, (uint8_t)(cap_ptr + 16));
                if (cmul == 0)
                    cmul = 1;
                have_notify = true;
                break;
            case CFG_ISR: ibase = base; have_isr = true; break;
            case CFG_DEVICE: dbase = base; have_dev = true; break;
            }
            cap_ptr = next;
        }
        if (!have_common || !have_notify || !have_isr)
            continue;

        out->common = (volatile uint8_t *)cbase;
        out->notify = (volatile uint8_t *)nbase;
        out->isr = (volatile uint8_t *)ibase;
        out->device = have_dev ? (volatile uint8_t *)dbase : nullptr;
        out->notify_mult = cmul ? cmul : 1;
        out->ready = 0;

        /* reset + ack */
        out->common[0x14] = 0; /* device_status @0x14 = 0 (reset) */
        while (out->common[0x14])
            ;
        out->common[0x14] = S_ACK | S_DRIVER;
        return 0;
    }
    return -1;
}

/* negotiate: accept ONLY the bits in `accept` that the device offers */
/* real common-cfg map (v1.0 §4.1.4.3): feat_select@0 feat@4
 * drv_select@8 drv@c status@14 qsel@16 qsize@18 qenable@1c
 * qnotify_off@1e desc@20 avail@28 used@30 */
uint64_t vmod_features(vmod_dev *d, uint64_t accept) {
    *(volatile uint32_t *)(d->common + 0x00) = 0; /* select lo half */
    uint32_t lo = *(volatile uint32_t *)(d->common + 0x04);
    *(volatile uint32_t *)(d->common + 0x00) = 1;
    uint32_t hi = *(volatile uint32_t *)(d->common + 0x04);
    uint64_t dev = (uint64_t)lo | ((uint64_t)hi << 32);
    uint64_t drv = dev & accept;
    *(volatile uint32_t *)(d->common + 0x08) = 0;
    *(volatile uint32_t *)(d->common + 0x0C) = (uint32_t)drv;
    *(volatile uint32_t *)(d->common + 0x08) = 1;
    *(volatile uint32_t *)(d->common + 0x0C) = (uint32_t)(drv >> 32);
    return drv;
}

void vmod_status_add(vmod_dev *d, uint8_t bits) { d->common[0x14] |= bits; }

void vmod_driver_ok(vmod_dev *d) { vmod_status_add(d, S_DRIVER_OK); }

uint16_t vmod_queue_size(vmod_dev *d, uint16_t qidx) {
    *(volatile uint16_t *)(d->common + 0x16) = qidx;
    return *(volatile uint16_t *)(d->common + 0x18);
}

int vmod_queue_setup(vmod_dev *d, uint16_t qidx, uint16_t size,
                     uint64_t desc_pa, uint64_t avail_pa, uint64_t used_pa) {
    *(volatile uint16_t *)(d->common + 0x16) = qidx;
    uint16_t max = *(volatile uint16_t *)(d->common + 0x18);
    if (max == 0)
        return -1;
    if (size > max)
        size = max;
    *(volatile uint16_t *)(d->common + 0x18) = size;
    *(volatile uint16_t *)(d->common + 0x16) = qidx; /* re-select */
    mmio_wr64(d->common + 0x20, desc_pa);
    mmio_wr64(d->common + 0x28, avail_pa);
    mmio_wr64(d->common + 0x30, used_pa);
    *(volatile uint16_t *)(d->common + 0x1C) = 1; /* queue_enable */
    return (int)size;
}

void vmod_notify(vmod_dev *d, uint16_t qidx, uint16_t queue_notify_off) {
    *(volatile uint16_t *)(d->notify +
                           (uint32_t)queue_notify_off * d->notify_mult) =
        qidx;
}

uint8_t vmod_isr(vmod_dev *d) { return *d->isr; }

/* read a 64-bit field from the DEVICE-specific config region */
uint64_t vmod_cfg_u64(vmod_dev *d, uint32_t off) {
    if (!d->device)
        return 0;
    return mmio_rd64(d->device + off);
}

uint16_t vmod_queue_notify_off(vmod_dev *d, uint16_t qidx) {
    *(volatile uint16_t *)(d->common + 0x16) = qidx;
    return *(volatile uint16_t *)(d->common + 0x1E);
}

} /* extern "C" */
