#ifndef PORTS_H
#define PORTS_H
#include <stdint.h>

/* Message ports per abi/ABI.md §1: datagrams, kernel-mediated copy,
 * max 4096 B payload, recv never blocks. Handles are per-session.
 * Kernel-owned well-known endpoints ("registry"/"devman"/"power") are
 * dispatched inline at send time; replies land on the sending port
 * queue with matching seq (ABI §7). */

#ifdef __cplusplus
extern "C" {
#endif

void ports_init(void);

/* called from raw imports with the current session's sid */
int  port_create(uint32_t sid, const char *name, uint32_t name_len); /* h | -1 */
int  port_bind(uint32_t sid, const char *name, uint32_t name_len);   /* h | -1 */
bool is_well_known_name(const char *name, uint32_t name_len);
int  port_send(uint32_t sid, int h, const void *data, uint32_t len); /* 0|-1|-2 */
int  port_recv(uint32_t sid, int h, void *out, uint32_t cap);        /* >0|0|-1 */
void ports_kernel_enqueue(uint32_t sid, int h, const void *data, uint32_t len);
bool ports_enqueue_by_name(const char *name, const void *data, uint32_t len);
int  ports_owner_of_handle(uint32_t sid, int h);
void ports_drain_session(uint32_t sid);
void ports_clear_session_handles(uint32_t sid);

#ifdef __cplusplus
}
#endif

#endif
