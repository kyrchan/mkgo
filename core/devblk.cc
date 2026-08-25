/* Block window backend: RAM disk (Phase 5). Phase 8 re-backs this same
 * window with virtio-blk; guests cannot tell the difference (ABI §8). */
#include "devblk.h"
#include "plat.h"
#include "rt.h"
#include "mm.h"
#include "sched.h"
#include <stdint.h>

/* window field offsets (abi/ABI.md §3) */
#define W_MAGIC 0
#define W_BLKSZ 4
#define W_NBLK 8
#define W_REQID 16
#define W_OP 24
#define W_LBA 32
#define W_CNT 40
#define W_OFF 48
#define W_DONE 56
#define W_STATUS 60

static const uint32_t BLKW_MAGIC = 0x574B4C42; /* 'BLKW' LE */

static struct {
    bool used;
    int pending_sid;
    uint32_t sid;
    void *engine_rt; /* IM3Runtime for m3_GetMemory */
    uint64_t processed_req;
    uint8_t *disk;
    uint64_t nblocks;
} dev;

extern "C" {
#include "wasm3.h"
M3Result ResizeMemory(IM3Runtime io_runtime, unsigned i_numPages);
}

void devblk_init(void) {
    dev.used = false;
    dev.disk = 0;
    dev.pending_sid = -1;
}

int devblk_attach(void) {
    if (dev.used)
        return 0; /* idempotent */
    if (!dev.disk) {
        dev.nblocks = 16 * 1024 * 1024ULL / BLK_SECT; /* 16 MiB RAM disk */
        dev.disk = (uint8_t *)mm_alloc(dev.nblocks * BLK_SECT, 4096);
        if (!dev.disk) {
            console_puts("[devblk] ramdisk alloc failed\n");
            return -1;
        }
        for (uint64_t i = 0; i < dev.nblocks * BLK_SECT; i++)
            dev.disk[i] = 0;
    }
    dev.used = true;
    console_puts("[devblk] ramdisk ready blocks=");
    console_hex64(dev.nblocks);
    console_puts("\n");
    return 0;
}


extern "C" int virtio_blk_available(void);

int devblk_rw(uint32_t sid, int write, uint64_t lba, void *buf,
              uint32_t count_sectors) {
    /* F31: raw block access is a capability, not an implicit right. The
     * whole-disk R/W surface would otherwise bypass fs.wasm's uid rooting
     * entirely. CAP_FSADM (bit 4) gates it; fs.wasm is spawned holding it
     * (services/init spawn table). Documented in abi/ABI.md §7. */
    if (!(sched_capmask_of(sid) & SCHED_CAP_FSADM)) {
        console_puts("[audit] sid=");
        console_hex64(sid);
        console_puts(" op=blk reason=cap target=devblk\n");
        return -1;
    }
    /* F12: bounds are checked with wraparound-safe math BEFORE any address
     * arithmetic; applies to both backends (virtio and RAM disk). */
    if (count_sectors == 0 || count_sectors > 128)
        return -1;
    uint64_t end_lba;
    if (__builtin_add_overflow(lba, (uint64_t)count_sectors, &end_lba))
        return -1;
    /* virtio-blk backend: real persistent storage */
    if (virtio_blk_available())
        return virtio_blk_rw(write, lba, buf, count_sectors);
    /* RAM disk fallback */
    if (!dev.disk || end_lba > dev.nblocks)
        return -1;
    uint8_t *b = (uint8_t *)buf;
    if (write) {
        for (uint64_t i = 0; i < (uint64_t)count_sectors * BLK_SECT; i++)
            dev.disk[lba * BLK_SECT + i] = b[i];
    } else {
        for (uint64_t i = 0; i < (uint64_t)count_sectors * BLK_SECT; i++)
            b[i] = dev.disk[lba * BLK_SECT + i];
    }
    return 0;
}

void devblk_poll(void) {
    /* block class now served synchronously via devblk_rw (ABI v1.1) */
}
