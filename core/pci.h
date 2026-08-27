#ifndef PCI_H
#define PCI_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// PCI config access (arch via 0xCF8/0xCFC)
int32_t pci_read32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset);
int32_t pci_write32(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t offset, uint32_t val);

// BAR helpers
int pci_bar_info(uint32_t bus, uint32_t dev, uint32_t fn, uint32_t bar, uint64_t *out_phys, uint64_t *out_size, bool *out_is_mem);
int pci_enable_busmaster(uint32_t bus, uint32_t dev, uint32_t fn);
int pci_flr(uint32_t bus, uint32_t dev, uint32_t fn);

// Enumeration
struct pci_dev {
    uint8_t bus, dev, fn;
    uint16_t vendor, device;
    uint8_t class_code, subclass, prog_if;
    uint32_t bar[6];
};
int pci_enumerate(struct pci_dev *out, int max, int *count);

#ifdef __cplusplus
}
#endif
#endif
