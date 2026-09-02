/* arch/x86_64/mp.S -- AP trampoline.
 *
 * Phase 8.2 (planned). Brings up N AP cores via MADT/MP table + SIPI.
 * Each AP runs its own cooperative-under-interrupt scheduler over its
 * own session pool; no session migrates between cores.
 *
 * Why this does NOT require a true preemptive context switch:
 *   - The wasm3 interpreter is a virtual machine whose internal state
 *     (_sp, _mem, metacode PC) is opaque C locals in m3_exec.c. The
 *     kernel cannot save/resume it mid-op without patching wasm3
 *     (violates the "vendor wasm3, don't clean-room it" principle)
 *     or corrupting its state.
 *   - The Go runtime IS the preemption mechanism: Go 1.14+ yields
 *     cooperatively in wasm at every goroutine switch point, and our
 *     kernel switches sessions at those yield points.
 *   - Multiple cores provide the parallelism, the Go runtime provides
 *     the per-core preemption -- neither requires touching the opaque
 *     interpreter state.
 *
 * SMP-portability contract (rule #2): all cores share the SAME identity
 * PML4 set up by paging_init for CPU0. No per-arch page tables.
 */
.section .text
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
