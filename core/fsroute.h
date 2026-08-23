/* Kernel-routed preview1 path ops wait for fs.wasm replies without
 * re-entering the fs runtime (which stays suspended mid-yield on its own
 * stack). Requests go out as normal §1 datagrams; replies come back
 * addressed to the CALLER'S session-name queue and are intercepted here
 * by seq. The waiting raw import alternates sched_yield with checks. */
#ifndef FSROUTE_H
#define FSROUTE_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void fsroute_init(void);

/* Register expectation; returns 0 ok. */
int  fsroute_expect(uint16_t seq, const char *session_name);
int  fsroute_pending_for(const char *session_name);
/* Feed a possibly-matching reply (called from ports_enqueue_by_name). */
void fsroute_feed(const char *name, const uint8_t *data, uint32_t len);
/* Block (yielding) until done; fills resp. Returns reply len or -1. */
int  fsroute_wait(uint16_t seq, uint8_t *resp, uint32_t cap);

#ifdef __cplusplus
}
#endif

#endif
