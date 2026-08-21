#ifndef MM_H
#define MM_H
#include <stdint.h>

struct boot_mmap {
    void *desc;
    uint64_t count;
    uint64_t dsize;
};

void mm_init(const struct boot_mmap *m);
uint64_t mm_top(void);
uint64_t mm_ram_top(void);
void *mm_alloc(uint64_t n, uint64_t align);
void paging_identity_init(void);
void mm_pool(uint64_t *base, uint64_t *end);

#endif
