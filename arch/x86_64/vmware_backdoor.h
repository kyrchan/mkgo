#ifndef VMWARE_BACKDOOR_H
#define VMWARE_BACKDOOR_H
#include <stdint.h>

// VMware backdoor — I/O port 0x5658 RPC shim.
// These compile to inline-asm port I/O in the arch layer.

int vmware_backdoor_present(void);
void vmware_backdoor_get_time(uint32_t *low, uint32_t *high);
void vmware_backdoor_get_uuid(uint8_t *out16);
void vmware_backdoor_log(const uint8_t *msg, uint32_t len);

#endif
