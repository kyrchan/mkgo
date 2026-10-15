/* arch/x86_64/lock.cc -- substrate lock primitives (x86_64 implementation).
 *
 * Pair with core/arch_lock.h. See that file for the contract.
 *
 * Why inline asm and not compiler intrinsics:
 *   - core/ compiles with -fno-exceptions -fno-rtti -fno-threadsafe-
 *     statics; we still need the C++ mode. Intrinsics are fine
 *     inside extern "C" bodies but the kernel does not include
 *     <atomic> (freestanding).
 *   - The instructions we need (lock xadd, lock cmpxchg, pushf, popf)
 *     are well-defined and the asm is one line each; intrinsic
 *     wrappers would be more code, not less.
 *
 * Memory ordering on x86:
 *   - Loads are not reordered with other loads.
 *   - Stores are not reordered with other stores.
 *   - A LOCK-prefixed instruction is a full fence.
 *   - We still emit sfence after store-release for documentation
 *     and to make the intent visible to other backends (aarch64
 *     would need a real dmb ish here).
 */
#include "arch_lock.h"
#include <stdint.h>

/* pushf; cli; pop rax  -- saves IF, clears it, returns saved value.
 * Compiles to 3 instructions; the function is __attribute__((noinline))
 * so the compiler can't reorder the cli into a later basic block. */
__attribute__((noinline))
uint64_t arch_irq_save_x86(void) {
    uint64_t s;
    __asm__ volatile(
        "pushfq\n"
        "cli\n"
        "popq %0\n"
        : "=r"(s)
        :
        : "memory"
    );
    return s;
}

__attribute__((noinline))
void arch_irq_restore_x86(uint64_t s) {
    /* push rax; popfq -- writes IF from the saved state. We push the
     * saved value (low bit = IF) and popfq restores it. Other RFLAGS
     * bits are loaded from the value too; we only ever want to
     * restore IF so we mask. */
    __asm__ volatile(
        "pushq %0\n"
        "popfq\n"
        :
        : "r"(s)
        : "memory"
    );
}

__attribute__((noinline))
void arch_irq_disable_x86(void) {
    __asm__ volatile("cli" ::: "memory");
}

__attribute__((noinline))
void arch_irq_enable_x86(void) {
    __asm__ volatile("sti" ::: "memory");
}

__attribute__((noinline))
int arch_irqs_enabled_x86(void) {
    uint64_t f;
    __asm__ volatile(
        "pushfq\n"
        "popq %0\n"
        : "=r"(f)
        :
        : "memory"
    );
    return (int)(f & 0x200ULL); /* IF is bit 9 */
}

/* The C-callable wrappers in arch_lock.h. Each one is a thin shim
 * around the asm body above; keeping them in C++ linkage so the
 * host test harness can mock them. */
extern "C" {

arch_irq_state_t arch_irq_save(void) {
    return (arch_irq_state_t)arch_irq_save_x86();
}
void arch_irq_restore(arch_irq_state_t s) {
    arch_irq_restore_x86((uint64_t)s);
}
void arch_irq_disable(void) { arch_irq_disable_x86(); }
void arch_irq_enable(void) { arch_irq_enable_x86(); }
int  arch_irqs_enabled(void) { return arch_irqs_enabled_x86(); }

/* ---- spinlock (Mellor-Crummey ticket lock) -------------------------- */

void arch_spinlock_init(arch_spinlock_t *lk) {
    lk->next = 0;
    lk->owner = 0;
    /* The two stores need to be visible before any acquire call.
     * arch_spinlock_init runs at boot under cli, but a release fence
     * here is cheap and matches the convention. */
    __asm__ volatile("" ::: "memory");
}

void arch_spinlock_acquire(arch_spinlock_t *lk) {
    /* Fetch-and-increment `next` to get our ticket. LOCK XADD is a
     * full memory fence, no extra barrier needed. */
    uint32_t my;
    __asm__ volatile(
        "lock xaddl %0, %1\n"
        : "=r"(my), "+m"(lk->next)
        : "0"(1)
        : "memory", "cc"
    );
    /* Wait for our turn. We use the test-and-test-and-set pattern:
     * read `owner` (uncached) without LOCK prefix in the loop, then
     * a single LOCK-prefixed read once we see our ticket. Saves
     * cache-line contention under heavy contention. */
    while (lk->owner != my) {
        __asm__ volatile("pause" ::: "memory");
    }
    /* Acquire fence: pairs with the sfence in release. */
    __asm__ volatile("" ::: "memory");
}

void arch_spinlock_release(arch_spinlock_t *lk) {
    /* Advance the SERVING counter (owner) by exactly one.  The holder
     * is the only writer, so owner++ hands the lock to the next ticket
     * in line whatever the queue depth.
     *
     * The old code stored `next` into `owner`, reasoning "my ticket =
     * next - 1, so store next".  That only holds when nobody is
     * queued.  With k waiters, next = my + k + 1, so owner = next
     * skips k tickets: those waiters never observe owner == their
     * ticket and the lock livelocks forever.  The kernel only ever
     * ran one core, so no queue ever formed and the bug stayed
     * latent; the SMP console (first genuinely contended lock) turned
     * it into a boot wedge during AP bring-up.  The port ring has the
     * same exposure as soon as two cores enqueue at once. */
    __asm__ volatile(
        "lock addl $1, %0\n"
        "sfence\n"
        : "+m"(lk->owner)
        :
        : "memory", "cc"
    );
}

int arch_spinlock_try_acquire(arch_spinlock_t *lk) {
    /* Atomically: if (lk->owner == lk->next) { lk->next++; return 1; }
     * else { return 0; }  Implemented as LOCK CMPXCHG. */
    uint32_t cur = lk->owner;
    uint32_t expected = lk->next;
    if (cur != expected) return 0;
    uint32_t new_next = expected + 1;
    uint32_t prev;
    __asm__ volatile(
        "lock cmpxchgl %3, %1\n"
        : "=a"(prev), "+m"(lk->next)
        : "0"(expected), "r"(new_next)
        : "memory", "cc"
    );
    return prev == expected;
}

} /* extern "C" */
