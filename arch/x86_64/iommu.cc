// arch/x86_64/iommu.cc — VT-d/AMD-Vi side of the device-visible memory
// interface (core/arch_iommu.h).
//
// Status, stated plainly: this detects the hardware and refuses everything
// else. Mapping a frame means writing a context-table entry and a root-port
// second-level page-table entry, invalidating a device cache and (on AMD)
// doing a write-combining dance; none of that is written, and a
// half-implemented VT-d is worse than none, because it would report success
// for a mapping the device cannot use -- and on hardware that cannot be
// untested, "untested" is how DMA ends up scribbling over the kernel.
//
// So arch_iommu_present() reports PRESENT only when a DMAR table exists AND
// the programming path is compiled in (it is not, yet), and every programming
// call fails with -1. The consequence is the correct one: a guest asking for
// a transfer-ring buffer is refused, and the drivers report "dma
// unavailable" rather than pointing a device at guest RAM.

#include "arch_iommu.h"
#include "acpi.h"
#include "plat.h"

static constexpr int kMaxDomains = 16;

static bool domain_live[kMaxDomains];

extern "C" iommu_state arch_iommu_present(void) {
    uint64_t dmar = 0;
    if (!acpi_find_table("DMAR", &dmar)) {
        return IOMMU_ABSENT;
    }
    /* A DMAR table exists. Its remapping structures would have to be parsed
     * and programmed before any frame could be mapped, and that is not
     * implemented, so this platform cannot contain device DMA. */
    console_puts("[iommu] DMAR present, programming not implemented: refusing device DMA\n");
    return IOMMU_ABSENT;
}

extern "C" int arch_iommu_domain_create(uint32_t domain_id) {
    if (domain_id >= kMaxDomains || domain_live[domain_id])
        return -1;
    if (arch_iommu_present() != IOMMU_PRESENT)
        return -1;
    domain_live[domain_id] = true;
    return 0;
}

extern "C" int arch_iommu_domain_destroy(uint32_t domain_id) {
    if (domain_id >= kMaxDomains || !domain_live[domain_id])
        return -1;
    domain_live[domain_id] = false;
    return 0;
}

extern "C" int arch_iommu_attach(uint32_t domain_id, uint8_t bus, uint8_t dev, uint8_t fn) {
    (void)bus;
    (void)dev;
    (void)fn;
    return domain_id < kMaxDomains && domain_live[domain_id] ? 0 : -1;
}

extern "C" int arch_iommu_detach(uint32_t domain_id, uint8_t bus, uint8_t dev, uint8_t fn) {
    (void)bus;
    (void)dev;
    (void)fn;
    return domain_id < kMaxDomains && domain_live[domain_id] ? 0 : -1;
}

extern "C" int arch_iommu_map(uint32_t domain_id, uint64_t dev_addr, uint64_t phys) {
    (void)domain_id;
    (void)dev_addr;
    (void)phys;
    return -1; /* no second-level mapping without VT-d programming */
}

extern "C" int arch_iommu_unmap(uint32_t domain_id, uint64_t dev_addr) {
    (void)domain_id;
    (void)dev_addr;
    return -1;
}

extern "C" int arch_iommu_invalidate(uint32_t domain_id) {
    (void)domain_id;
    return -1;
}
