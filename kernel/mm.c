#include "mm.h"
#include "cpu.h"
#include "serial.h"

/* Bump allocator over the largest conventional region below 4 GiB. */
static uint64_t pool_base, pool_ptr, pool_end;
static uint64_t ram_top;

static int usable_type(uint32_t t) {
    return t == 7 /*Conventional*/ || t == 4 /*BS data*/ ||
           t == 3 /*BS code*/ || t == 2 /*Loader data*/ || t == 1 /*Loader code*/ ||
           t == 9 /*ACPI reclaim*/ || t == 10 /*ACPI NVS*/;
}

void mm_init(const struct boot_mmap *m) {
    const uint8_t *d = m->desc;
    for (uint64_t i = 0; i < m->count; i++, d += m->dsize) {
        uint32_t type;
        uint64_t phys, npages;
        __builtin_memcpy(&type, d, 4);
        __builtin_memcpy(&phys, d + 8, 8);
        __builtin_memcpy(&npages, d + 24, 8);
        if (!usable_type(type))
            continue;
        uint64_t end = phys + npages * 4096ULL;
        if (end > (1ULL << 32))
            end = 1ULL << 32;
        if (phys >= end)
            continue;
        if (end > ram_top)
            ram_top = end;
        if (end - phys > pool_end - pool_base) {
            pool_base = phys;
            pool_end = end;
        }
    }
    /* keep clear of anything the loader/firmware still marks below 1 MiB */
    if (pool_base < (1 << 20))
        pool_base = 1 << 20;
    pool_base = (pool_base + 0xFFF) & ~0xFFFULL;
    pool_ptr = pool_base;
    serial_puts("[mm] pool ");
    serial_hex64(pool_base);
    serial_puts("-");
    serial_hex64(pool_end);
    serial_puts("\n");
}

uint64_t mm_top(void) { return pool_end; }

uint64_t mm_ram_top(void) { return ram_top; }

void mm_pool(uint64_t *base, uint64_t *end) {
    *base = pool_ptr;   /* next free byte */
    *end = pool_end;
}

void *mm_alloc(uint64_t n, uint64_t align) {
    if (!align)
        align = 16;
    uint64_t p = (pool_ptr + align - 1) & ~(align - 1);
    if (p + n > pool_end)
        return 0;
    pool_ptr = p + n;
    return (void *)p;
}

/* ---- identity paging with 2 MiB pages: PML4[0] -> PDPT -> PDs ---- */

static uint64_t *mk_table(void) {
    uint64_t *t = mm_alloc(4096, 4096);
    for (int i = 0; i < 512; i++)
        t[i] = 0;
    return t;
}

void paging_identity_init(void) {
    uint64_t top = (mm_ram_top() + 0x1FFFFF) & ~0x1FFFFFULL;
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
    serial_puts("[mm] identity map to ");
    serial_hex64(top);
    serial_puts(" cr3=");
    serial_hex64((uint64_t)(uintptr_t)pml4);
    serial_puts("\n");
}
