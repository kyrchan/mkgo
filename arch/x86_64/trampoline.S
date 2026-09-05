/* arch/x86_64/trampoline.S -- AP real-mode trampoline for vector 0x07 (0x7000).
 *  Copied to 0x7000 and executed by each AP after SIPI.
 *  Uses indirect far jumps via low-memory pointers at 0x7400 to avoid
 *  absolute patching of the trampoline itself.
 *
 *  Layout (all <1M, identity-mapped):
 *    0x7000  real code (0x00-0x3F)
 *    0x7040  protected code (0x40-0x7F)
 *    0x7080  long code (0x80-0x1FF)
 *    0x7200  GDT (0x200-0x21F)
 *    0x7220  GDTR (0x220-0x225)
 *    0x7400  far ptr prot (6) @0x7400, far ptr long (6) @0x7406, vars @0x7410
 */

.code16
.section .trampoline, "ax"
.globl trampoline_start
.globl trampoline_end
.globl trampoline_gdt
.globl trampoline_gdt_end
.globl trampoline_gdt_desc
trampoline_start:
    cli
    xorw %ax, %ax
    movw %ax, %ds
    movw %ax, %es
    movw %ax, %ss
    movw $0x7000, %sp
    lgdtl 0x7220
    movl %cr0, %eax
    orl $1, %eax
    movl %eax, %cr0
    ljmpl *0x7400

    /* pad to 0x40 */
    .fill 0x40 - (. - trampoline_start), 1, 0x90

/* protected mode entry at 0x7040 */
.code32
prot:
    movw $0x10, %ax
    movw %ax, %ds
    movw %ax, %es
    movw %ax, %fs
    movw %ax, %gs
    movw %ax, %ss
    movl %cr4, %eax
    orl $0x20, %eax
    movl %eax, %cr4
    movl 0x7410, %eax
    movl %eax, %cr3
    movl $0xC0000080, %ecx
    rdmsr
    orl $0x100, %eax
    wrmsr
    movl %cr0, %eax
    orl $0x80000000, %eax
    movl %eax, %cr0
    ljmpl *0x7406

    .fill 0x80 - (. - trampoline_start), 1, 0x90

/* long mode entry at 0x7080 */
.code64
longm:
    movq 0x7418, %rsp
    movq 0x7430, %rdi
    movq 0x7428, %rax
    call *%rax
1:  hlt
    jmp 1b

    .fill 0x200 - (. - trampoline_start), 1, 0x90

/* GDT at 0x7200 */
gdt:
    .quad 0x0000000000000000
    .quad 0x00CF9A000000FFFF /* 0x08: 32-bit code */
    .quad 0x00CF92000000FFFF /* 0x10: data */
    .quad 0x00AF9A000000FFFF /* 0x18: 64-bit code */
gdt_end:

    .fill 0x220 - (. - trampoline_start), 1, 0x90

/* GDTR at 0x7220 */
gdt_desc:
    .word gdt_end - gdt - 1
    .long 0x7200
    .long 0

    .fill 0x400 - (. - trampoline_start), 1, 0x90

/* variables at 0x7400 */
vars:
    /* 0x7400: far ptr to prot (offset 0x7040, selector 0x08) */
    .long 0x7040
    .word 0x08
    /* 0x7406: far ptr to long (offset 0x7080, selector 0x18) */
    .long 0x7080
    .word 0x18
    /* 0x740C: padding */
    .long 0
    /* 0x7410: ap_pml4 */
    .quad 0
    /* 0x7418: ap_stack */
    .quad 0
    /* 0x7420: padding */
    .quad 0
    /* 0x7428: ap_entry */
    .quad 0
    /* 0x7430: ap_info */
    .quad 0
    /* 0x7438: padding */
    .quad 0

trampoline_end:
