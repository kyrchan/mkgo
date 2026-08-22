#include <cstdio>
#include <cstdlib>
#include <cstdint>
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

    extern int sched_spawn_named(const char *, const uint8_t *, uint64_t,
                                 uint32_t, uint64_t);
    extern void ports_init(void);
    extern void sched_run(void);
    extern void sched_init(void);
    ports_init();
    sched_init();
    fprintf(stderr, "[stage] sched_init done\n");
    int rc = sched_spawn(blob, len, 0, 0);
    fprintf(stderr, "[stage] spawned rc=%d\n", rc);
    return rc;
}
