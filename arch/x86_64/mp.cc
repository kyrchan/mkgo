/* arch/x86_64/mp.cc -- MADT parser + AP bring-up.
 *
 * Phase 8.2 (planned). Brings up N AP cores via MADT/MP table + SIPI.
 * Each AP runs its own cooperative-under-interrupt scheduler over its
 * own session pool; no session migrates between cores.
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

static struct madt g_madt;
static int g_madt_ok = 0;

/* Parse the MADT at `phys`. The MADT is an ACPI SDT: a 36-byte header
 * followed by a fixed body (Local APIC address + flags) and then
 * MADT entries. We walk the entries collecting Local APIC IDs. */
static void madt_parse_at(uint64_t phys) {
    if (!phys || g_madt_ok)
        return;
    uint8_t *t = (uint8_t *)(uintptr_t)phys;
    if (memcmp(t, "APIC", 4) != 0) {
        console_puts("[ap] MADT sig mismatch\n");
        return;
    }
    uint32_t len = *(uint32_t *)(t + 4);
    console_puts("[ap] MADT len=");
    console_hex64(len);
    console_puts(" flags=");
    console_hex64(*(uint32_t *)(t + 40));
    console_puts("\n");
    if (len < 44 || len > 65536)
        return;
    g_madt.lapic_addr = *(uint32_t *)(t + 36);
    g_madt.flags      = *(uint32_t *)(t + 40);
    g_madt.n_cpus = 0;
    g_madt.n_ioapics = 0;
    uint32_t off = 44; /* past SDT header (36) + MADT fixed body (8) */
    console_puts("[ap] MADT entries:");
    for (uint32_t j = 36; j < len && j < 36 + 64; j++) {
        console_hex64(t[j]);
    }
    console_puts("\n");
    while (off + 2 <= len && g_madt.n_cpus < 16) {
        uint8_t type = t[off];
        uint8_t elen = t[off + 1];
        console_puts("[ap] entry off=");
        console_hex64(off);
        console_puts(" type=");
        console_hex64(type);
        console_puts(" elen=");
        console_hex64(elen);
        console_puts("\n");
        if (elen < 2)
            break;
        if (type == MADT_LOCAL_APIC && elen >= 8) {
            uint8_t apic_id = t[off + 3];
            uint32_t flags = *(uint32_t *)(t + off + 4);
            if (flags & 1)
                g_madt.apic_ids[g_madt.n_cpus++] = apic_id;
        } else if (type == MADT_LOCAL_X2APIC && elen >= 16 && g_madt.n_cpus < 16) {
            uint32_t apic_id = *(uint32_t *)(t + off + 4);
            uint32_t flags = *(uint32_t *)(t + off + 8);
            if (flags & 1)
                g_madt.apic_ids[g_madt.n_cpus++] = apic_id;
        } else if (type == MADT_IO_APIC && elen >= 12 && g_madt.n_ioapics < 8) {
            uint32_t id = *(uint32_t *)(t + off + 2);
            uint32_t base = *(uint32_t *)(t + off + 8);
            g_madt.ioapic_ids[g_madt.n_ioapics] = id;
            g_madt.ioapic_bases[g_madt.n_ioapics] = base;
            g_madt.n_ioapics++;
        }
        off += elen;
    }
    for (uint32_t i = 0; i < g_madt.n_cpus; i++)
        for (uint32_t j = i + 1; j < g_madt.n_cpus; j++)
            if (g_madt.apic_ids[i] > g_madt.apic_ids[j]) {
                uint32_t tmp = g_madt.apic_ids[i];
                g_madt.apic_ids[i] = g_madt.apic_ids[j];
                g_madt.apic_ids[j] = tmp;
            }
    g_madt_ok = 1;
}

extern "C" uint64_t boot_madt_phys(void) {
    return boot_info()->madt_phys;
}

const struct madt *madt_parse(void) {
    uint64_t phys = boot_madt_phys();
    if (phys && !g_madt_ok)
        madt_parse_at(phys);
    return g_madt_ok ? &g_madt : 0;
}

extern "C" char trampoline_start[];
extern "C" char trampoline_end[];

static inline void tramp_copy(void) {
    uint8_t *dst = (uint8_t *)(uintptr_t)0x7000;
    uint8_t *src = (uint8_t *)trampoline_start;
    uint64_t len = (uint64_t)(trampoline_end - trampoline_start);
    if (len > 0x1000) len = 0x1000;
    for (uint64_t i = 0; i < len; i++) dst[i] = src[i];
    for (uint64_t i = len; i < 0x1000; i++) dst[i] = 0;
    console_puts("[ap] trampoline copied to 0x7000, ");
    console_hex64(len);
    console_puts(" bytes\n");
}

int ap_boot(const struct madt *m) {
    if (!m) return 0;
    // BSP is cpu0
    console_puts("[ap] cpu0 booted\n");
    if (m->n_cpus < 2) return 0;
    tramp_copy();
    uint64_t pml4_pa = paging_pml4_pa();
    int acked = 0;
    for (uint32_t i = 1; i < m->n_cpus && i < 16; i++) {
        uint32_t id = m->apic_ids[i];
        struct ap_boot_info *info = (struct ap_boot_info *)mm_alloc(sizeof(*info), 64);
        if (!info) {
            console_puts("[ap] alloc info failed\n");
            continue;
        }
        info->apic_id = id;
        info->ap_index = i;
        void *stk = mm_alloc(16384, 16);
        if (!stk) {
            console_puts("[ap] alloc stack failed\n");
            continue;
        }
        info->ap_stack = (uint64_t)((uint8_t *)stk + 16384);
        info->ap_stack_bytes = 16384;
        info->ap_pml4 = pml4_pa;
        info->ap_entry = (uint64_t)(uintptr_t)ap_entry;
        info->ap_ready = 0;

        // Fill low-memory variables for trampoline (see trampoline.S layout)
        *(volatile uint64_t *)(uintptr_t)0x7410 = pml4_pa;
        *(volatile uint64_t *)(uintptr_t)0x7418 = info->ap_stack;
        *(volatile uint64_t *)(uintptr_t)0x7428 = info->ap_entry;
        *(volatile uint64_t *)(uintptr_t)0x7430 = (uint64_t)(uintptr_t)info;

        // INIT
        lapic_write(0x310, id << 24);
        lapic_write(0x300, 0x4500);
        for (int t = 0; t < 100000; t++) __asm__ volatile("pause");
        // SIPI vector 0x07 -> 0x7000
        lapic_write(0x310, id << 24);
        lapic_write(0x300, 0x4600 | 0x07);
        for (int t = 0; t < 10000; t++) __asm__ volatile("pause");
        lapic_write(0x310, id << 24);
        lapic_write(0x300, 0x4600 | 0x07);
        for (int t = 0; t < 10000; t++) __asm__ volatile("pause");

        // wait for AP
        int waited = 0;
        while (waited < 5000) {
            if (__atomic_load_n(&info->ap_ready, __ATOMIC_ACQUIRE)) break;
            for (int t = 0; t < 1000; t++) __asm__ volatile("pause");
            waited++;
        }
        if (__atomic_load_n(&info->ap_ready, __ATOMIC_ACQUIRE)) {
            acked++;
            console_puts("[ap] core ");
            console_hex64(i);
            console_puts(" (apic ");
            console_hex64(id);
            console_puts(") booted\n");
        } else {
            console_puts("[ap] core ");
            console_hex64(i);
            console_puts(" (apic ");
            console_hex64(id);
            console_puts(") timeout\n");
        }
    }
    return acked;
}

extern "C" void ap_entry_c(struct ap_boot_info *info) {
    sched_ap_boot(info);
}
