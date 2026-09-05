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
    /* Identity-map RAM (<4 GiB, 2 MiB pages) plus a SPARSE high window
     * (1 GiB pages) covering the q35 64-bit PCI MMIO BAR area (virtio
     * modern regs live at e.g. 0xC_0000_0000 = 48 GiB). High mappings
     * cost one PDPT entry per GiB -- no PD allocation up there.
     * F81: MMIO ranges are mapped uncacheable (PCD=1) to prevent
     * CPU cache reordering of device-register accesses on bare metal.
     * RAM below 4 GiB stays cacheable (0x83 = P|RW|PS=0|A). */
    uint64_t top = 1ULL << 42; /* 4 TiB */
    uint64_t *pml4 = mk_table();
    g_pml4 = pml4;

    for (uint64_t region = 0; region < top; region += (1ULL << 39)) {
        uint64_t *pdpt = mk_table();
        pml4[(region >> 39) & 511] = ((uint64_t)(uintptr_t)pdpt) | 3;
        for (uint64_t addr = region; addr < region + (1ULL << 39) &&
                                     addr < top;
             addr += (1ULL << 30)) {
            uint32_t gi = (addr >> 30) & 511;
            if (addr < (1ULL << 32)) {
                /* low 4 GiB: 2 MiB pages (RAM, cacheable) */
                uint64_t *pd = mk_table();
                pdpt[gi] = ((uint64_t)(uintptr_t)pd) | 3;
                for (int i = 0; i < 512; i++)
                    pd[i] = (addr + (uint64_t)i * (1 << 21)) | 0x83;
            } else {
                /* high window: 1 GiB leaf pages (MMIO BARs), uncacheable */
                pdpt[gi] = addr | 0x8F; /* P|RW|US=0|PS(1G)|A|PCD */
            }
        }
    }
    wr_cr3((uint64_t)(uintptr_t)pml4);
    /* VRING-BLOCKER root cause #2b: OVMF leaves CR4.PGE set and its own
     * global TLB entries behind; those survive CR3 reload, so any VA the
     * firmware touched keeps the FIRMWARE-era translation. Clear PGE. */
    {
        uint64_t cr4;
        __asm__ volatile("mov %%cr4, %0" : "=r"(cr4));
        if (cr4 & (1ULL << 7))
            __asm__ volatile("mov %0, %%cr4" :: "r"(cr4 & ~(1ULL << 7)));
    }
    console_puts("[mm] identity map to ");
    console_hex64(top);
    console_puts(" cr3=");
    console_hex64((uint64_t)(uintptr_t)pml4);
    console_puts("\n");
}
