/* Identity paging with 2 MiB pages: PML4[0] -> PDPT -> PDs.
 * Flat/identity everywhere is a binding design rule (no per-arch page
 * table management beyond this bring-up map). */
#include "cpu.h"
#include "plat.h"
#include "mm.h"
#include <stdint.h>

static uint64_t *mk_table(void) {
    uint64_t *t = (uint64_t *)mm_alloc(4096, 4096);
    for (int i = 0; i < 512; i++)
        t[i] = 0;
    return t;
}

void paging_identity_init(void) {
    uint64_t top = 1ULL << 32; /* full 4GB identity map */
    if (top > (1ULL << 32))
        top = 1ULL << 32;
    uint64_t *pml4 = mk_table();
    uint64_t *pdpt = mk_table();
    pml4[0] = ((uint64_t)(uintptr_t)pdpt) | 3;

    for (uint64_t addr = 0; addr < top; addr += (1ULL << 30)) {
        uint64_t *pd = mk_table();
        pdpt[(addr >> 30) & 511] = ((uint64_t)(uintptr_t)pd) | 3;
        for (int i = 0; i < 512 && addr + i * (1 << 21) < top; i++) {
            uint64_t pa = addr + (uint64_t)i * (1 << 21);
            pd[i] = pa | 0x83; /* present | write | 2MiB page */
        }
    }
    wr_cr3((uint64_t)(uintptr_t)pml4);
    console_puts("[mm] identity map to ");
    console_hex64(top);
    console_puts(" cr3=");
    console_hex64((uint64_t)(uintptr_t)pml4);
    console_puts("\n");
}
