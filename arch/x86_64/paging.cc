/* Identity paging with 2 MiB pages: PML4[0] -> PDPT -> PDs.
 * Flat/identity everywhere is a binding design rule (no per-arch page
 * table management beyond this bring-up map). */
#include "cpu.h"
#include "plat.h"
#include "mm.h"
#include <stdint.h>

/* VRING-BLOCKER root cause #1: page tables MUST NOT live in the general
 * mm pool -- engine/session allocations later overwrite them (observed as
 * "not-present" faults on VAs whose PTEs were verifiably written). This
 * arena is private to paging and never handed out again. Sized for
 * top=64GiB: 1 pml4 + 1..8 pdpt + up to 128 pd == ~130 pages. */
static uint8_t pt_arena[160 * 4096] __attribute__((aligned(4096)));
static uint32_t pt_used;

static uint64_t *mk_table(void) {
    if (pt_used + 4096 > sizeof(pt_arena))
        return 0;
    uint64_t *t = (uint64_t *)(pt_arena + pt_used);
    pt_used += 4096;
    for (int i = 0; i < 512; i++)
        t[i] = 0;
    return t;
}

static uint64_t *g_pml4;
uint64_t paging_pml4_pa(void) { return (uint64_t)(uintptr_t)g_pml4; }

void paging_identity_init(void) {
    /* Identity-map the first 512 GiB: covers conventional RAM (<4GiB)
     * AND the 64-bit MMIO BARs (virtio modern common/notify/isr/device
     * regions live at e.g. 0xc_0000_0000 under q35). Mapping MMIO here
     * is safe: pages fault only on access, and we access exactly the
     * device bars. */
    /* 64 GiB identity: RAM below 4 GiB plus q35 64-bit MMIO BAR window */
    uint64_t top = 1ULL << 36;
    uint64_t *pml4 = mk_table();
    g_pml4 = pml4;

    /* one PDPT per PML4 slot (each covers 512 GiB); identity throughout */
    for (uint64_t region = 0; region < top; region += (1ULL << 39)) {
        uint64_t *pdpt = mk_table();
        pml4[(region >> 39) & 511] = ((uint64_t)(uintptr_t)pdpt) | 3;
        for (uint64_t addr = region; addr < region + (1ULL << 39) &&
                                     addr < top;
             addr += (1ULL << 30)) {
            uint64_t *pd = mk_table();
            pdpt[(addr >> 30) & 511] = ((uint64_t)(uintptr_t)pd) | 3;
            for (int i = 0; i < 512 && addr + (uint64_t)i * (1 << 21) < top;
                 i++) {
                uint64_t pa = addr + (uint64_t)i * (1 << 21);
                pd[i] = pa | 0x83; /* present | write | 2MiB page */
            }
        }
    }
    wr_cr3((uint64_t)(uintptr_t)pml4);
    /* VRING-BLOCKER root cause #2: OVMF leaves CR4.PGE set and its own
     * global TLB entries behind; those survive our CR3 reload, so any VA
     * the firmware touched (e.g. 64-bit device BARs it sized) keeps the
     * FIRMWARE-era translation -- typically not-present for us. Clearing
     * PGE flushes every global entry. */
    {
        uint64_t cr4;
        __asm__ volatile("mov %%cr4, %0" : "=r"(cr4));
        if (cr4 & (1ULL << 7)) { /* PGE */
            __asm__ volatile("mov %0, %%cr4" :: "r"(cr4 & ~(1ULL << 7)));
        }
    }
    console_puts("[mm] identity map to ");
    console_hex64(top);
    console_puts(" cr3=");
    console_hex64((uint64_t)(uintptr_t)pml4);
    console_puts("\n");
}
