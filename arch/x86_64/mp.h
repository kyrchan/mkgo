/* arch/x86_64/mp.h -- MADT parser + AP bring-up.
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
#ifndef MP_H
#define MP_H

#include <stdint.h>

/* MADT (Multiple APIC Description Table) entry types */
#define MADT_LOCAL_APIC          0  /* per-CPU Local APIC */
#define MADT_IO_APIC             1  /* I/O APIC (interrupt routing) */
#define MADT_OVERRIDE            2  /* address override */
#define MADT_NMI_SOURCE          3  /* NMI source */
#define MADT_LAPIC_NMI           4  /* Local APIC NMI */
#define MADT_LOCAL_X2APIC        9  /* x2APIC (64-bit, extended) */
#define MADT_LOCAL_X2APIC_NMI    10 /* x2APIC NMI */

/* Per-CPU boot state. Each AP core gets its own copy of this struct,
 * allocated from the mm pool and identity-mapped. The trampoline
 * (mp.S) jumps here with the CPU's own state pointer in %rdi. */
struct ap_boot_info {
    uint32_t apic_id;        /* Local APIC ID (from MADT) */
    uint32_t ap_index;       /* 0 = BSP, 1..N-1 = APs */
    uint64_t ap_stack;       /* physical address of this core's stack */
    uint64_t ap_stack_bytes; /* stack size in bytes */
    uint64_t ap_pml4;        /* CR3 value (shared identity PML4) */
    uint64_t ap_entry;       /* virtual address of ap_entry (long mode) */
    volatile int ap_ready;   /* 1 = AP has booted and is running */
};

/* MADT table (ACPISDT header + entries). Parsed at boot; the parsed
 * result is kept in the kernel's mm pool so it survives past
 * ExitBootServices. */
struct madt {
    uint32_t lapic_addr;    /* Local APIC base (MMIO, default 0xFEE00000) */
    uint32_t flags;         /* MADT flags field */
    uint32_t n_cpus;        /* number of Local APIC entries (incl. BSP) */
    uint32_t n_ioapics;     /* number of I/O APIC entries */
    uint32_t apic_ids[16];  /* Local APIC IDs, sorted, 0 = BSP */
    uint32_t ioapic_ids[8]; /* I/O APIC IDs */
    uint64_t ioapic_bases[8];
};

/* Returns the parsed MADT, or NULL if no ACPI MADT is present.
 * The caller must keep the ACPI tables alive (they live in the mm
 * pool, so they do). */
const struct madt *madt_parse(void);

/* Bring up AP cores. Sends SIPI to each AP in madt->apic_ids[1..].
 * Returns the number of APs that acked (should equal n_cpus - 1). */
int ap_boot(const struct madt *m);

/* Called from mp.S after the AP has entered long mode and set up its
 * own CR3. Sets up per-CPU state and enters the scheduler.
 * Implemented in assembly (mp.S), so extern "C". */
extern "C" void ap_entry(struct ap_boot_info *info);

/* Local APIC MMIO accessors. The Local APIC is at lapic_addr (MMIO);
 * we map it through the identity map, so a direct physical load works. */
static inline uint32_t lapic_read(uint32_t off) {
    volatile uint32_t *r = (volatile uint32_t *)(uintptr_t)(0xFEE00000ULL + off);
    return r[0];
}
static inline void lapic_write(uint32_t off, uint32_t v) {
    volatile uint32_t *r = (volatile uint32_t *)(uintptr_t)(0xFEE00000ULL + off);
    r[0] = v;
}

/* I/O APIC MMIO accessors (default base 0xFEC00000). */
static inline uint32_t ioapic_read(uint32_t ioapic, uint32_t off) {
    volatile uint32_t *r = (volatile uint32_t *)(uintptr_t)(ioapic + off);
    return r[0];
}
static inline void ioapic_write(uint32_t ioapic, uint32_t off, uint32_t v) {
    volatile uint32_t *r = (volatile uint32_t *)(uintptr_t)(ioapic + off);
    r[0] = v;
}

#endif /* MP_H */