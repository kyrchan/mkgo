#ifndef BOOT_H
#define BOOT_H
#include <stdint.h>

/* Produced by core/main.cc before ExitBootServices hand-off; consumed by
 * kmain(). Go-heap relics (free_base/free_end/tsc_khz) retired in Phase 2. */
struct boot_info {
    uint64_t magic;
    uint64_t mmap_desc;
    uint64_t mmap_count;
    uint64_t mmap_dsize;
    uint64_t prog;
    uint64_t prog_len;
    /* preloaded boot service modules (ESP -> EfiLoaderData, pre-EBS) */
    uint64_t mod_console;
    uint64_t mod_console_len;
    uint64_t mod_login;
    uint64_t mod_login_len;
};

void kmain(const struct boot_info *bi);

#endif
