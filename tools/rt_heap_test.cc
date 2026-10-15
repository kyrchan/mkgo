/* rt_heap_test -- regression tests for the freestanding heap
 * (core/rt.cc) that wasm3 and the WASI glue allocate from.
 *
 * Why this exists: mm_alloc is a pure bump allocator (no free), so
 * EVERY block the heap does not recycle is pool memory lost forever.
 * rt_free used to bin only sizes up to 2^19 (512 KiB) and silently
 * leak anything larger ("oversized: leak (rare in wasm3 patterns)").
 * It was not rare: a Go guest's linear memory grows by doubling
 * (2.4 MiB -> ~35 MiB for shell.wasm), and every intermediate size went
 * through rt_realloc -> rt_free -> leaked. Six services exhausted the
 * 447 MiB pool and the next guest to ask for memory died with Go's
 * "out of memory: cannot allocate 524288-byte block (3538944 in use)".
 *
 * These tests drive the REAL core/rt.cc against a fake bump pool so the
 * leak is observable as pool exhaustion. Run against the pre-fix code
 * they fail (large block is not recycled; the soak exhausts the pool).
 *
 * Build/run: make test-kernel-heap
 */
#include <cstdio>
#include <cstring>
#include <cstdint>

#include "rt.h"
#include "mm.h"
#include "plat.h"

static int g_run, g_fail;
#define CHECK(cond, label)                                              \
    do {                                                                \
        g_run++;                                                        \
        if (!(cond)) {                                                  \
            g_fail++;                                                   \
            fprintf(stderr, "FAIL %s (line %d)\n", label, __LINE__);   \
        } else {                                                        \
            fprintf(stderr, "pass %s\n", label);                        \
        }                                                               \
    } while (0)

/* ---- fake bump pool (mirrors core/mm.c: allocate, never free) ---- */
#define POOL_BYTES (512ull << 20)
static uint8_t g_pool[POOL_BYTES] __attribute__((aligned(16)));
static uint64_t g_pool_ptr;
static uint64_t g_peak;

extern "C" {
void *mm_alloc(uint64_t n, uint64_t align) {
    if (!align)
        align = 16;
    uint64_t p = (g_pool_ptr + align - 1) & ~(align - 1);
    if (p + n > POOL_BYTES)
        return nullptr; /* pool exhausted, exactly like the kernel */
    g_pool_ptr = p + n;
    if (g_pool_ptr > g_peak)
        g_peak = g_pool_ptr;
    return (void *)g_pool + p;
}
uint64_t mm_top(void) { return POOL_BYTES; }
uint64_t mm_ram_top(void) { return POOL_BYTES; }
uint64_t mm_total_bytes(void) { return POOL_BYTES; }
uint64_t mm_used_bytes(void) { return g_pool_ptr; }

/* console + halt stubs: rt.cc references them via plat.h */
void console_init(void) {}
void console_putc(char) {}
void console_puts(const char *) {}
void console_hex64(uint64_t) {}
void cpu_halt(void) {}
}

/* ---- T1: small blocks recycle through the bins ---- */
static void t_small_recycle(void) {
    void *a = rt_malloc(64);
    CHECK(a != nullptr, "T1 small alloc succeeds");
    rt_free(a);
    void *b = rt_malloc(64);
    CHECK(b == a, "T1 freed small block is reused (no pool growth)");
    rt_free(b);
}

/* ---- T2: a block larger than the old 512 KiB class recycles ---- */
static void t_large_recycle(void) {
    const uint64_t big = 4ull << 20; /* 4 MiB */
    uint64_t before = g_pool_ptr;
    void *a = rt_malloc(big);
    CHECK(a != nullptr, "T2 4 MiB alloc succeeds");
    rt_free(a);
    void *b = rt_malloc(big);
    /* Pre-fix this is a different address: the 4 MiB block was leaked. */
    CHECK(b == a, "T2 freed 4 MiB block is reused");
    /* Sized to a power-of-two class, so the pool grows by at most
     * 2x the request -- the point is "one block", not "exactly n". */
    uint64_t grew = g_pool_ptr - before;
    CHECK(grew >= big && grew <= 2 * big,
          "T2 pool grew by one block, not two");
    rt_free(b);
}

/* ---- T3: doubling growth must not leak (the Go linear-memory shape) ----
 * A Go guest's memory.grow under wasm3 is a doubling realloc:
 * 2.5 MiB -> 5 -> 10 -> 20 -> 40 MiB. rt_realloc allocates the new size
 * and frees the old one, so if free does not recycle, EVERY sequence
 * permanently costs the pool another ~2x the final size. Pre-fix, the
 * second sequence could not even complete in a 512 MiB pool -- which is
 * exactly the boot failure this test was written for. Post-fix the
 * pool plateaus after the first sequence. */
static uint64_t run_doubling_sequence(void) {
    void *p = nullptr;
    uint64_t cur = (5ull << 20) / 2; /* 2.5 MiB */
    for (int i = 0; i < 5; i++) {
        void *q = rt_realloc(p, cur);
        if (!q)
            return 0;
        p = q;
        cur *= 2;
    }
    rt_free(p);
    return 1;
}

static void t_doubling_growth_no_leak(void) {
    CHECK(run_doubling_sequence() == 1,
          "T3 first doubling sequence completes");
    uint64_t after_first = g_pool_ptr;
    for (int seq = 0; seq < 3; seq++) {
        if (!run_doubling_sequence()) {
            CHECK(false, "T3 repeat doubling sequence completes");
            return;
        }
    }
    CHECK(true, "T3 three repeat doubling sequences complete");
    /* Nothing new was taken from the pool: the sizes freed by the first
     * sequence were recycled by the next three. */
    CHECK(g_pool_ptr == after_first,
          "T3 pool does not grow across repeated doubling sequences");
}

/* ---- T4: soak -- repeated large alloc/free must not grow the pool ---- */
static void t_large_soak(void) {
    uint64_t before = g_pool_ptr;
    for (int i = 0; i < 64; i++) {
        void *p = rt_malloc(2ull << 20); /* 2 MiB, above the old cap */
        if (!p) {
            CHECK(false, "T4 soak: alloc failed (pool leaked)");
            return;
        }
        rt_free(p);
    }
    /* One block, reused 64 times: the pool must not move at all. */
    CHECK(g_pool_ptr == before,
          "T4 64x 2 MiB alloc/free cycles reuse one block");
}

int main(void) {
    fprintf(stderr, "== rt_heap_test: freestanding heap ==\n");
    t_small_recycle();
    t_large_recycle();
    t_doubling_growth_no_leak();
    t_large_soak();
    fprintf(stderr, "== %d/%d passed, %d failed ==\n", g_run - g_fail, g_run,
            g_fail);
    return g_fail ? 1 : 0;
}
