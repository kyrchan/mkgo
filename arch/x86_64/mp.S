/* arch/x86_64/mp.S -- AP trampoline.
 *
 * Brings up N AP cores via MADT/MP table + SIPI.
 * Each AP runs its own cooperative-under-interrupt scheduler over its
 * own session pool; no session migrates between cores.
 *
 * SMP-portability contract (rule #2): all cores share the SAME identity
 * PML4 set up by paging_init for CPU0. No per-arch page tables.
 *
 * This file now only contains the 64-bit AP entry point (ap_entry).
 * The real-mode trampoline lives in arch/x86_64/trampoline.S and is
 * copied to 0x7000 (vector 0x07) by mp.cc.
 */
.text
.code64

/* ap_entry -- called by the trampoline after the AP has entered long
 * mode and set up its own CR3. %rdi = struct ap_boot_info *info.
 *
 * struct ap_boot_info layout (from arch/x86_64/mp.h):
 *   offset 0:  uint32_t apic_id
 *   offset 4:  uint32_t ap_index
 *   offset 8:  uint64_t ap_stack
 *   offset 16: uint64_t ap_stack_bytes
 *   offset 24: uint64_t ap_pml4
 *   offset 32: uint64_t ap_entry
 *   offset 40: volatile int ap_ready
 */
.globl ap_entry
ap_entry:
    /* Save the boot info pointer. */
    movq %rdi, %rbx

    /* Set up the AP's own stack. */
    movq 8(%rbx), %rsp
    /* Zero the stack canary (the first 16 bytes). */
    xorq %rax, %rax
    movq %rax, 0(%rsp)
    movq %rax, 8(%rsp)

    /* Disable interrupts until the AP is fully set up. */
    cli

    /* Call into C: ap_entry_c(info) in mp.cc. */
    call ap_entry_c

    /* If ap_entry_c returns, halt. */
1:  hlt
    jmp 1b
