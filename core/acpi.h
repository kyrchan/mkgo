#ifndef KERNEL_ACPI_H
#define KERNEL_ACPI_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Finds an ACPI table by its 4-character signature and returns its physical
 * address. The RSDT/XSDT walk was written for the MADT (AP bring-up) and is
 * generalised here so the IOMMU shim can look for "DMAR" the same way.
 * Returns false when there is no RSDP, no RSDT/XSDT, or no such table.
 *
 * Tables live in EfiACPIMemoryNVS, so they survive ExitBootServices, and
 * the identity map means the physical address is directly readable. */
bool acpi_find_table(const char signature[4], uint64_t *out_phys);

/* Boot path only: records the RSDP address while the UEFI Configuration
 * Tables are still readable, so lookups work after ExitBootServices. */
void acpi_set_rsdp(uint64_t rsdp_phys);

#ifdef __cplusplus
}
#endif

#endif
