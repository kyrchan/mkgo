#ifndef BOOT_H
#define BOOT_H
#include <stdint.h>
#include "mm.h"

/* Shared with gokernel/main.go (runtime/os_baremetal.go bootInfo). */
struct boot_info {
    uint64_t magic;
    uint64_t serial_ok;
    uint64_t mmap_desc;
    uint64_t mmap_count;
    uint64_t mmap_dsize;
    uint64_t prog;
    uint64_t prog_len;
    uint64_t free_base;
    uint64_t free_end;
    uint64_t tsc_khz;
};

void kmain(const struct boot_info *bi);   /* legacy C path (dormant) */

#endif
