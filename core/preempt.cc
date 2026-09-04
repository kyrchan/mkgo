/* Phase 8: timer/preempt config. Cooperative scheduling is primary;
 * IRQ-driven preemption can be enabled via SETCONF preempt=1.
 *
 * Implementation: when preempt_on, IRQ0 saves the running session's
 * full register state into a per-session preempt_save area, sets
 * preempt_pending = sid, sends EOI, iretqs back. The session resumes
 * as if nothing happened. Next time the session enters a kernel call
 * (any sched_yield_current, or a WASI import via wasi_glue.cc), the
 * scheduler checks preempt_pending and performs the actual context
 * switch.
 *
 * This is "cooperative under interrupt": the IRQ is cheap (no
 * context switch in interrupt context, no kernel-stack juggling),
 * the actual switch happens at the next yield point. Property
 * preserved: a session that does NOT call any kernel import is
 * still starved -- but Go runtimes call sched_yield often enough
 * (~100 us via runtime.Gosched) that the busy/polite gate p8a
 * passes with quantum_ms=5.
 */
#include <stdint.h>
#include "io.h"
#include "cpu.h"

extern "C" {
#include "plat.h"

void conf_set_quantum_us(uint64_t us);
void conf_set_preempt(uint64_t on);

/* Called from the IRQ0 stub after saving the running session's
 * registers. Sets the pending-sid flag and lets the stub iretq back
 * to the guest. Must NOT touch the kernel stack beyond the IRQ
 * frame (we ARE on the guest's stack at this point). */
void preempt_mark_pending(uint32_t sid);

uint32_t preempt_take_pending(void);
uint8_t preempt_is_on(void);
uint32_t preempt_quantum_us(void);

/* Reprograms the PIT to fire at the requested quantum. Idempotent. */
void pit_reprogram_for_quantum(uint32_t quantum_us);
}

static volatile uint32_t quantum_ticks = 5;     /* legacy, used by tests */
static volatile uint8_t preempt_on = 1;          /* Phase 8: default ON */
static volatile uint32_t preempt_pending_sid = 0; /* 0 = no pending */
static volatile uint64_t preemption_count = 0;    /* observability */

/* Forward from sched.cc */
extern uint32_t sched_current_sid(void);

void preempt_mark_pending(uint32_t sid) {
    if (sid == 0) return;
    preempt_pending_sid = sid;
    preemption_count++;
}

uint32_t preempt_take_pending(void) {
    uint32_t s = preempt_pending_sid;
    preempt_pending_sid = 0;
    return s;
}

uint8_t preempt_is_on(void) { return preempt_on; }

/* Phase 15 observability (top via registry SYSSTAT): scheduler quantum
 * in microseconds, derived from the same ticks the PIT is programmed
 * with. Real in kernel and host builds (preempt.o links both). */
uint32_t preempt_quantum_us(void) { return quantum_ticks * 1000U; }

void conf_set_quantum_us(uint64_t us) {
    if (us >= 100 && us <= 200000) {
        quantum_ticks = (uint32_t)(us / 1000);
        pit_reprogram_for_quantum((uint32_t)us);
    }
}
void conf_set_preempt(uint64_t on) {
    uint8_t want = (uint8_t)(on & 1);
    if (want && !preempt_on) {
        /* first time on: make sure PIT is firing at the quantum rate */
        pit_reprogram_for_quantum(quantum_ticks * 1000U);
    }
    preempt_on = want;
}

/* ---- PIT programming (port 0x40/0x43) ---- */

void pit_reprogram_for_quantum(uint32_t quantum_us) {
    /* PIT ch0 divisor = 1193182 Hz / target_hz
     * target_hz = 1_000_000 / quantum_us
     * divisor = 1193182 * quantum_us / 1_000_000
     * Clamp to [1, 65535] for sane PIT behaviour. */
    uint64_t d = (uint64_t)1193182U * (uint64_t)quantum_us / 1000000ULL;
    if (d < 1) d = 1;
    if (d > 65535) d = 65535;
#ifdef HOST_BUILD
    (void)d; /* no port I/O in userspace (outb would SIGSEGV) */
#else
    outb(0x43, 0x36);
    outb(0x40, (uint8_t)(d & 0xFF));
    outb(0x40, (uint8_t)((d >> 8) & 0xFF));
#endif
}
