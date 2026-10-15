#include "ctx.h"

#include <string.h>

uint64_t *ctx_make(uint8_t *stack_base, uint64_t size) {
    uint64_t top = ((uint64_t)(uintptr_t)(stack_base + size)) & ~15ULL;
    /* Frame layout, mirroring ctx_switch exactly. From the returned sp
     * upward: the 272-byte FPU area, then r15 r14 r13 r12 rbx rbp, then
     * the return address. (rbp is pushed first, so it lands at the
     * highest address of that block and the restore pops r15 first.)
     *
     * The total is 328 bytes, which is 8 mod 16 on purpose: ctx_switch
     * lands at ctx_trampoline with rsp = sp + 328, and the trampoline
     * immediately `call`s, which requires rsp = 0 (mod 16) at the call. The
     * old 56-byte frame got that for free; a frame that is 0 mod 16 runs
     * the callee misaligned, which faults on glibc's aligned SSE spills
     * rather than anything in our own code. */
    uint64_t *slot = (uint64_t *)(uintptr_t)(top - (CTX_FPU_BYTES + 6 * 8 + 8));
    /* A session that has never run has no FPU state to preserve: zero the
     * registers, but seed MXCSR with the masked-exception value the CPU
     * boots with (0x1F80). Zeroing it instead means "every FP exception
     * unmasked", and the first inexact f64 result in the guest then traps
     * as an unmasked SIMD exception inside the engine -- which is exactly
     * how g3 (stock Go on wasm3) died when this area was first added. */
    memset(slot, 0, CTX_FPU_BYTES);
    slot[CTX_FPU_BYTES / 8 - 2] = 0x00001F80u; /* MXCSR */
    slot[CTX_FPU_BYTES / 8 + 0] = 0;           /* r15 */
    slot[CTX_FPU_BYTES / 8 + 1] = 0;           /* r14 */
    slot[CTX_FPU_BYTES / 8 + 2] = 0;           /* r13 */
    slot[CTX_FPU_BYTES / 8 + 3] = 0;           /* r12 */
    slot[CTX_FPU_BYTES / 8 + 4] = 0;           /* rbx */
    slot[CTX_FPU_BYTES / 8 + 5] = 0;           /* rbp */
    slot[CTX_FPU_BYTES / 8 + 6] = (uint64_t)(uintptr_t)&ctx_trampoline;
    return slot;
}
