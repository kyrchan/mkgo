#include "ctx.h"

uint64_t *ctx_make(uint8_t *stack_base, uint64_t size) {
    uint64_t top = ((uint64_t)(uintptr_t)(stack_base + size)) & ~15ULL;
    uint64_t *sp = (uint64_t *)(uintptr_t)top;
    /* ctx_switch pops r15,r14,r13,r12,rbx,rbp then rets.
     * RIP slot sits HIGHEST; callee-saved slots are zeros below it. */
    *--sp = (uint64_t)(uintptr_t)&ctx_trampoline; /* ret target */
    *--sp = 0; /* rbp */
    *--sp = 0; /* rbx */
    *--sp = 0; /* r12 */
    *--sp = 0; /* r13 */
    *--sp = 0; /* r14 */
    *--sp = 0; /* r15 */
    return sp;
}
