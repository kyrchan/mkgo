#ifndef DEVBLK_H
#define DEVBLK_H
#include <stdint.h>

/* Block window backend (abi/ABI.md §3): kernel-side RAM disk paired with
 * a window inside ONE session's linear memory (fs.wasm). Single
 * outstanding request, polled between scheduler quanta. */

/* Window lives ABOVE the managed runtime's requested pages: the kernel
 * grows guest memory to BLK_WIN_PAGES and reserves the tail. Linear
 * offsets are stable across wasm3 resizes (host pointer moves, guest
 * view does not). */
#define BLK_WIN_OFF 0x700000ULL
#define BLK_MIN_PAGES 128ULL /* 8 MiB */
#define BLK_WIN_DATA 512ULL     /* staging area offset within window */
/* NOTE: window sits inside the module's DECLARED memory (Go wasip1 emits
 * ~2.1 MiB min). Managed-runtime overlap risk accepted for v1 single
 * service with bounded heap; revisit with per-class reserved regions. */
#define BLK_SECT 512ULL

#ifdef __cplusplus
extern "C" {
#endif

void devblk_init(void);
int  devblk_attach(uint32_t sid); /* give sid a block window; 0 ok */
void devblk_poll(void);           /* service pending requests (kernel ctx) */
int  devblk_rw(uint32_t sid, int write, uint64_t lba, void *buf,
               uint32_t count_sectors); /* managed-runtime transport */

#ifdef __cplusplus
}
#endif

#endif
