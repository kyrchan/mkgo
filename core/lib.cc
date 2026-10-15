/* Freestanding libc bits the compiler may call behind our back. */
#include <stdint.h>
#include <stddef.h>
#include "plat.h"

extern "C" {

void *memset(void *d, int c, size_t n) {
    uint8_t *p = (uint8_t *)d;
    while (n--)
        *p++ = (uint8_t)c;
    return d;
}

void *memcpy(void *d, const void *s, size_t n) {
    uint8_t *pd = (uint8_t *)d;
    const uint8_t *ps = (const uint8_t *)s;
    while (n--)
        *pd++ = *ps++;
    return d;
}

void *memmove(void *d, const void *s, size_t n) {
    uint8_t *pd = (uint8_t *)d;
    const uint8_t *ps = (const uint8_t *)s;
    if (pd < ps) {
        while (n--)
            *pd++ = *ps++;
    } else {
        pd += n;
        ps += n;
        while (n--)
            *--pd = *--ps;
    }
    return d;
}

int memcmp(const void *a, const void *b, size_t n) {
    const uint8_t *pa = (const uint8_t *)a, *pb = (const uint8_t *)b;
    while (n--) {
        if (*pa != *pb)
            return *pa - *pb;
        pa++;
        pb++;
    }
    return 0;
}

static uint64_t g_tsc_khz = 2000000; /* plausible default pre-calibration */

void wasi_calibrate_clock(uint64_t tsc_khz) {
    if (tsc_khz)
        g_tsc_khz = tsc_khz;
}

uint64_t wasi_now_ns(void) {
    /* TSC ticks -> ns. F59: divide first (with remainder correction) so
     * cycles*1e6 can never overflow u64 on large cycle accumulators. */
    uint64_t khz = g_tsc_khz;
    if (!khz)
        return 0;
    uint64_t c = cpu_cycles();
    return (c / khz) * 1000000ULL + ((c % khz) * 1000000ULL) / khz;
}

} /* extern "C" */
