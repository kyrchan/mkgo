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
    uint64_t mod_fs;
    uint64_t mod_fs_len;
    uint64_t mod_init;
    uint64_t mod_init_len;
    uint64_t mod_shell;
    uint64_t mod_shell_len;
    uint64_t mod_net;
    uint64_t mod_net_len;
    uint64_t mod_p9;
    uint64_t mod_p9_len;
    uint64_t mod_graphics;
    uint64_t mod_graphics_len;
    /* \etc\init.conf preloaded (NUL-terminated via conf_z in main.cc) */
    uint64_t conf;
    uint64_t conf_len;
    /* optional second payload slot (/vm/app2); falls back to prog */
    uint64_t prog2;
    uint64_t prog2_len;
    /* gate mask for legacy payload slots (0 = default KILL) */
    uint64_t gate_mask;
};

void kmain(const struct boot_info *bi);

#endif
