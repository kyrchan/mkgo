#ifndef KERNEL_ARCH_IOMMU_H
#define KERNEL_ARCH_IOMMU_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Device-visible memory: the arch-blind interface (see core/arch_lock.h for
 * the same pattern), implemented per target in arch/<target>/iommu.cc.
 *
 * The model, deliberately the same on every target even though the register
 * sequences are not:
 *
 *   1. does this platform have a hardware IOMMU at all?
 *   2. create a domain (one per session that owns devices)
 *   3. attach a device to a domain
 *   4. map / unmap one frame: the device-visible address and the frame the
 *      CPU accesses
 *   5. make the CPU and the device agree (invalidate caches/TLB)
 *
 * Under the kernel's flat identity map (rule 2) the device-visible address
 * *is* the guest's own physical address: the guest keeps programming the
 * address it already has, and the IOMMU's job is to make exactly those
 * frames reachable for the device and nothing else. That is why DMA needs no
 * new guest addressing and no per-session page tables.
 *
 * Porting note: on x86-64 this is VT-d/AMD-Vi (extended page tables, BDF as
 * the requester id, invalidation through the device or MFENCE/WBINVD); on
 * arm64 it is SMMUv3 (stage-2 descriptors, a stream-id world instead of
 * BDFs, TLBI+DSB) plus an ITS for MSI-X/LPIs. The calls are isomorphic,
 * the registers are not, which is why this is an arch shim.
 */

typedef enum {
    IOMMU_ABSENT = 0,    /* no hardware IOMMU: refuse device-visible memory */
    IOMMU_PRESENT = 1,
} iommu_state;

/* True only if a hardware IOMMU exists AND this build can program it.
 * A platform that has an IOMMU but no implementation must report ABSENT
 * rather than hand out frames it cannot contain. */
iommu_state arch_iommu_present(void);

/* Domains are keyed by an opaque id the caller allocates. */
int arch_iommu_domain_create(uint32_t domain_id);
int arch_iommu_domain_destroy(uint32_t domain_id);
int arch_iommu_attach(uint32_t domain_id, uint8_t bus, uint8_t dev, uint8_t fn);
int arch_iommu_detach(uint32_t domain_id, uint8_t bus, uint8_t dev, uint8_t fn);
int arch_iommu_map(uint32_t domain_id, uint64_t dev_addr, uint64_t phys);
int arch_iommu_unmap(uint32_t domain_id, uint64_t dev_addr);
int arch_iommu_invalidate(uint32_t domain_id);

#ifdef __cplusplus
}
#endif

#endif
