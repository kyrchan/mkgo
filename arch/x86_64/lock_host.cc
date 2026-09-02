/* arch/x86_64/lock_host.cc -- host-side implementation of the
 * arch_lock API for the host test harness.
 *
 * The host test runs on Linux, not bare metal. Inline asm like `cli`
 * would segfault in userspace. We use C11 atomics via the
 * __atomic_* builtins (always available in g++ >= 4.8) and a
 * no-op for the IRQ primitives.
 *
 * Same C-callable signatures as arch/x86_64/lock.cc, just different
 * internals. Tests in tools/hosttest.cc link this instead of the
 * kernel's lock.cc.
 */
#include "arch_lock.h"
#include <stdint.h>

extern "C" {

/* ---- IRQ save / restore (host: no-op, Linux userspace) ------------- */

arch_irq_state_t arch_irq_save(void) {
    /* Linux userspace: pretend we saved flags. The host test never
     * actually receives interrupts so this is just a marker. */
    return 0;
}
void arch_irq_restore(arch_irq_state_t s) { (void)s; }
void arch_irq_disable(void) {}
void arch_irq_enable(void) {}
int  arch_irqs_enabled(void) { return 1; }

/* ---- spinlock (ticket lock via __atomic builtins) ------------------- */

void arch_spinlock_init(arch_spinlock_t *lk) {
    __atomic_store_n(&lk->next, 0, __ATOMIC_RELAXED);
    __atomic_store_n(&lk->owner, 0, __ATOMIC_RELAXED);
}

void arch_spinlock_acquire(arch_spinlock_t *lk) {
    uint32_t my = __atomic_fetch_add(&lk->next, 1, __ATOMIC_ACQ_REL);
    while (__atomic_load_n(&lk->owner, __ATOMIC_ACQUIRE) != my) {
        __asm__ volatile("pause" ::: "memory");
    }
}

void arch_spinlock_release(arch_spinlock_t *lk) {
    /* owner = my + 1. Since we hold the lock, my = owner = next - 1.
     * So just store `next` into `owner`. */
    uint32_t n = __atomic_load_n(&lk->next, __ATOMIC_RELAXED);
    __atomic_store_n(&lk->owner, n, __ATOMIC_RELEASE);
}

int arch_spinlock_try_acquire(arch_spinlock_t *lk) {
    uint32_t cur = __atomic_load_n(&lk->owner, __ATOMIC_ACQUIRE);
    uint32_t expected = __atomic_load_n(&lk->next, __ATOMIC_ACQUIRE);
    if (cur != expected) return 0;
    uint32_t new_next = expected + 1;
    /* CAS on lk->next. We use the same pattern as the kernel:
     * __atomic_compare_exchange returns 1 on success. */
    return __atomic_compare_exchange_n(
        &lk->next, &expected, new_next,
        /*weak=*/0, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE);
}

} /* extern "C" */
