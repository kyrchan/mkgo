#include "log.h"
#include "arch_lock.h"

#define LOG_SIZE 16384u

static uint8_t log_ring[LOG_SIZE];
static uint64_t log_total; /* ever-growing byte count */
static arch_spinlock_t log_lk;
static int log_init_done;

static void log_ensure_init(void) {
    if (!log_init_done) {
        arch_spinlock_init(&log_lk);
        log_init_done = 1;
    }
}

void log_push(const char *p, uint32_t n) {
    if (!p || !n)
        return;
    log_ensure_init();
    arch_spinlock_acquire(&log_lk);
    for (uint32_t i = 0; i < n; i++) {
        log_ring[log_total % LOG_SIZE] = (uint8_t)p[i];
        log_total++;
    }
    arch_spinlock_release(&log_lk);
}

uint32_t log_read(uint64_t off, uint8_t *dst, uint32_t cap,
                  uint64_t *out_total, uint64_t *out_begin) {
    log_ensure_init();
    arch_spinlock_acquire(&log_lk);
    uint64_t total = log_total;
    uint64_t begin = total > LOG_SIZE ? total - LOG_SIZE : 0;
    if (off < begin)
        off = begin;
    uint64_t avail = off < total ? total - off : 0;
    uint32_t n = avail < cap ? (uint32_t)avail : cap;
    for (uint32_t i = 0; i < n; i++)
        dst[i] = log_ring[(off + i) % LOG_SIZE];
    arch_spinlock_release(&log_lk);
    if (out_total)
        *out_total = total;
    if (out_begin)
        *out_begin = off;
    return n;
}
