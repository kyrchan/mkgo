#ifndef PERF_H
#define PERF_H
#include <stdint.h>

/* HP0 perf counters (Stage 0a): cheap baseline telemetry for the
 * high-performance track. One relaxed-atomic u64 per counter; the
 * increment is a single LOCK XADD on x86_64 (~a few ns uncontended)
 * and a C11 __atomic builtin on host builds. Arch-blind: no asm,
 * no per-arch code (rule 1).
 *
 * Semantics (all cumulative since boot, wrap-safe by u64):
 *   SEND/RECV/BYTES: port_send ok (all 3 paths: kernel dispatch,
 *     fsroute intercept, queue enqueue) + kernel-side enqueues
 *     (enqueue_by_name, kernel_enqueue) count as SEND; port_recv > 0
 *     counts as RECV. Kernel-originated traffic therefore appears as
 *     RECV without a matching SEND -- asymmetric by design, deltas
 *     are what gates assert.
 *   FDW_*: console fd_write(1/2) calls + payload bytes, plus routed
 *     fd writes (fd>=3) completed bytes. Per-BYTE UART cost stays
 *     invisible here by design; pair with FDW_CALLS to see it.
 *   BLK_*: completed sectors + device notifies (1 notify/sector today;
 *     batching work must move SECTORS/NOTIFY >> 1).
 *   NET_*: TX kicks, TX completions, RX deliveries to the §6 window,
 *     RX drops on full window.
 *   PICKS/SCANS: scheduler session picks + full-pool scans (all_dead).
 *     SCANS >> PICKS means idle spinning; HP3 per-core queues must
 *     move it.
 *
 * Wire: registry op 13 = PERFSTAT -> {u32 status=0, u64 x PERF_N}.
 * No capability required (v1 observability, same as SYSSTAT/LOGDUMP).
 * Cost note for HP2: if the atomic ever shows in profiles, switch to
 * per-core shards summed on read (AP cores exist now). */
enum perf_ctr {
    PERF_IPC_SEND = 0,
    PERF_IPC_RECV = 1,
    PERF_IPC_BYTES = 2,
    PERF_FDW_CALLS = 3,
    PERF_FDW_BYTES = 4,
    PERF_BLK_SECTORS = 5,
    PERF_BLK_NOTIFY = 6,
    PERF_NET_TX_KICK = 7,
    PERF_NET_TX_DONE = 8,
    PERF_NET_RX = 9,
    PERF_NET_DROP = 10,
    PERF_SCHED_PICKS = 11,
    PERF_SCHED_SCANS = 12,
    PERF_N = 13,
};

void perf_inc(int id, uint64_t n);
uint64_t perf_get(int id);

#endif /* PERF_H */
