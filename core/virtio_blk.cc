/* Virtio-blk native shim -- MODERN (virtio 1.x) transport via
 * core/virtio_modern.cc. Re-backs kern_blk_* / devblk_rw with real
 * storage; guests cannot tell which backend serves them (ABI §8).
 * F45 semantics preserved: persistent consumed index, device status
 * byte checked, spec split-ring layout with explicit queue addresses. */
#include "plat.h"
#include "sched.h"
#include "virtio_modern.h"
#include <stdint.h>

#define QNUM 256
#define DESC_TBL (QNUM * 16)
#define AVAIL_SZ (6 + QNUM * 2)
#define USED_OFF ((DESC_TBL + AVAIL_SZ + 4095u) & ~4095u)

static uint8_t vring[USED_OFF + 4u + 8u * QNUM] __attribute__((aligned(4096)));
static uint16_t avail_idx;
static uint16_t last_used;

static uint8_t sector_data[8 * 512] __attribute__((aligned(16)));
static uint8_t status_byte = 99;

struct vblk_req_hdr {
    uint32_t type;
    uint32_t reserved;
    uint64_t sector;
} __attribute__((packed));
static vblk_req_hdr hdr;

static vmod_dev dev;
static uint16_t g_qoff;
static int g_ready;
static uint64_t g_sectors;

static void desc_put(uint16_t idx, uint64_t pa, uint32_t len, uint16_t flags,
                     uint16_t next) {
    volatile uint64_t *a = (volatile uint64_t *)(vring + idx * 16);
    volatile uint64_t *b = (volatile uint64_t *)(vring + idx * 16 + 8);
    *a = pa;
    *b = (uint64_t)len | ((uint64_t)flags << 32) | ((uint64_t)next << 48);
}

void virtio_blk_init(void) {
    g_ready = 0;
    if (vmod_probe(0x1001, &dev) != 0) {
        console_puts("[virtio-blk] no device\n");
        return;
    }
    /* VERSION_1 is mandatory for modern; nothing else needed for IO */
    vmod_features(&dev, 1ull << 32);
    vmod_status_add(&dev, 8); /* FEATURES_OK */
    if (!(dev.common[0x14] & 8)) {
        console_puts("[virtio-blk] FEATURES_OK refused\n");
        return;
    }
    g_sectors = vmod_cfg_u64(&dev, 0); /* capacity at cfg+0 */

    uint16_t qn = vmod_queue_size(&dev, 0);
    if (qn == 0) {
        console_puts("[virtio-blk] queue dead\n");
        return;
    }
    if (qn > QNUM)
        qn = QNUM;
    uint64_t base = (uint64_t)(uintptr_t)vring;
    if (vmod_queue_setup(&dev, 0, qn, base, base + DESC_TBL,
                         base + USED_OFF) < 0) {
        console_puts("[virtio-blk] qsetup failed\n");
        return;
    }
    g_qoff = vmod_queue_notify_off(&dev, 0);
    vmod_driver_ok(&dev);
    last_used = 0;
    avail_idx = 0;
    g_ready = 1;
    console_puts("[virtio-blk] modern ready sectors=");
    console_hex64(g_sectors);
    console_puts("\n");
}

int virtio_blk_available(void) { return g_ready; }

int virtio_blk_rw(int write, uint64_t lba, void *buf, uint32_t count) {
    if (!g_ready)
        return -1;
    uint64_t end_lba;
    if (count == 0 || count > 8 ||
        __builtin_add_overflow(lba, (uint64_t)count, &end_lba) ||
        (g_sectors && end_lba > g_sectors))
        return -1;

    for (uint32_t s = 0; s < count; s++) {
        hdr.type = write ? 1 : 0;
        hdr.reserved = 0;
        hdr.sector = lba + s;
        status_byte = 99;
        if (write)
            for (uint32_t i = 0; i < 512; i++)
                sector_data[i] = ((uint8_t *)buf)[s * 512 + i];

        desc_put(0, (uintptr_t)&hdr, 16, 1, 1);
        desc_put(1, (uintptr_t)sector_data, 512, write ? 0 : 2, 2);
        desc_put(2, (uintptr_t)&status_byte, 1, 2, 0);

        volatile uint16_t *aidx = (volatile uint16_t *)(vring + DESC_TBL + 2);
        volatile uint16_t *aring = (volatile uint16_t *)(vring + DESC_TBL + 4);
        aring[avail_idx % QNUM] = 0;
        avail_idx++;
        *aidx = avail_idx;
        vmod_notify(&dev, 0, g_qoff);

        volatile uint16_t *uidx = (volatile uint16_t *)(vring + USED_OFF + 2);
        while (*uidx == last_used) {
            /* polled completion (cooperative context) */
        }
        last_used = *uidx;
        if (status_byte != 0)
            return -1;
        if (!write)
            for (uint32_t i = 0; i < 512; i++)
                ((uint8_t *)buf)[s * 512 + i] = sector_data[i];
    }
    return 0;
}
