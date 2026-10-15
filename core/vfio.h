#ifndef VFIO_H
#define VFIO_H
#include <stdint.h>

struct boot_info;

#ifdef __cplusplus
extern "C" {
#endif

// VFIO foundation (abi/ABI.md §12) — PCI BAR mapping + IRQ doorbell
void vfio_init(const struct boot_info *bi);

// Doorbell
int vfio_bind_irq(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t type); // returns handle or -1
int vfio_doorbell_wait(uint32_t sid, uint32_t handle, uint32_t timeout_ms); // 0 fired, 1 timeout, -1 err

// BAR mapping — returns window offset or -1, updates guest memory if needed
int64_t vfio_map_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar);
int vfio_unmap_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar);

// Framebuffer (§13)
int vfio_fb_set_mode(uint32_t sid, uint32_t w, uint32_t h, uint32_t bpp);
int vfio_fb_set_cursor(uint32_t sid, uint32_t x, uint32_t y);
int vfio_fb_present(uint32_t sid); // copy guest FB window to physical LFB

// Devman class 10 enumeration helper
int vfio_dev_count(void);
struct vfio_pci_info { uint8_t bus, dev, fn; uint16_t vendor, device; };
int vfio_enumerate(struct vfio_pci_info *out, int max);

// Assignment (registry op7)
int vfio_assign_pci(uint32_t target_sid, uint8_t bus, uint8_t dev, uint8_t fn, uint32_t caller_sid);

// IOMMU enforcement: check if session may DMA to a physical range
bool vfio_iommu_permits(uint32_t sid, uint64_t phys, uint32_t size);
/* Mediated BAR access (ABI v2.5). The guest cannot hold a pointer to
 * device MMIO under rule 2, so register reads and writes are forwarded
 * here; every access is checked against the session's assignment and its
 * IOMMU domain. read returns -1 on denial, otherwise the value. */
int64_t vfio_bar_read(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                      uint32_t bar, uint32_t off, uint32_t width);
int32_t vfio_bar_write(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                       uint32_t bar, uint32_t off, uint32_t width, uint32_t value);
/* Device-visible memory (ABI v2.5). Maps [guest_off, guest_off+size) of the
 * session's linear memory into the device's IOMMU domain, at the guest's own
 * address so the guest needs no new addressing. Returns 0, or -1 when
 * refused: no IOMMU on this platform, no capability, the device is not
 * assigned to the session, the range is outside session memory, or the
 * domain is full. Never returns a buffer it cannot contain. */
int vfio_dma_map(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                 uint64_t guest_off, uint32_t size);
int vfio_dma_unmap(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn,
                   uint64_t guest_off, uint32_t size);

// Check that a PCI BDF is assigned to the calling session (or caller is admin).
bool vfio_bdf_assigned_to(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn);

// Session cleanup: release all VFIO resources owned by a session
void vfio_session_cleanup(uint32_t sid);

// FLR recovery: unmap/unbind a device and re-issue FLR
int vfio_recover_after_flr(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn);

// IOMMU security boundary
bool vfio_iommu_permits(uint32_t sid, uint64_t phys, uint32_t size);

// FLR recovery: re-bind after GPU reset
int vfio_recover_after_flr(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn);

// Framebuffer console: render text to the GOP scanout (mirrors serial).
void fb_console_puts(const char *s);
void term_init(void);
bool fb_console_active(void);

// Session cleanup: release all VFIO resources owned by a session
void vfio_session_cleanup(uint32_t sid);

#ifdef __cplusplus
}
#endif
#endif
