#include "mm.h"
#include "plat.h"

/* Bump allocator over the largest conventional region below 4 GiB. */
static uint64_t pool_base, pool_ptr, pool_end;
static uint64_t ram_top;

static int usable_type(uint32_t t) {
    return t == 7 /*Conventional*/ || t == 4 /*BS data*/ ||
           t == 3 /*BS code*/ || t == 2 /*Loader data*/ || t == 1 /*Loader code*/ ||
           t == 9 /*ACPI reclaim*/ || t == 10 /*ACPI NVS*/;
}

void mm_init(const struct boot_mmap *m) {
    const uint8_t *d = (const uint8_t *)m->desc;
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
    console_puts("[mm] pool ");
    console_hex64(pool_base);
    console_puts("-");
    console_hex64(pool_end);
    console_puts("\n");
}

uint64_t mm_top(void) { return pool_end; }

uint64_t mm_ram_top(void) { return ram_top; }

void *mm_alloc(uint64_t n, uint64_t align) {
    if (!align)
        align = 16;
    uint64_t p = (pool_ptr + align - 1) & ~(align - 1);
    if (p + n > pool_end)
        return 0;
    pool_ptr = p + n;
    return (void *)p;
}
