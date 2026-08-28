// Host-build stubs for vfio functions referenced by kernsvc.cc.
// The full vfio.cc/pci.cc are not linked into the host test harness.
#include "vfio.h"
#include "pci.h"

extern "C" int vfio_assign_pci(uint32_t target_sid, uint8_t bus, uint8_t dev, uint8_t fn, uint32_t caller_sid) {
    (void)target_sid; (void)bus; (void)dev; (void)fn; (void)caller_sid;
    return -1;
}

extern "C" int vfio_enumerate(struct vfio_pci_info *out, int max) {
    (void)out; (void)max;
    return 0;
}
