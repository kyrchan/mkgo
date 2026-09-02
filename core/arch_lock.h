/* core/arch_lock.h -- substrate lock primitives (arch-blind interface).
 *
 * Per AGENTS.md rule #1 (core/ is arch-blind) the *interface* lives
 * here and the *implementation* lives in arch/<target>/lock.cc. We
 * provide a spinlock and an IRQ-save/restore pair. Together they are
 * the two primitives every shared kernel structure will eventually
 * guard with.
 *
 * Threat model (Sept 2026, single CPU):
 *   - One kernel core, IF=1 after kmain sti() (92313c5).
 *   - IRQ0 is the only unmasked vector; it can preempt any kernel
 *     C frame. The IRQ stub runs on the GUEST's stack and iretqs
 *     back; it does NOT call back into the kernel hot path.
 *   - Other vectors (exceptions) can still fire from inside the
 *     kernel's UART poll loop; their isr_dump path writes to the
 *     console, so it must not re-enter the console.
 *
 * SMP-portability contract (NOT yet realised -- single CPU today):
 *   - When AP cores are brought up, every shared mutable structure
 *     in core/ must be acquired with arch_spinlock_acquire OR
 *     protected by arch_irq_save. The interface here is what those
 *     later commits will use.
 *   - The implementation MUST stay correct under flat identity
 *     mapping (rule #2); no page-table-based cache-line tricks.
 *   - arch_irq_save MUST be a true cli/sti pair (or equivalent) so
 *     that acquire-after-save is a no-op for IRQ context, and
 *     restore-after-acquire re-enables IRQs at the saved state.
 *
 * Why both a spinlock AND an irq_save:
 *   - Spinlock alone is not enough against an IRQ that fires while
 *     we hold it -- the IRQ could deadlock if it tries to take the
 *     same lock. Convention: spinlock for cross-CPU mutual
 *     exclusion, irq_save for the "I'm in IRQ-scope, don't preempt
 *     me" discipline. Often both are taken together.
 *   - The convention is documented per-call-site; the API enforces
 *     nothing automatically. Code review is the backstop.
 */
#ifndef ARCH_LOCK_H
#define ARCH_LOCK_H

#include <stdint.h>

/* ---- IRQ save / restore --------------------------------------------- */

/* Snapshot of EFLAGS.IF plus a portable wrapper. arch_irq_save
 * returns a value that arch_irq_restore will pass back to the
 * architecture primitive to atomically (re)set IF.
 *
 * On x86 the encoding is the low 1 bit of the saved RFLAGS: IF=1
 * means IRQs were on, IF=0 means they were off. The implementation
 * in arch/x86_64/lock.cc uses pushf/cli/popf.
 */
typedef uint64_t arch_irq_state_t;

#ifdef __cplusplus
extern "C" {
#endif

arch_irq_state_t arch_irq_save(void);
void             arch_irq_restore(arch_irq_state_t s);

/* Disable / enable IRQ without saving. Used at boot, around init
 * sequences that must run before sti(). */
void arch_irq_disable(void);
void arch_irq_enable(void);

/* Convenience: returns 1 if IRQs are currently enabled (RFLAGS.IF).
 * Used by assertions and by the host test harness. */
int arch_irqs_enabled(void);

/* ---- spinlock -------------------------------------------------------- */

/* Ticket lock (Mellor-Crummey / John Mellor-Crummey) -- 32-bit, fits
 * in a cache line, fair (FIFO acquisition order, no starvation).
 * On x86, we use LOCK XADD for the ticket fetch and a regular store
 * + sfence for release. 4 bytes -- identity-mapped, no special
 * alignment required.
 *
 * Acquisition pattern (test-and-test-and-set, see arch/.../lock.cc):
 *   my_ticket = atomic_fetch_add(&lk->next, 1);
 *   while (lk->owner != my_ticket) cpu_pause();
 *
 * Release:
 *   lk->owner = my_ticket + 1; sfence();
 */
typedef struct {
    volatile uint32_t next;    /* next ticket to hand out */
    volatile uint32_t owner;   /* ticket currently holding the lock */
} arch_spinlock_t;

void arch_spinlock_init(arch_spinlock_t *lk);
void arch_spinlock_acquire(arch_spinlock_t *lk);
void arch_spinlock_release(arch_spinlock_t *lk);
/* Try-acquire: returns 1 if acquired, 0 if busy. Never spins. */
int  arch_spinlock_try_acquire(arch_spinlock_t *lk);

#ifdef __cplusplus
}
#endif

#endif /* ARCH_LOCK_H */
