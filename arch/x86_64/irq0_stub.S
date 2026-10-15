/* Phase 8 IRQ0 preemption stub.
 *
 * Runs on the SESSION'S stack (no IST double-fault stack: we only use
 * IST for the existing #DF/#TS stubs). On entry, the CPU has pushed
 * {RIP, CS, RFLAGS, RSP, SS} in that order at (%rsp).
 *
 * We:
 *  1. Allocate a 16-slot buffer on the stack for the rest of the GPRs.
 *  2. Save RAX, RCX, RDX, RBX, RSI, RDI, R8..R15 into it.
 *  3. Call sched_current_sid() (an extern "C" function in core/sched.cc)
 *     to get the running sid.
 *  4. Stash the save buffer pointer + sid into a per-session save
 *     area (declared in core/sched.cc).
 *  5. Send EOI to PIC.
 *  6. Restore GPRs, iretq.
 *
 * The actual context switch does NOT happen here -- the IRQ is
 * cheap, no kernel-stack switch, no scheduler entry. The session
 * resumes mid-C-call as if nothing happened. The kernel scheduler
 * notices preempt_pending on its next iteration and switches.
 *
 * Saves live GPRs the same way GCC's __attribute__((interrupt)) does,
 * so any C code in the kernel (including the interpreter hot loop)
 * can be safely interrupted.
 */
.section .text
.code64

.globl preempt_save_slot
.globl sched_current_sid

.globl irq0_stub
irq0_stub:
    /* CPU pushed: RIP, CS, RFLAGS, RSP, SS in that order.
     * Total frame = 5 * 8 = 40 bytes. We need a 16-slot GPR buffer
     * below that. Allocate 16*8 = 128 bytes.
     *
     * Plus a 272-byte FPU area above the GPR buffer. The two C calls below
     * (sched_current_sid, preempt_mark_pending) clobber xmm0-15 and MXCSR,
     * and an interrupt may land between an SSE load and the store that
     * consumes it (AGENTS rule 10: preemption at ANY host instruction), so
     * without this a float-using guest resumes with the kernel's
     * registers. graphics.wasm alpha blends, so this is not theoretical.
     * Total 400 bytes; both are multiples of 16, so the stack stays
     * 16-byte aligned for those calls. */
    subq $400, %rsp

    /* Save caller-saved GPRs (rax, rcx, rdx, rsi, rdi, r8..r11) AND
     * callee-saved GPRs (rbx, rbp, r12..r15) so any C frame is
     * resumable. Layout: [0]=rax [1]=rbx [2]=rcx [3]=rdx [4]=rsi
     * [5]=rdi [6]=rbp [7]=rsp [8..15]=r8..r15 */
    movq %rax,   0*8(%rsp)
    movq %rbx,   1*8(%rsp)
    movq %rcx,   2*8(%rsp)
    movq %rdx,   3*8(%rsp)
    movq %rsi,   4*8(%rsp)
    movq %rdi,   5*8(%rsp)
    movq %rbp,   6*8(%rsp)
    movups %xmm0,  128+0(%rsp)
    movups %xmm1,  128+16(%rsp)
    movups %xmm2,  128+32(%rsp)
    movups %xmm3,  128+48(%rsp)
    movups %xmm4,  128+64(%rsp)
    movups %xmm5,  128+80(%rsp)
    movups %xmm6,  128+96(%rsp)
    movups %xmm7, 128+112(%rsp)
    movups %xmm8, 128+128(%rsp)
    movups %xmm9, 128+144(%rsp)
    movups %xmm10, 128+160(%rsp)
    movups %xmm11, 128+176(%rsp)
    movups %xmm12, 128+192(%rsp)
    movups %xmm13, 128+208(%rsp)
    movups %xmm14, 128+224(%rsp)
    movups %xmm15, 128+240(%rsp)
    stmxcsr 128+256(%rsp)

    /* slot 7: rsp at IRQ entry = current %rsp + 400 (our push) + 40
     * (CPU frame) = +440. */
    leaq 440(%rsp), %rax
    movq %rax,   7*8(%rsp)
    movq %r8,    8*8(%rsp)
    movq %r9,    9*8(%rsp)
    movq %r10,  10*8(%rsp)
    movq %r11,  11*8(%rsp)
    movq %r12,  12*8(%rsp)
    movq %r13,  13*8(%rsp)
    movq %r14,  14*8(%rsp)
    movq %r15,  15*8(%rsp)

    /* Call sched_current_sid() -- rdi already holds whatever the
     * caller had; preserve it across the call by saving into the
     * buffer first. We already did that. */
    pushq %rdi                       /* preserve rdi across call */
    call sched_current_sid
    movq %rax, %rdi                  /* sid -> rdi (1st arg) */
    call preempt_mark_pending
    popq %rdi

    /* Send EOI to master PIC. */
    movq $0x20, %rdx
    movb $0x20, %al
    outb %al, %dx

    /* Restore GPRs and iretq back to the session. */
    movq  0*8(%rsp), %rax
    movq  1*8(%rsp), %rbx
    movq  2*8(%rsp), %rcx
    movq  3*8(%rsp), %rdx
    movq  4*8(%rsp), %rsi
    movq  5*8(%rsp), %rdi
    movq  6*8(%rsp), %rbp
    movq  8*8(%rsp), %r8
    movq  9*8(%rsp), %r9
    movq 10*8(%rsp), %r10
    movq 11*8(%rsp), %r11
    movq 12*8(%rsp), %r12
    movq 13*8(%rsp), %r13
    movq 14*8(%rsp), %r14
    movq 15*8(%rsp), %r15

    movups 128+0(%rsp), %xmm0
    movups 128+16(%rsp), %xmm1
    movups 128+32(%rsp), %xmm2
    movups 128+48(%rsp), %xmm3
    movups 128+64(%rsp), %xmm4
    movups 128+80(%rsp), %xmm5
    movups 128+96(%rsp), %xmm6
    movups 128+112(%rsp), %xmm7
    movups 128+128(%rsp), %xmm8
    movups 128+144(%rsp), %xmm9
    movups 128+160(%rsp), %xmm10
    movups 128+176(%rsp), %xmm11
    movups 128+192(%rsp), %xmm12
    movups 128+208(%rsp), %xmm13
    movups 128+224(%rsp), %xmm14
    movups 128+240(%rsp), %xmm15
    ldmxcsr 128+256(%rsp)

    addq $400, %rsp
    iretq
