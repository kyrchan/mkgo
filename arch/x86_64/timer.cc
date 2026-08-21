/* Timer shim: calibrate TSC against PIT channel 2 once at boot so the
 * WASI clock shim can convert cycles -> nanoseconds. */
#include "plat.h"
#include "io.h"
#include "cpu.h"
#include <stdint.h>

/* Rough TSC frequency via PIT channel 2 (~1.193 MHz reference). */
uint64_t timer_calibrate_tsc_khz(void) {
    outb(0x61, (inb(0x61) & ~0x02) | 0x01);
    outb(0x43, 0xB0);
    outb(0x42, 1193 & 0xFF);
    outb(0x42, 1193 >> 8);
    uint64_t t0 = rdtsc();
    uint16_t last = 0, wraps = 0;
    while (wraps < 3) {
        uint16_t cnt = (uint16_t)inb(0x42);
        cnt |= (uint16_t)inb(0x42) << 8;
        if (cnt > last)
            wraps++;
        last = cnt;
    }
    uint64_t t1 = rdtsc();
    outb(0x61, inb(0x61) & ~0x03);

    uint64_t elapsed_us = (uint64_t)wraps * 65536ULL * 1000000ULL / 1193181ULL;
    if (!elapsed_us)
        return 0;
    uint64_t khz = (t1 - t0) / elapsed_us;
    if (khz < 1000 || khz > 10000000)
        return 0; /* implausible (e.g. TCG virtual tsc): caller keeps default */
    return khz;
}
