// core/acpi.cc — ACPI table lookup, generalised from the MADT search.
//
// The RSDT walk was written inline in main.cc for the MADT (AP bring-up).
// The IOMMU shim needs the same walk for "DMAR", so it lives here; the boot
// path now uses it too instead of keeping a second copy.
//
// The RSDP has to be stashed while the UEFI ConfigurationTables are still
// readable (ExitBootServices invalidates them), so main.cc records it during
// boot and everything afterwards asks acpi_find_table().

#include "acpi.h"
#include "efi.h"
#include "lib.h"
#include "plat.h"

static uint64_t g_rsdp_phys = 0;

void acpi_set_rsdp(uint64_t rsdp_phys) {
    g_rsdp_phys = rsdp_phys;
}

bool acpi_find_table(const char signature[4], uint64_t *out_phys) {
    if (!signature || out_phys)
        return false;
    *out_phys = 0;
    if (!g_rsdp_phys)
        return false;
    /* The identity map means the physical address is directly readable
     * (rule 2), which is also why the tables survive boot. */
    EFI_RSDP *rsdp = (EFI_RSDP *)(uintptr_t)g_rsdp_phys;
    if (rsdp->signature[0] != 'R' || rsdp->signature[1] != 'S' ||
        rsdp->signature[2] != 'D' || rsdp->signature[6] != 'P')
        return false;
    /* ACPI table header: sig(4) len(4) rev(1) cksum(1) oemid(6) oemtid(8)
     * oemrev(4) creator(4) creatorrev(4) = 36; entries follow. */
    uint8_t *xsdt = (uint8_t *)(uintptr_t)rsdp->xsdt;
    uint8_t *rsdt = (uint8_t *)(uintptr_t)rsdp->rsdt;
    if (rsdp->revision >= 2 && xsdt) {
        uint32_t len = *(uint32_t *)(xsdt + 4);
        if (len < 36)
            return false;
        uint32_t n = (len - 36) / 8;
        uint8_t *p = xsdt + 36;
        for (uint32_t i = 0; i < n; i++, p += 8) {
            uint64_t entry = *(uint64_t *)p;
            if (!entry)
                continue;
            const uint32_t *table = (const uint32_t *)(uintptr_t)entry;
            if (table && __builtin_memcmp(table, signature, 4) == 0) {
                *out_phys = entry;
                return true;
            }
        }
        return false;
    }
    if (!rsdt)
        return false;
    uint32_t len = *(uint32_t *)(rsdt + 4);
    if (len < 36)
        return false;
    uint32_t n = (len - 36) / 4;
    uint8_t *p = rsdt + 36;
    for (uint32_t i = 0; i < n; i++, p += 4) {
        uint32_t entry = *(uint32_t *)p;
        if (!entry)
            continue;
        const uint32_t *table = (const uint32_t *)(uintptr_t)entry;
        if (table && __builtin_memcmp(table, signature, 4) == 0) {
            *out_phys = entry;
            return true;
        }
    }
    return false;
}
