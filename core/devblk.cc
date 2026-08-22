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

int devblk_attach(uint32_t sid) {
    if (dev.used)
        return -1;
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
    dev.sid = sid;
    console_puts("[devblk] ramdisk ready sid=");
    console_hex64(sid);
    console_puts(" blocks=");
    console_hex64(dev.nblocks);
    console_puts("\n");
    return 0;
}

int devblk_rw(uint32_t sid, int write, uint64_t lba, void *buf,
              uint32_t count_sectors) {
    (void)sid;
    if (!dev.disk || count_sectors == 0 || count_sectors > 128 ||
        lba + count_sectors > dev.nblocks)
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
