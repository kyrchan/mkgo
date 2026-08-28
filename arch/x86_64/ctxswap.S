/* ctx_save(buf): save callee-saved regs + rsp + retaddr.
 * ctx_restore(buf): restore and jump. */
.section .text
.code64

.globl ctx_save
ctx_save:
    movq (%rsp), %rax         /* return address -> resume rip */
    leaq 8(%rsp), %rcx        /* caller's rsp (past retaddr) */
    movq %rbx, 0*8(%rdi)
    movq %rbp, 1*8(%rdi)
    movq %r12, 2*8(%rdi)
    movq %r13, 3*8(%rdi)
    movq %r14, 4*8(%rdi)
    movq %r15, 5*8(%rdi)
    movq %rcx, 6*8(%rdi)      /* resume rsp */
    movq %rax, 7*8(%rdi)      /* resume rip */
    xorq %rax, %rax
    ret                       /* return normally to caller */

.globl ctx_restore
ctx_restore:
    movq 0*8(%rsi), %rbx
    movq 1*8(%rsi), %rbp
    movq 2*8(%rsi), %r12
    movq 3*8(%rsi), %r13
    movq 4*8(%rsi), %r14
    movq 5*8(%rsi), %r15
    movq 6*8(%rsi), %rsp      /* switch stack FIRST */
    movq 7*8(%rsi), %rax      /* target rip */
    jmp *%rax                 /* jump to saved rip */
