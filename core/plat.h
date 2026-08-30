#ifndef CORE_PLAT_H
#define CORE_PLAT_H
#include <stdint.h>

/* The entire arch contract. Implemented per-target under arch/<target>/;
 * core/ carries zero #ifdef, zero inline asm, zero direct HW access
 * (AGENTS.md hard rule 1). extern "C": wasm3 (plain C) links these too. */
#ifdef __cplusplus
extern "C" {
#endif

/* console (uart) */
void console_init(void);
void console_putc(char c);
void console_puts(const char *s);
void console_hex64(uint64_t v);
int  console_rx_ready(void); /* 1 = byte pending on serial */
int  console_rx_byte(void); /* blocking only if rx_ready said so */
void pic_remap(void);
void pit_init(uint32_t hz);
void irq0_eoi(void);
void sti_impl(void);
void irq0_stub(void);
void virtio_blk_init(void);
int virtio_blk_rw(int write, uint64_t lba, void *buf, uint32_t count);
int virtio_blk_available(void);
void virtio_net_init(void);
void virtio_net_poll(void);
int virtio_net_available(void);

/* VMware backdoor (Phase 13, optional — I/O port 0x5658 RPC) */
int vmware_backdoor_present(void);
void vmware_backdoor_get_time(uint32_t *low, uint32_t *high);
void vmware_backdoor_get_uuid(uint8_t *out16);
void vmware_backdoor_log(const uint8_t *msg, uint32_t len);

/* cpu */
void cpu_dump_features(void);
int  cpu_enable_vector(void); /* 0 = vector unit live */
void cpu_halt(void);
uint64_t cpu_cycles(void); /* monotonically increasing cycle counter */

/* machine bring-up */
void gdt_install(void);
void idt_install(void);
void paging_identity_init(void);
uint64_t paging_pml4_pa(void);

/* timer */
uint64_t timer_calibrate_tsc_khz(void);

#ifdef __cplusplus
}
#endif

#endif
