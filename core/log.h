#ifndef CORE_LOG_H
#define CORE_LOG_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* v1 syslog path (Phase 15): append-only ring of everything emitted via
 * console_putc — kernel boot trail, [audit] denials, panics, AND guest
 * fd_write bytes (wasi_glue funnels those through console_putc too).
 * Carriage returns are NOT stored (terminal control, not content).
 *
 * Exposed read-only via registry LOGDUMP (op 9); dmesg/audit are shell
 * built-ins over it. No per-arch code: the ring is core-owned, fed by a
 * one-line hook in arch/x86_64/uart.cc:console_putc. */

/* Append n bytes (usually n==1 from the putc hook). SMP-safe. */
void log_push(const char *p, uint32_t n);

/* Copy up to cap bytes starting at absolute stream offset off into dst.
 * Clamps off up to the oldest retained byte; stores the ever-growing
 * total and the clamped begin. Returns bytes copied. SMP-safe. */
uint32_t log_read(uint64_t off, uint8_t *dst, uint32_t cap,
                  uint64_t *out_total, uint64_t *out_begin);

#ifdef __cplusplus
}
#endif

#endif
