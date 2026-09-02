#ifndef SCHED_H
#define SCHED_H
#include <stdint.h>
#include "mp.h"
#include "engine.h"

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
#define SCHED_CAP_CONF   (1ULL << 7)
#define SCHED_CAP_PCI    (1ULL << 8)
#define SCHED_CAP_FB     (1ULL << 9)

constexpr int MAX_SESSIONS = 12;
constexpr int MAX_IMAGES = 8;

/* per-session WASI state (args, exit, routed-fd table) */
#define SCHED_MAX_FDS 64
#define SCHED_FD_EMPTY 0xFFFFFFFFu
struct sched_wasi_state {
    const char *argv[16];
    bool exited;
    int exit_code;
    uint32_t fds[SCHED_MAX_FDS]; /* fd -> fs file handle (SCHED_FD_EMPTY) */};

/* Preloaded boot image (name + wasm blob). */
struct image {
    bool used;
    char name[16];
    const uint8_t *blob;
    uint64_t len;
};

/* Session state. The `eng` field holds the wasm3 runtime; the `wctx`
 * field holds the per-session WASI state. */
struct session {
    uint32_t sid;
    uint32_t uid;
    uint64_t capmask;
    char name[16];
    int state;
    struct engine eng;
    bool eng_live;
    int exit_code;
    uint8_t *stack;
    uint64_t *sp;
    sched_wasi_state wctx;
};

/* ---- Phase 8.2: per-CPU scheduler state (planned, not implemented) ----
 *
 * Each AP core runs its own cooperative-under-interrupt scheduler over
 * its own session pool. No session migrates between cores. The
 * spinlock API (core/arch_lock.h, commits f20ed90 + d873672) makes
 * cross-core shared state (port ring, mm pool) safe.
 *
 * Why this does NOT require a true preemptive context switch: the
 * wasm3 interpreter is a virtual machine whose internal state (_sp,
 * _mem, metacode PC) is opaque C locals in m3_exec.c. The kernel
 * cannot save/resume it mid-op without patching wasm3 (violates the
 * "vendor wasm3, don't clean-room it" principle) or corrupting its
 * state. The Go runtime IS the preemption mechanism: Go 1.14+ yields
 * cooperatively in wasm at every goroutine switch point, and our
 * kernel switches sessions at those yield points. Multiple cores
 * provide the parallelism, the Go runtime provides the per-core
 * preemption -- neither requires touching the opaque interpreter
 * state. */

#define MAX_CPUS 4

struct sched_state {
    session sessions[MAX_SESSIONS];  /* per-core session pool */
    image images[MAX_IMAGES];         /* preloaded images */
    uint32_t next_rr;
    session *cur;                     /* running on ITS stack right now */
    uint64_t *kern_sp;                /* scheduler/boot stack */
    uint32_t preempt_pending;         /* pending sid for this core */
    uint32_t cpu_id;                  /* 0 = BSP, 1..N-1 = APs */
    int ap_ready;                     /* 1 once this core is up */
};

/* Returns the per-CPU scheduler state for the current core. On x86-64
 * the GS base is the per-CPU pointer (set by the trampoline); on the
 * host it's just g_cpu[0]. */
struct sched_state *sched_current_cpu(void);

/* Enter the round-robin loop on this core. Returns when every session
 * in this core's pool is dead. */
void sched_run_ap(void);

/* Called by the AP trampoline (mp.S) after the AP has entered long mode
 * and set up its own CR3. Sets up per-CPU state and enters the
 * scheduler. */
#ifdef __cplusplus
extern "C" {
#endif
void sched_ap_boot(struct ap_boot_info *info);
#ifdef __cplusplus
}
#endif

/* ---- kernel service plumbing ---- */
#ifdef __cplusplus
extern "C" {
#endif
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
/* SPAWN from preloaded image table; returns sid or negative errno-style.
 * argv/argc (may be NULL/0) are passed to the spawned session. */
int sched_spawn_image(const char *name, uint32_t uid, uint64_t capmask,
                      const char *modname, const char *const *argv, int argc);
void sched_preload_image(const char *name, const uint8_t *blob, uint64_t len);
int  sched_session_by_name(const char *name); /* sid | -1 */
void *sched_runtime_of(uint32_t sid);
void sched_set_identity(uint32_t sid, uint32_t uid, uint64_t capmask);
bool sched_is_login(uint32_t sid); /* does sid own the "login" port? */
#ifdef __cplusplus
}
#endif

void sched_init(void);

/* Parse+load+link now (kernel stack), run later. name copied (<=15 chars).
 * argv0 = session name; argv may be null. Returns sid or -1. */
int sched_spawn_named(const char *name, const uint8_t *blob, uint64_t len,
                      uint32_t uid, uint64_t capmask);
int sched_spawn_named_argv(const char *name, const uint8_t *blob,
                           uint64_t len, uint32_t uid, uint64_t capmask,
                           const char *const *argv, int argc);

/* Enter the round-robin loop; returns when every session is dead. */
void sched_run(void);

/* --- called from session stacks (raw imports) --- */
void sched_yield_current(void);
void sched_exit_current(int rc);

sched_wasi_state *sched_wasi_current(void);

#endif /* SCHED_H */
