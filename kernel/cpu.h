#ifndef CPU_H
#define CPU_H
#include <stdint.h>

struct cpuid { uint32_t a, b, c, d; };

static inline struct cpuid cpuid(uint32_t leaf, uint32_t subleaf) {
    struct cpuid r;
    __asm__ volatile("cpuid"
                     : "=a"(r.a), "=b"(r.b), "=c"(r.c), "=d"(r.d)
                     : "a"(leaf), "c"(subleaf));
    return r;
}

static inline uint64_t rd_cr0(void) {
    uint64_t v;
    __asm__ volatile("mov %%cr0, %0" : "=r"(v));
    return v;
}
static inline void wr_cr0(uint64_t v) { __asm__ volatile("mov %0, %%cr0" :: "r"(v)); }
static inline uint64_t rd_cr3(void) {
    uint64_t v;
    __asm__ volatile("mov %%cr3, %0" : "=r"(v));
    return v;
}
static inline void wr_cr3(uint64_t v) { __asm__ volatile("mov %0, %%cr3" :: "r"(v) : "memory"); }
static inline uint64_t rd_cr4(void) {
    uint64_t v;
    __asm__ volatile("mov %%cr4, %0" : "=r"(v));
    return v;
}
static inline void wr_cr4(uint64_t v) { __asm__ volatile("mov %0, %%cr4" :: "r"(v)); }

static inline uint64_t rd_xcr0(uint32_t reg) {
    uint32_t lo, hi;
    __asm__ volatile("xgetbv" : "=a"(lo), "=d"(hi) : "c"(reg));
    return lo | ((uint64_t)hi << 32);
}
static inline void wr_xcr0(uint32_t reg, uint64_t val) {
    __asm__ volatile("xsetbv" :: "a"((uint32_t)val), "d"((uint32_t)(val >> 32)), "c"(reg));
}

static inline void hlt(void) { __asm__ volatile("hlt"); }
static inline void cli(void) { __asm__ volatile("cli"); }

void cpu_dump_features(void);
int cpu_enable_avx2(void);

#endif
