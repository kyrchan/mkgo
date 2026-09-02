/* arch/x86_64/mp.cc -- MADT parser + AP bring-up.
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
#include "mp.h"
#include "plat.h"
#include "io.h"
#include "mm.h"
#include "lib.h"
#include "boot.h"
#include "sched.h"

/* MADT signature is "APIC" (0x41504943 in little-endian). */
#define MADT_SIG 0x43495041

/* MADT entry types */
#define MADT_LOCAL_APIC          0
#define MADT_IO_APIC             1

static struct madt g_madt;
static int g_madt_ok = 0;

/* Parse the MADT at `phys`. The MADT is an ACPI SDT: a 32-byte header
 * followed by a fixed body (Local APIC address + flags) and then
 * MADT entries. We walk the entries collecting Local APIC IDs. */
static void madt_parse_at(uint64_t phys) {
    if (!phys || g_madt_ok)
        return;
    /* The MADT lives in EfiACPIMemoryNVS which is identity-mapped, so
     * a direct physical load works. */
    uint8_t *t = (uint8_t *)(uintptr_t)phys;
    if (memcmp(t, "APIC", 4) != 0)
        return;
    uint32_t len = *(uint32_t *)(t + 4);
    if (len < 40 || len > 65536)
        return;
    g_madt.lapic_addr = *(uint32_t *)(t + 24);
    g_madt.flags      = *(uint32_t *)(t + 28);
    g_madt.n_cpus = 0;
    g_madt.n_ioapics = 0;
    uint32_t off = 36; /* past the SDT header + MADT fixed body */
    while (off + 2 <= len && g_madt.n_cpus < 16) {
        uint8_t type = t[off];
        uint8_t elen = t[off + 1];
        if (elen < 2)
            break;
        if (type == MADT_LOCAL_APIC && elen >= 8) {
            uint32_t id = *(uint32_t *)(t + off + 4);
            uint8_t  en = t[off + 8];
            if (en) {
                g_madt.apic_ids[g_madt.n_cpus++] = id;
            }
        } else if (type == MADT_IO_APIC && elen >= 12 && g_madt.n_ioapics < 8) {
            uint32_t id = *(uint32_t *)(t + off + 4);
            uint64_t base = *(uint64_t *)(t + off + 8);
            g_madt.ioapic_ids[g_madt.n_ioapics] = id;
            g_madt.ioapic_bases[g_madt.n_ioapics] = base;
            g_madt.n_ioapics++;
        }
        off += elen;
    }
    /* The first Local APIC entry is the BSP (APIC ID 0 on most
     * firmware). Sort so apic_ids[0] is the BSP. */
    for (uint32_t i = 0; i < g_madt.n_cpus; i++)
        for (uint32_t j = i + 1; j < g_madt.n_cpus; j++)
            if (g_madt.apic_ids[i] > g_madt.apic_ids[j]) {
                uint32_t tmp = g_madt.apic_ids[i];
                g_madt.apic_ids[i] = g_madt.apic_ids[j];
                g_madt.apic_ids[j] = tmp;
            }
    g_madt_ok = 1;
}

/* Returns the MADT physical address from the boot info struct. */
extern "C" uint64_t boot_madt_phys(void) {
    return boot_info()->madt_phys;
}

const struct madt *madt_parse(void) {
    uint64_t phys = boot_madt_phys();
    if (phys && !g_madt_ok)
        madt_parse_at(phys);
    return g_madt_ok ? &g_madt : 0;
}

int ap_boot(const struct madt *m) {
    if (!m) return 0;
    /* Send SIPI to each AP in m->apic_ids[1..n_cpus-1].
     * Each SIPI must be retried (the spec allows up to 255 retries).
     * We wait for delivery status to clear (bit 12 in ICR low). */
    int acked = 0;
    for (uint32_t i = 1; i < m->n_cpus && i < 16; i++) {
        uint32_t id = m->apic_ids[i];
        /* SIPI message: vector 0x30 (our trampoline), shorthand
         * destination = the AP's APIC ID. */
        lapic_write(0x310, id);              /* ICR high (dest) */
        lapic_write(0x300, 0x4400 | 0x30);   /* ICR low: assert, vector 0x30 */
        for (int t = 0; t < 100000; t++) {
            if (!(lapic_read(0x300) & (1 << 12)))
                break;
        }
        acked++;
    }
    return acked;
}

/* Called from mp.S after the AP has entered long mode and set up its
 * own CR3. %rdi = struct ap_boot_info *info. */
extern "C" void ap_entry_c(struct ap_boot_info *info) {
    sched_ap_boot(info);
}

/* C-side entry point for the trampoline. The asm stub (mp.S) jumps
 * here with the boot info pointer in %rdi. */
void ap_entry(struct ap_boot_info *info) {
    ap_entry_c(info);
}
