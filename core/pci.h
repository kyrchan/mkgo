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

// MSI-X (capability ID 0x11)
struct pci_msix {
    uint8_t cap_off;    // config offset of MSI-X capability
    uint8_t bir;        // BAR index holding the table
    uint32_t table_off; // byte offset within BAR
    uint16_t num_vecs;  // table size (N)
    uint64_t table_phys;// host physical address of table
};
int pci_msix_find(uint32_t bus, uint32_t dev, uint32_t fn, struct pci_msix *out);
int pci_msix_enable(uint32_t bus, uint32_t dev, uint32_t fn, const struct pci_msix *m, uint16_t num_vecs);
int pci_msix_set_vector(uint32_t bus, uint32_t dev, uint32_t fn, const struct pci_msix *m, uint16_t vec, uint64_t addr, uint16_t data);

// Virtual framebuffer (headless VFIO test device). BDF 0:2:0.
void pci_vfb_init(uint32_t width, uint32_t height);
bool pci_is_vfb(uint32_t bus, uint32_t dev, uint32_t fn);
// Find a real PCI display device (class 0x03, not the VFB). Returns its
// framebuffer BAR0 physical address and size. 0 = found, -1 = headless.
int pci_find_display(uint32_t *out_phys, uint32_t *out_size);

#ifdef __cplusplus
}
#endif
#endif
