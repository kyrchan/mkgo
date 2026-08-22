#ifndef SCHED_H
#define SCHED_H
#include <stdint.h>

/* Cooperative round-robin session scheduler (binding policy: RR forever,
 * <=400 LOC budget incl. queues/timer hookup later).
 *
 * Each session runs on its own native stack; kern_port/sched_yield raw
 * imports switch back to the boot stack (arch/x86_64/ctx.S). Sessions are
 * addressed by sid (index); sid 0 is the kernel itself. */

#define SCHED_CAP_KILL   (1ULL << 0)
#define SCHED_CAP_DEVMAN (1ULL << 1)
#define SCHED_CAP_POWER  (1ULL << 2)
#define SCHED_CAP_FOCUS  (1ULL << 3)
#define SCHED_CAP_FSADM  (1ULL << 4)
#define SCHED_CAP_NETADM (1ULL << 5)
#define SCHED_CAP_SPAWN  (1ULL << 6)

/* per-session WASI state (args, exit) so concurrent sessions don't share */
struct sched_wasi_state {
    const char *argv[16];
    bool exited;
    int exit_code;
};
struct sched_wasi_state *sched_wasi_current(void);

void sched_init(void);

/* Parse+load+link now (kernel stack), run later. name copied (<=15 chars).
 * argv0 = session name; argv may be null. Returns sid or -1. */
int sched_spawn_named(const char *name, const uint8_t *blob, uint64_t len,
                      uint32_t uid, uint64_t capmask);

/* Enter the round-robin loop; returns when every session is dead. */
void sched_run(void);

/* --- called from session stacks (raw imports) --- */
void sched_yield_current(void);
void sched_exit_current(int rc);

/* --- kernel service plumbing --- */
uint32_t sched_current_sid(void);
uint64_t sched_capmask_of(uint32_t sid);
uint32_t sched_uid_of(uint32_t sid);
const char *sched_name_of(uint32_t sid);
bool sched_alive(uint32_t sid);
uint32_t sched_count(void);
/* fills up to max records; returns count written */
uint32_t sched_list(uint32_t *out /* sid,uid,state triples interleaved */,
                    char names[][16], uint32_t max);
int sched_kill(uint32_t sid);
/* SPAWN from preloaded image table; returns sid or negative errno-style */
int sched_spawn_image(const char *name, uint32_t uid, uint64_t capmask,
                      const char *modname);
void sched_preload_image(const char *name, const uint8_t *blob, uint64_t len);

#endif
