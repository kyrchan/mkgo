/* Vector accelerator: the four guest vector ops lower 1:1 onto real AVX2
 * instructions executed natively by the host CPU.
 * Compiled with -mavx2; only safe to call after XCR0/SSE/AVX is enabled. */
#include <stdint.h>

typedef unsigned long long u64x4 __attribute__((vector_size(32)));
typedef unsigned int u32x8 __attribute__((vector_size(32)));
typedef unsigned short u16x16 __attribute__((vector_size(32)));
typedef unsigned char u8x32 __attribute__((vector_size(32)));

void vec_load(void *p, void *dst) {
    __asm__ volatile("vmovdqu (%1), %0" : "=x"(*(u64x4 *)dst) : "r"(p) : "memory");
}

void vec_store(void *p, const void *src) {
    __asm__ volatile("vmovdqu %1, (%0)" :: "r"(p), "x"(*(const u64x4 *)src)
                     : "memory");
}

void vec_bcast(uint64_t val, int szcls, void *dst) {
    switch (szcls & 3) {
    case 0: {
        uint8_t t = (uint8_t)val;
        __asm__("vpbroadcastb %1, %0" : "=x"(*(u8x32 *)dst) : "m"(t));
        break;
    }
    case 1: {
        uint16_t t = (uint16_t)val;
        __asm__("vpbroadcastw %1, %0" : "=x"(*(u16x16 *)dst) : "m"(t));
        break;
    }
    case 2: {
        uint32_t t = (uint32_t)val;
        __asm__("vpbroadcastd %1, %0" : "=x"(*(u32x8 *)dst) : "m"(t));
        break;
    }
    default: {
        uint64_t t = val;
        __asm__("vpbroadcastq %1, %0" : "=x"(*(u64x4 *)dst) : "m"(t));
        break;
    }
    }
}

void vec_sub(const void *a, const void *b, int szcls, void *dst) {
    switch (szcls & 3) {
    case 0:
        __asm__("vpsubb %2, %1, %0"
                : "=x"(*(u8x32 *)dst)
                : "x"(*(const u8x32 *)a), "x"(*(const u8x32 *)b));
        break;
    case 1:
        __asm__("vpsubw %2, %1, %0"
                : "=x"(*(u16x16 *)dst)
                : "x"(*(const u16x16 *)a), "x"(*(const u16x16 *)b));
        break;
    case 2:
        __asm__("vpsubd %2, %1, %0"
                : "=x"(*(u32x8 *)dst)
                : "x"(*(const u32x8 *)a), "x"(*(const u32x8 *)b));
        break;
    default:
        __asm__("vpsubq %2, %1, %0"
                : "=x"(*(u64x4 *)dst)
                : "x"(*(const u64x4 *)a), "x"(*(const u64x4 *)b));
        break;
    }
}

int vec_cmpeq_all(const void *a, const void *b, int szcls, void *dst) {
    u64x4 m;
    switch (szcls & 3) {
    case 0:
        __asm__("vpcmpeqb %2, %1, %0"
                : "=x"(*(u8x32 *)dst)
                : "x"(*(const u8x32 *)a), "x"(*(const u8x32 *)b));
        break;
    case 1:
        __asm__("vpcmpeqw %2, %1, %0"
                : "=x"(*(u16x16 *)dst)
                : "x"(*(const u16x16 *)a), "x"(*(const u16x16 *)b));
        break;
    case 2:
        __asm__("vpcmpeqd %2, %1, %0"
                : "=x"(*(u32x8 *)dst)
                : "x"(*(const u32x8 *)a), "x"(*(const u32x8 *)b));
        break;
    default:
        __asm__("vpcmpeqq %2, %1, %0"
                : "=x"(*(u64x4 *)dst)
                : "x"(*(const u64x4 *)a), "x"(*(const u64x4 *)b));
        break;
    }
    const u64x4 *mask = (const u64x4 *)dst;
    /* ZF := all lanes matched (mask is all ones) */
    for (int i = 0; i < 4; i++)
        if ((*mask)[i] != ~0ULL)
            return 0;
    return 1;
}
