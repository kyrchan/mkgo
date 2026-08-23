.section .text
.code64
.globl irq0_stub
irq0_stub:
    /* minimal: EOI + return (no preemption in cooperative mode) */
    pushq %rax
    movq $0x20, %rdx
    movb $0x20, %al
    outb %al, %dx
    popq %rax
    iretq
