#include "perf.h"

/* HP0 counter store: bump-free static array (never touches mm/rt,
 * safe to call from any context including IRQ-adjacent paths). */
static uint64_t g_c[PERF_N];

void perf_inc(int id, uint64_t n) {
    if ((unsigned)id < (unsigned)PERF_N)
        __atomic_fetch_add(&g_c[id], n, __ATOMIC_RELAXED);
}

uint64_t perf_get(int id) {
    uint64_t v = 0;
    if ((unsigned)id < (unsigned)PERF_N)
        __atomic_load(&g_c[id], &v, __ATOMIC_RELAXED);
    return v;
}
