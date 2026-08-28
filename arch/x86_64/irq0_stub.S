/* Minimal IRQ0 stub: skip red zone, EOI, return. */
.section .text
.code64
.globl irq0_stub
irq0_stub:
    subq $128, %rsp          /* skip red zone */
    pushq %rax
    movq $0x20, %rdx
    movb $0x20, %al
    outb %al, %dx            /* EOI */
    popq %rax
    addq $128, %rsp
    iretq
