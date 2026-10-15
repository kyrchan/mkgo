/* arch/x86_64/ctx.S -- cooperative context switch (coroutine style).
 * Each session runs on its own native stack; the scheduler lives on the
 * boot stack. Callee-saved regs + RIP are preserved per switch.
 *
 * FPU/SSE state is preserved too, and that is NOT optional here. Saving
 * only the callee-saved GPRs is ABI-legal for an ordinary call, because
 * SysV makes xmm0-15 caller-saved -- but a session switch is not an
 * ordinary call. The scheduler switches out in the middle of the owned
 * interpreter's instruction loop, and an interrupt may land between an
 * SSE load and the store that consumes it (AGENTS rule 10: preemption at
 * ANY host instruction). Wasm guests use floats -- graphics.wasm alpha
 * blends -- so without this a resumed session computes with the previous
 * session's registers and silently produces wrong pixels.
 *
 * Saved: xmm0-15 + MXCSR. Not saved: the x87 stack and the XSAVE-area
 * components (PKRU/PCID/CSR). Wasm has no x87, and the FPU code paths in
 * the kernel do not depend on those components; skipping them also avoids
 * the FXRSTOR64 trap on a component the CPU does not implement, which a
 * whole-image FXSAVE would walk into. movups is used deliberately: the
 * area needs no particular alignment, so ctx_make can place it anywhere
 * in the frame.
 */

.section .text
.code64

/* Bytes of FPU area reserved on the stack per switch: 16 xmm registers
 * (16 B each) + MXCSR (4 B) + padding. Must match CTX_FPU_BYTES in
 * core/ctx.h, which ctx_make uses to lay out the initial frame. */
.set CTX_FPU_BYTES, 272

/* void ctx_switch(uint64_t **old_sp_out, uint64_t *new_sp)
 * rdi = &old_sp, rsi = new_sp */
.globl ctx_switch
ctx_switch:
    pushq %rbp
    pushq %rbx
    pushq %r12
    pushq %r13
    pushq %r14
    pushq %r15
    subq  $CTX_FPU_BYTES, %rsp
    movups %xmm0,  0(%rsp)
    movups %xmm1,  16(%rsp)
    movups %xmm2,  32(%rsp)
    movups %xmm3,  48(%rsp)
    movups %xmm4,  64(%rsp)
    movups %xmm5,  80(%rsp)
    movups %xmm6,  96(%rsp)
    movups %xmm7, 112(%rsp)
    movups %xmm8, 128(%rsp)
    movups %xmm9, 144(%rsp)
    movups %xmm10, 160(%rsp)
    movups %xmm11, 176(%rsp)
    movups %xmm12, 192(%rsp)
    movups %xmm13, 208(%rsp)
    movups %xmm14, 224(%rsp)
    movups %xmm15, 240(%rsp)
    stmxcsr 256(%rsp)

    movq  %rsp, (%rdi)      /* saved sp = base of the FPU area */
    movq  %rsi, %rsp        /* adopt new stack */

    movups  0(%rsp), %xmm0
    movups  16(%rsp), %xmm1
    movups  32(%rsp), %xmm2
    movups  48(%rsp), %xmm3
    movups  64(%rsp), %xmm4
    movups  80(%rsp), %xmm5
    movups  96(%rsp), %xmm6
    movups 112(%rsp), %xmm7
    movups 128(%rsp), %xmm8
    movups 144(%rsp), %xmm9
    movups 160(%rsp), %xmm10
    movups 176(%rsp), %xmm11
    movups 192(%rsp), %xmm12
    movups 208(%rsp), %xmm13
    movups 224(%rsp), %xmm14
    movups 240(%rsp), %xmm15
    ldmxcsr 256(%rsp)
    /* The callee-saved block sits directly above the FPU area, r15 at its
     * base (rbp is pushed first, so it lands at the highest address, and
     * the pops below read r15 first). Skip the FPU area to reach it; after
     * the six pops and the ret, rsp is sp + 328. */
    addq  $CTX_FPU_BYTES, %rsp

    popq  %r15
    popq  %r14
    popq  %r13
    popq  %r12
    popq  %rbx
    popq  %rbp
    ret

/* Bootstrap: entered with a hand-crafted stack whose top holds the entry
 * trampoline below; ctx_start calls session_main then exits to scheduler. */
.globl ctx_trampoline
ctx_trampoline:
    call  sched_session_main   /* never returns; parks itself when done */
1:  hlt
    jmp 1b
