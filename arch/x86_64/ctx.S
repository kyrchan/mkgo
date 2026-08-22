/* arch/x86_64/ctx.S -- cooperative context switch (coroutine style).
 * Each session runs on its own native stack; the scheduler lives on the
 * boot stack. Callee-saved regs + RIP are preserved per switch. */

.section .text
.code64

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
    movq  %rsp, (%rdi)      /* save old stack pointer */
    movq  %rsi, %rsp        /* adopt new stack */
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
