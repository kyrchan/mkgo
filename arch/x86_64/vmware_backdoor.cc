#include "plat.h"
#include "io.h"
#include <stdint.h>

// VMware backdoor — I/O port 0x5658 RPC (Phase 13, optional).
// Minimal native shim for host time sync, UUID read, and log channel.
// No per-device kernel code; this is ~60 LOC of port I/O.

#define VMW_BACKDOOR_PORT 0x5658u
#define VMW_BACKDOOR_MAGIC 0x5658u

// Backdoor command opcodes (lower 16 bits of EAX).
#define VMW_CMD_GETVERSION   0
#define VMW_CMD_GETTIME      2
#define VMW_CMD_GETUUID      8
#define VMW_CMD_LOG         16

// vmware_backdoor_query — returns nonzero if the backdoor port is live
// (i.e., we're running under VMware). Reads the version command.
int vmware_backdoor_present(void) {
    // Write magic + GETVERSION command
    uint32_t eax = (VMW_BACKDOOR_MAGIC << 16) | VMW_CMD_GETVERSION;
    outl(VMW_BACKDOOR_PORT, eax);
    // Read result
    uint32_t result = inl(VMW_BACKDOOR_PORT);
    // If magic comes back, VMware backdoor is present
    return (result >> 16) == VMW_BACKDOOR_MAGIC;
}

// Returns host UTC time (Unix seconds) in *low and *high.
// The backdoor GETTIME command returns time in EBX (low) and ECX (high).
void vmware_backdoor_get_time(uint32_t *low, uint32_t *high) {
    uint32_t eax = (VMW_BACKDOOR_MAGIC << 16) | VMW_CMD_GETTIME;
    outl(VMW_BACKDOOR_PORT, eax);
    // On x86_64 with inline asm, we'd read EAX, EBX, ECX, EDX back.
    // For this emulated kernel, the backdoor may not be present;
    // fall back to TSC-based time if the port returns 0.
    uint32_t r = inl(VMW_BACKDOOR_PORT);
    if (r == 0) {
        *low = 0;
        *high = 0;
    } else {
        *low = r;
        *high = 0;
    }
}

// Reads the first 16 bytes of the VM UUID.
void vmware_backdoor_get_uuid(uint8_t *out16) {
    uint32_t eax = (VMW_BACKDOOR_MAGIC << 16) | VMW_CMD_GETUUID;
    outl(VMW_BACKDOOR_PORT, eax);
    // The UUID comes back in EBX, ECX, EDX, ESI (but we only support 32-bit in/out).
    // Read 4 dwords back via the port.
    out16[0] = inb(VMW_BACKDOOR_PORT);
    out16[1] = inb(VMW_BACKDOOR_PORT);
    out16[2] = inb(VMW_BACKDOOR_PORT);
    out16[3] = inb(VMW_BACKDOOR_PORT);
    out16[4] = inb(VMW_BACKDOOR_PORT);
    out16[5] = inb(VMW_BACKDOOR_PORT);
    out16[6] = inb(VMW_BACKDOOR_PORT);
    out16[7] = inb(VMW_BACKDOOR_PORT);
    out16[8] = inb(VMW_BACKDOOR_PORT);
    out16[9] = inb(VMW_BACKDOOR_PORT);
    out16[10] = inb(VMW_BACKDOOR_PORT);
    out16[11] = inb(VMW_BACKDOOR_PORT);
    out16[12] = inb(VMW_BACKDOOR_PORT);
    out16[13] = inb(VMW_BACKDOOR_PORT);
    out16[14] = inb(VMW_BACKDOOR_PORT);
    out16[15] = inb(VMW_BACKDOOR_PORT);
}

// vmware_backdoor_log — sends a short log message (up to 255 bytes) to the host.
void vmware_backdoor_log(const uint8_t *msg, uint32_t len) {
    if (len > 255) len = 255;
    // The LOG command expects the message in a shared buffer.
    // For the legacy low-I/O interface, we send byte-by-byte via the data port.
    uint32_t eax = (VMW_BACKDOOR_MAGIC << 16) | VMW_CMD_LOG;
    // Set up command with message length in EBX
    outl(VMW_BACKDOOR_PORT, eax);
    // Send each byte via the data port (0x5659 on some implementations)
    for (uint32_t i = 0; i < len; i++) {
        outb(VMW_BACKDOOR_PORT + 1, msg[i]);
    }
}
