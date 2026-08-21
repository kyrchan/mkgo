#ifndef SCHED_H
#define SCHED_H
#include <stdint.h>

/* Session scheduler: a session is one wasm instance running to completion
 * (cooperative round-robin; single-session suffices for Phase 3, the
 * array form lets Phase 4 add ports without re-shaping the API). */

void sched_init(void);
int  sched_spawn(const uint8_t *blob, uint64_t len,
                 const char *const *argv, int argc);
void sched_run(void); /* runs all sessions to exit; returns when none left */
uint32_t sched_live(void);

#endif
