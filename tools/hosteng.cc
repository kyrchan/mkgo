/* Host harness: runs a .wasm through the REAL engine + WASI glue + rt
 * (same objects the kernel links) so guest issues surface in seconds,
 * not QEMU-round-trips. */
#include <cstdio>
#include <cstdlib>
#include <cstdint>
#include <ctime>

void console_init(void) {}
void console_putc(char c) { fputc(c, stdout); }
void console_puts(const char *s) { fputs(s, stdout); }
void console_hex64(uint64_t v) { printf("0x%llx", (unsigned long long)v); }
uint64_t cpu_cycles(void) { return (uint64_t)clock(); }
void cpu_halt(void) { exit(0); }
uint64_t timer_calibrate_tsc_khz(void) { return 1000000; }

#include "mm.h"
void *mm_alloc(uint64_t n, uint64_t align) {
    if (!align || align < 16)
        align = 16;
    return aligned_alloc(align, ((n + align - 1) & ~(align - 1)));
}

uint32_t timer_calibrate_tsc_khz_public();

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: hosteng app.wasm\n");
        return 2;
    }
    FILE *f = fopen(argv[1], "rb");
    if (!f) {
        perror("open");
        return 2;
    }
    fseek(f, 0, SEEK_END);
    long len = ftell(f);
    fseek(f, 0, SEEK_SET);
    uint8_t *blob = (uint8_t *)malloc(len);
    if (fread(blob, 1, len, f) != (size_t)len)
        return 2;
    fclose(f);

    fprintf(stderr, "[stage] read %ld bytes\n", len);
    extern void wasi_calibrate_clock(uint64_t);
    wasi_calibrate_clock(timer_calibrate_tsc_khz());
    fprintf(stderr, "[stage] clock calibrated\n");

    extern int sched_spawn(const uint8_t *, uint64_t, const char *const *,
                           int);
    extern void sched_init(void);
    sched_init();
    fprintf(stderr, "[stage] sched_init done\n");
    int rc = sched_spawn(blob, len, 0, 0);
    fprintf(stderr, "[stage] spawned rc=%d\n", rc);
    return rc;
}
