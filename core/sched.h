#ifndef SCHED_H
#define SCHED_H
#include <stdint.h>
#include "mp.h"
#include "engine.h"

/* Round-robin session scheduler. Each core owns its scheduler state and
 * session pool; sessions do not migrate between cores. Timer and remote
 * wake requests are handed to the owned engine at explicit continuation
 * points, without requiring guest sched_yield. Each session runs on its own
 * native stack and is addressed by sid; sid 0 is the kernel itself. */

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
#define SCHED_CAP_DOORBELL (1ULL << 10)
#define SCHED_CAP_VMWARE   (1ULL << 11)
#define SCHED_CAP_PORTBIND (1ULL << 12)
#define SCHED_CAP_ALL    ((1ULL << 13) - 1)

constexpr int MAX_SESSIONS = 12;
constexpr int MAX_IMAGES = 8;

/* Session execution mode (session.exec_mode). ABI-frozen pair with
 * JIT_EXEC_* in core/jit.h — keep the values in sync. */
constexpr uint8_t SCHED_EXEC_INTERP = 0;
constexpr uint8_t SCHED_EXEC_NATIVE = 1;

/* per-session WASI state (args, exit, routed-fd table) */
#define SCHED_MAX_FDS 64
#define SCHED_FD_EMPTY 0xFFFFFFFFu
/* F-AUDIT-2: per-session routed-FS buffers; previously file-scope statics
 * in wasi_glue.cc, which raced across concurrent sessions because
 * fs_roundtrip yields (fsroute_wait -> sched_yield_current). */
#define SCHED_FSREQ_MAX 16384
#define SCHED_FSRESP_MAX 8192
struct sched_wasi_state {
    const char *argv[16];
    bool exited;
    int exit_code;
    uint32_t fds[SCHED_MAX_FDS]; /* fd -> fs file handle (SCHED_FD_EMPTY) */
    uint8_t fsreq[SCHED_FSREQ_MAX];
    uint8_t fsresp[SCHED_FSRESP_MAX];
    uint16_t fs_seq_ctr;   /* per-session outgoing seq counter */
    uint16_t fs_rt_seq;    /* per-session roundtrip seq (stored in req[2:3]) */
};

/* Preloaded boot image (name + wasm blob). */
struct image {
    bool used;
    char name[16];
    const uint8_t *blob;
    uint64_t len;
};

/* Session state. The `eng` field holds the bootstrap wasm3 runtime until
 * the owned-engine cutover; `wctx` holds per-session WASI state. */
struct session {
     uint32_t sid;
     uint32_t uid;
     uint64_t capmask;
     uint8_t cap_source; /* 0=login-issued, 1=chcaps, 2=init-issued */
     uint8_t exec_mode;  /* 0=bootstrap wasm3, 1=NATIVE (JIT cache).
                           * Zero-init defaults to INTERP. Values match
                           * JIT_EXEC_* in core/jit.h (ABI-frozen pair). */
     /* NATIVE binding (Stage 3b-i): pool-resident image + entry func.
      * Valid only when exec_mode == 1 (set together by
      * sched_bind_native). Pool memory never moves, so raw pointers
      * are stable for the session lifetime. */
     const uint8_t *native_code;
     uint64_t native_len;
     const uint32_t *native_offs;
     uint32_t native_nfuncs;
     uint32_t native_entry; /* func index to invoke (v1: func 0) */
     char name[15]; /* room for NUL */
     int state;
     struct engine eng;
     bool eng_live;
     int exit_code;
     uint8_t *stack;
     uint64_t *sp;
     sched_wasi_state wctx;
     /* Phase 8 preemption: GPR save area for deferred IRQ return.
      * When preempt_pending is set for this session, the IRQ stub
      * saves the interrupted GPRs here instead of on the session
      * stack. On resume, the GPRs are restored from here and the
      * session iretqs back to the guest at the interrupted point. */
     uint64_t gpr_save[16]; /* [0]=rax [1]=rbx [2]=rcx [3]=rdx
                                    [4]=rsi [5]=rdi [6]=rbp [7]=rsp
                                    [8..15]=r8..r15 */
     bool preempt_saved;    /* true if gpr_save holds a valid save */
 };

/* Phase 8.2 per-CPU scheduler state. Each AP owns its scheduler/run state;
 * no session migrates between cores. The spinlock API protects shared
 * substrate state, while execution ownership and timer/remote requests
 * remain per-CPU. The current wasm3 path is bootstrap evidence only; the
 * production path uses the project-owned engine and its explicit
 * continuation points. */

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
int sched_ncpus(void); /* cores booted (Phase 15 SYSSTAT) */
uint64_t sched_capmask_of(uint32_t sid);
uint32_t sched_uid_of(uint32_t sid);
const char *sched_name_of(uint32_t sid);
bool sched_alive(uint32_t sid);
uint32_t sched_count(void);
/* fills up to max records; returns count written */
uint32_t sched_list(uint32_t *out /* sid,uid,state triples interleaved */,
                    char names[][16], uint32_t max);
int sched_kill(uint32_t sid, uint32_t from_sid);
/* SPAWN from preloaded image table; returns sid or negative errno-style.
 * argv/argc (may be NULL/0) are passed to the spawned session. */
int sched_spawn_image(const char *name, uint32_t uid, uint64_t capmask,
                      const char *modname, const char *const *argv, int argc);
void sched_preload_image(const char *name, const uint8_t *blob, uint64_t len);
int  sched_session_by_name(const char *name); /* sid | -1 */
void sched_request_preempt(uint32_t sid);
/* Session-scoped guest linear memory. Device windows (ABI.md 6 net RX/TX,
 * VFIO BARs) must go through these rather than caching a pointer: growing a
 * session's memory can relocate the buffer. */
uint8_t *sched_session_memory(uint32_t sid, uint32_t *size);
int sched_session_ensure_bytes(uint32_t sid, uint32_t min_bytes);
void sched_set_identity(uint32_t sid, uint32_t uid, uint64_t capmask);
int  sched_set_capmask(uint32_t sid, uint64_t clear, uint64_t set, uint8_t source);
/* cap_source values */
constexpr uint8_t CAP_SRC_LOGIN = 0;
constexpr uint8_t CAP_SRC_CHCAPS = 1;
constexpr uint8_t CAP_SRC_INIT = 2;
bool sched_is_login(uint32_t sid); /* does sid own the "login" port? */
bool sched_is_init(uint32_t sid);  /* does sid == "init"? */
uint8_t sched_cap_source(uint32_t sid); /* 0=login, 1=chcaps, 2=init */
/* Bind a verified JIT image to a live session for NATIVE execution
 * (Stage 3b-i). Copies the image descriptor (pool memory is stable);
 * sets exec_mode=1 and the entry func. Returns 0 ok, -1 bad sid or
 * empty image. Pure-code images only in v1 (no import calls). */
int sched_bind_native(uint32_t sid, const uint8_t *code, uint64_t code_len,
                      const uint32_t *offs, uint32_t nfuncs, uint32_t entry);
/* Spawn a NATIVE session directly (no wasm3 engine): for payloads whose
 * bytes hash-match a verified cache image. Returns sid or -1. */
int sched_spawn_native(const char *name, uint32_t uid, uint64_t capmask,
                       const uint8_t *code, uint64_t code_len,
                       const uint32_t *offs, uint32_t nfuncs, uint32_t entry);
#ifdef __cplusplus
}
#endif

void sched_init(void);
void sched_set_owned_default(bool enabled);

/* Parse+load+link now (kernel stack), run later. name copied (<=15 chars).
 * argv0 = session name; argv may be null. Returns sid or -1. */
int sched_spawn_named(const char *name, const uint8_t *blob, uint64_t len,
                      uint32_t uid, uint64_t capmask);
int sched_spawn_named_argv(const char *name, const uint8_t *blob,
                           uint64_t len, uint32_t uid, uint64_t capmask,
                           const char *const *argv, int argc);
int sched_spawn_owned_named(const char *name, const uint8_t *blob, uint64_t len,
                            uint32_t uid, uint64_t capmask);
int sched_spawn_owned_named_argv(const char *name, const uint8_t *blob,
                                 uint64_t len, uint32_t uid, uint64_t capmask,
                                 const char *const *argv, int argc);
/* Attach a verified+bound native image to a live session's engine
 * (per-function dispatch). Must run before first execution. */
int sched_bind_engine_image(uint32_t sid, const struct jit_image *jit);

/* Enter the round-robin loop; returns when every session is dead. */
void sched_run(void);

/* Knob store (registry ops 11/12). Fixed 8 entries.
 * 0=quantum_us, 1=log_level, 2=audit_mask, 3..7 reserved. */
constexpr int KNOB_COUNT = 8;
constexpr int KNOB_QUANTUM_US = 0;
constexpr int KNOB_LOG_LEVEL = 1;
constexpr int KNOB_AUDIT_MASK = 2;

int  sched_knob_set(uint8_t idx, uint64_t val);
uint64_t sched_knob_get(uint8_t idx);
void sched_knobs_init(void);

/* --- called from session stacks (raw imports) --- */
void sched_yield_current(void);
void sched_exit_current(int rc);

sched_wasi_state *sched_wasi_current(void);

#endif /* SCHED_H */
