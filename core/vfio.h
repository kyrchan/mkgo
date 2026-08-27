#ifndef VFIO_H
#define VFIO_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// VFIO foundation (abi/ABI.md §12) — PCI BAR mapping + IRQ doorbell
void vfio_init(void);

// Doorbell
int vfio_bind_irq(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t type); // returns handle or -1
int vfio_doorbell_wait(uint32_t sid, uint32_t handle, uint32_t timeout_ms); // 0 fired, 1 timeout, -1 err

// BAR mapping — returns window offset or -1, updates guest memory if needed
int64_t vfio_map_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar);
int vfio_unmap_bar(uint32_t sid, uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar);

// Framebuffer (§13)
int vfio_fb_set_mode(uint32_t sid, uint32_t w, uint32_t h, uint32_t bpp);
int vfio_fb_set_cursor(uint32_t sid, uint32_t x, uint32_t y);

// Devman class 10 enumeration helper
int vfio_dev_count(void);
struct vfio_pci_info { uint8_t bus, dev, fn; uint16_t vendor, device; };
int vfio_enumerate(struct vfio_pci_info *out, int max);

// Assignment (registry op7)
int vfio_assign_pci(uint32_t target_sid, uint8_t bus, uint8_t dev, uint8_t fn, uint32_t caller_sid);

#ifdef __cplusplus
}
#endif
#endif
