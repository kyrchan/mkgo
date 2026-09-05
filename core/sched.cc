#include "sched.h"
#include "lib.h"
#include "mm.h"
#include "devblk.h"
#include "input.h"
#include "engine.h"
#include "wasi_glue.h"
#include "plat.h"
#include "ctx.h"
#include "rt.h"
#include "vfio.h"
#include "cpu.h"

static constexpr uint64_t STACK_BYTES = 1024 * 1024;

enum st { S_FREE = 0, S_RUNNABLE = 1, S_RUNNING = 2, S_ZOMBIE = 3 };

extern "C" {
bool ports_name_owned_by(uint32_t sid, const char *name);
void ports_drain_session(uint32_t sid);
void ports_clear_session_handles(uint32_t sid);
uint32_t preempt_take_pending(void);
uint8_t preempt_is_on(void);
}

static session sessions[MAX_SESSIONS];
static image images[MAX_IMAGES];
static uint32_t next_rr;
static session *cur;          /* running on ITS stack right now */
static uint64_t *kern_sp;     /* scheduler/boot stack */

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
static struct sched_state g_cpu[MAX_CPUS];
#ifdef HOST_BUILD
static __thread uint32_t g_cpu_id;          /* 0 = BSP, 1..N-1 = APs */
#else
static uint32_t g_cpu_id;          /* fallback for BSP before GS set */
#endif
static int g_n_cpus;               /* number of cores that booted */

/* Returns the per-CPU scheduler state for the current core. On x86-64
 * the GS base is the per-CPU pointer (set by sched_init/ap_boot); on the
 * host it's just g_cpu[0] or thread-local g_cpu_id. */
/* Phase 15 observability (top via registry SYSSTAT): cores booted. */
int sched_ncpus(void) { return __atomic_load_n(&g_n_cpus, __ATOMIC_RELAXED); }

struct sched_state *sched_current_cpu(void) {
#ifdef HOST_BUILD
    return &g_cpu[g_cpu_id];
#else
    uint64_t base = rd_gs_base();
    if (base) return (struct sched_state *)base;
    return &g_cpu[g_cpu_id];
#endif
}

sched_wasi_state *sched_wasi_current(void) {
    struct sched_state *st = sched_current_cpu();
    session *c = st->cur ? st->cur : cur;
    return c ? &c->wctx : 0;
}

void *sched_runtime_of(uint32_t sid) {
    if (sid >= MAX_SESSIONS || !sessions[sid].eng_live)
        return 0;
    return sessions[sid].eng.rt;
}

uint32_t sched_current_sid(void) {
    struct sched_state *st = sched_current_cpu();
    session *c = st->cur ? st->cur : cur;
    return c ? c->sid : 0;
}
uint64_t sched_capmask_of(uint32_t sid) {
    return sid < MAX_SESSIONS && sessions[sid].state != S_FREE ? sessions[sid].capmask : 0;
}
uint32_t sched_uid_of(uint32_t sid) {
    return sid < MAX_SESSIONS && sessions[sid].state != S_FREE ? sessions[sid].uid : 0;
}
const char *sched_name_of(uint32_t sid) {
    return sid < MAX_SESSIONS && sessions[sid].state != S_FREE ? sessions[sid].name : "?";
}
bool sched_alive(uint32_t sid) {
    return sid > 0 && sid < MAX_SESSIONS && sessions[sid].state != S_FREE &&
           sessions[sid].state != S_ZOMBIE;
}
uint32_t sched_count(void) {
    uint32_t n = 0;
    for (int i = 1; i < MAX_SESSIONS; i++)
        if (sessions[i].state == S_RUNNABLE || sessions[i].state == S_RUNNING)
            n++;
    return n;
}

void sched_preload_image(const char *name, const uint8_t *blob, uint64_t len) {
    for (int i = 0; i < MAX_IMAGES; i++) {
        if (images[i].used && !strcmp(images[i].name, name))
            return; /* already preloaded */
    }
    for (int i = 0; i < MAX_IMAGES; i++) {
        if (!images[i].used) {
            images[i].used = true;
            int j = 0;
            for (; name[j] && j < 15; j++)
                images[i].name[j] = name[j];
            images[i].name[j] = 0;
            images[i].blob = blob;
            images[i].len = len;
            return;
        }
    }
}

void sched_init(void) {
    for (int i = 0; i < MAX_SESSIONS; i++) {
        sessions[i].state = S_FREE;
        sessions[i].sid = (uint32_t)i;
        sessions[i].stack = 0;
        sessions[i].eng_live = false;
        sessions[i].cap_source = 2; /* init-issued default */
    }
    next_rr = 1;
    cur = 0;
    for (int i = 0; i < MAX_CPUS; i++) {
        g_cpu[i].cpu_id = i;
        g_cpu[i].next_rr = 1;
        g_cpu[i].cur = 0;
        g_cpu[i].kern_sp = 0;
        g_cpu[i].ap_ready = 0;
        for (int j = 0; j < MAX_SESSIONS; j++) {
            g_cpu[i].sessions[j].state = S_FREE;
            g_cpu[i].sessions[j].sid = j;
        }
    }
    g_cpu[0].ap_ready = 1;
    g_n_cpus = 1;
    sched_knobs_init();
#ifndef HOST_BUILD
    wr_gs_base((uint64_t)&g_cpu[0]);
#endif
}

int sched_spawn_image(const char *name, uint32_t uid, uint64_t capmask,
                      const char *modname, const char *const *argv, int argc) {
    for (int i = 0; i < MAX_IMAGES; i++) {
        if (images[i].used && !strcmp(images[i].name, modname))
            return sched_spawn_named_argv(name, images[i].blob, images[i].len,
                                          uid, capmask, argv, argc);
    }
    return -1;
}

/* runs ON THE SESSION STACK */
extern "C" void sched_session_main(void) {
    struct sched_state *st = sched_current_cpu();
    session *s = st->cur ? st->cur : cur;
    int r = engine_start(&s->eng);
    s->exit_code = s->wctx.exited ? s->wctx.exit_code : (r ? 1 : 0);
    console_puts("[sched] '");
    console_puts(s->name);
    console_puts("' exited rc=");
    console_hex64((uint64_t)s->exit_code);
    console_puts("\n");
    engine_shutdown(&s->eng);
    s->eng_live = false;
    vfio_session_cleanup(s->sid);
    s->state = S_ZOMBIE;
    uint64_t *ksp = st->kern_sp ? st->kern_sp : kern_sp;
    ctx_switch(&s->sp, ksp); /* never returns here */
    for (;;)
        ;
}

int sched_spawn_named(const char *name, const uint8_t *blob, uint64_t len,
                      uint32_t uid, uint64_t capmask) {
    const char *defargv[2] = {name, 0};
    return sched_spawn_named_argv(name, blob, len, uid, capmask, defargv, 1);
}

int sched_spawn_named_argv(const char *name, const uint8_t *blob,
                           uint64_t len, uint32_t uid, uint64_t capmask,
                           const char *const *argv, int argc) {
    for (int i = 1; i < MAX_SESSIONS; i++) {
        if (sessions[i].state != S_FREE)
            continue;
        session *s = &sessions[i];
        s->uid = uid;
        s->capmask = capmask;
        s->exit_code = -1;
        int j = 0;
        for (; name[j] && j < 15; j++)
            s->name[j] = name[j];
        s->name[j] = 0;

        int r = engine_init(&s->eng, blob, len);
        if (r != 0) {
            console_puts("[sched] init failed for '");
            console_puts(s->name);
            console_puts("'\n");
            return -1;
        }
        s->eng_live = true;
        s->stack = (uint8_t *)mm_alloc(STACK_BYTES, 16);
        if (!s->stack) {
            engine_shutdown(&s->eng);
            s->eng_live = false;
            return -1;
        }
        /* per-session WASI state: argv from caller */
        for (int k = 0; k < 16; k++)
            s->wctx.argv[k] = 0;
        int na = 0;
        if (argv) {
            for (; na < argc && na < 14; na++)
                s->wctx.argv[na] = argv[na];
        } else {
            s->wctx.argv[0] = s->name;
            na = 1;
        }
        s->wctx.exited = false;
        s->wctx.exit_code = 0;
        for (int k = 0; k < SCHED_MAX_FDS; k++)
            s->wctx.fds[k] = SCHED_FD_EMPTY;

        /* make it RUNNABLE: build initial frame entering the trampoline */
        s->sp = ctx_make(s->stack, STACK_BYTES);
        s->state = S_RUNNABLE;
        console_puts("[sched] spawned '");
        console_puts(s->name);
        console_puts("' sid=");
        console_hex64((uint64_t)i);
        console_puts("\n");
        return i;
    }
    return -1;
}

void sched_yield_current(void) {
    struct sched_state *st = sched_current_cpu();
    session *s = st->cur ? st->cur : cur;
    uint64_t *ksp = st->kern_sp ? st->kern_sp : kern_sp;
    if (!s) return;
    /* Phase 8 preemption: if the IRQ0 preemption stub flagged this
     * session as due for a switch, yield now. The IRQ itself does
     * NOT switch (it just saves state and iretqs); the switch
     * happens at the next yield point. This is the "cooperative
     * under interrupt" pattern: cheap IRQ, real switch on yield. */
    if (preempt_is_on() && preempt_take_pending() == s->sid) {
        s->state = S_RUNNABLE;
        ctx_switch(&s->sp, ksp);
        return;
    }
    s->state = S_RUNNABLE;
    ctx_switch(&s->sp, ksp);
}

void sched_exit_current(int rc) {
    (void)rc; /* exit path handled by session_main */
    struct sched_state *st = sched_current_cpu();
    session *s = st->cur ? st->cur : cur;
    uint64_t *ksp = st->kern_sp ? st->kern_sp : kern_sp;
    ctx_switch(&s->sp, ksp);
}

static void audit(const char *op, const char *reason, const char *target) {
    console_puts("[audit] sid=");
    console_hex64(cur ? cur->sid : 0);
    console_puts(" uid=");
    console_hex64(cur ? cur->uid : 0);
    console_puts(" op=");
    console_puts(op);
    console_puts(" reason=");
    console_puts(reason);
    console_puts(" target=");
    console_puts(target);
    console_puts("\n");
}

int sched_session_by_name(const char *name) {
    for (int i = 1; i < MAX_SESSIONS; i++) {
        if ((sessions[i].state == S_RUNNABLE || sessions[i].state == S_RUNNING) &&
            !strcmp(sessions[i].name, name))
            return i;
    }
    return -1;
}

void sched_set_identity(uint32_t sid, uint32_t uid, uint64_t capmask) {
    if (sid < MAX_SESSIONS && sessions[sid].state != S_FREE) {
        sessions[sid].uid = uid;
        sessions[sid].capmask = capmask;
        sessions[sid].cap_source = 0; /* LOGIN-issued */
        console_puts("[sched] identity '");
        console_puts(sessions[sid].name);
        console_puts("' uid=");
        console_hex64(uid);
        console_puts(" caps=");
        console_hex64(capmask);
        console_puts("\n");
    }
}

int sched_set_capmask(uint32_t sid, uint64_t clear, uint64_t set, uint8_t source) {
    if (sid < MAX_SESSIONS && sessions[sid].state != S_FREE) {
        sessions[sid].capmask = (sessions[sid].capmask & ~clear) | set;
        sessions[sid].cap_source = source;
        console_puts("[sched] chcaps '");
        console_puts(sessions[sid].name);
        console_puts("' caps=0x");
        console_hex64(sessions[sid].capmask);
        console_puts("\n");
        return 0;
    }
    return -1;
}

extern "C" void virtio_net_dbg(void);

bool sched_is_login(uint32_t sid) {
    return ports_name_owned_by(sid, "login");
}

bool sched_is_init(uint32_t sid) {
    return sid < MAX_SESSIONS && sessions[sid].state != S_FREE &&
           !strcmp(sessions[sid].name, "init");
}

uint8_t sched_cap_source(uint32_t sid) {
    return sid < MAX_SESSIONS && sessions[sid].state != S_FREE
           ? sessions[sid].cap_source : 0;
}
int sched_kill(uint32_t sid, uint32_t from_sid) {
    if (!sched_alive(sid)) {
        audit("KILL", "nosession", "registry");
        return -1;
    }
    /* F-AUDIT-3: check the caller's caps, not the BSP's `cur` pointer.
     * On SMP `cur` is a per-CPU field and only the BSP's session writes
     * to the global. Using the per-caller from_sid fixes a real cap
     * audit gap (AP sessions would otherwise be checked against BSP's
     * last set session). */
    if (!(sched_capmask_of(from_sid) & SCHED_CAP_KILL)) {
        audit("KILL", "cap", "registry");
        return -1;
    }
    sessions[sid].state = S_ZOMBIE;
    ports_drain_session(sid);
    ports_clear_session_handles(sid);
    vfio_session_cleanup(sid);
    console_puts("[sched] killed sid=");
    console_hex64(sid);
    console_puts(" ('");
    console_puts(sessions[sid].name);
    console_puts("')\n");
    return 0;
}

uint32_t sched_list(uint32_t *out, char names[][16], uint32_t max) {
    uint32_t n = 0;
    for (int i = 0; i < MAX_SESSIONS && n < max; i++) {
        if (sessions[i].state == S_FREE)
            continue;
        out[n * 3 + 0] = sessions[i].sid;
        out[n * 3 + 1] = sessions[i].uid;
        out[n * 3 + 2] = (uint32_t)sessions[i].state;
        for (int k = 0; k < 15; k++)
            names[n][k] = sessions[i].name[k];
        names[n][15] = 0;
        n++;
    }
    return n;
}

static bool all_dead(void) {
    for (int i = 1; i < MAX_SESSIONS; i++)
        if (sessions[i].state != S_FREE && sessions[i].state != S_ZOMBIE)
            return false;
    return true;
}

/* Transition S_ZOMBIE → S_FREE, reclaiming the slot for new sessions.
 * Called from the scheduler loop before the round-robin scan. */
static void sched_reap_zombies(void) {
    for (int i = 1; i < MAX_SESSIONS; i++) {
        if (sessions[i].state != S_ZOMBIE)
            continue;
        if (sessions[i].eng_live) {
            engine_shutdown(&sessions[i].eng);
            sessions[i].eng_live = false;
        }
        if (sessions[i].stack) {
            rt_free(sessions[i].stack);
            sessions[i].stack = 0;
        }
        sessions[i].state = S_FREE;
        sessions[i].exit_code = -1;
        console_puts("[sched] reaped sid=");
        console_hex64(i);
        console_puts("\n");
    }
}

extern "C" void virtio_net_dbg(void);

void sched_run(void) {
    extern void devblk_poll(void);
    extern void input_poll(void);
    extern void virtio_net_poll(void);
    while (!all_dead()) {
        sched_reap_zombies();
        input_poll();
        devblk_poll();
        virtio_net_poll();
        int picked = -1;
        for (uint32_t k = 0; k < MAX_SESSIONS; k++) {
            uint32_t i = (next_rr + k) % MAX_SESSIONS;
            if (i == 0)
                continue;
            if (sessions[i].state == S_RUNNABLE) {
#ifdef HOST_BUILD
                {
                    static unsigned long long pc;
                    if (pc++ < 60) {
                        console_puts("[pick sid=");
                        console_hex64(i);
                        console_puts("]\n");
                    }
                }
#endif
                picked = (int)i;
                next_rr = (uint32_t)((i + 1) % MAX_SESSIONS);
                break;
            }
        }
        if (picked < 0)
            continue; /* all parked?? spin defensively */

        session *s = &sessions[picked];
        s->state = S_RUNNING;
        cur = s;
        ctx_switch(&kern_sp, s->sp);
    }
}

/* ---- Phase 8.2: per-core scheduler entry (planned, not implemented) ---- */

/* Enter the round-robin loop on this core. Returns when every session
 * in this core's pool is dead. Each core runs its own cooperative-
 * under-interrupt scheduler over its own session pool; no session
 * migrates between cores. */
void sched_run_ap(void) {
    struct sched_state *st = sched_current_cpu();
    extern void devblk_poll(void);
    extern void input_poll(void);
    extern void virtio_net_poll(void);
    while (!all_dead()) {
        input_poll();
        devblk_poll();
        virtio_net_poll();
        int picked = -1;
        for (uint32_t k = 0; k < MAX_SESSIONS; k++) {
            uint32_t i = (st->next_rr + k) % MAX_SESSIONS;
            if (i == 0)
                continue;
            if (st->sessions[i].state == S_RUNNABLE) {
                picked = (int)i;
                st->next_rr = (uint32_t)((i + 1) % MAX_SESSIONS);
                break;
            }
        }
        if (picked < 0)
            continue; /* all parked?? spin defensively */
        st->sessions[picked].state = S_RUNNING;
        st->cur = &st->sessions[picked];
        ctx_switch(&st->kern_sp, st->sessions[picked].sp);
    }
}

/* Called by the AP trampoline (mp.S) after the AP has entered long mode
 * and set up its own CR3. Sets up per-CPU state and enters the
 * scheduler. */
extern "C" void sched_ap_boot(struct ap_boot_info *info) {
    uint32_t id = info->ap_index;
    if (id >= MAX_CPUS) {
        cpu_halt();
        return;
    }
#ifndef HOST_BUILD
    wr_gs_base((uint64_t)&g_cpu[id]);
#else
    g_cpu_id = id;
#endif
    g_cpu[id].cpu_id = id;
    g_cpu[id].ap_ready = 1;
    g_cpu[id].kern_sp = (uint64_t *)info->ap_stack;
    __atomic_fetch_add(&g_n_cpus, 1, __ATOMIC_RELAXED);
    /* Signal to ap_boot that this AP is ready. */
    __atomic_store_n(&info->ap_ready, 1, __ATOMIC_RELEASE);
    console_puts("[ap] cpu");
    char d[2] = {(char)('0' + id), 0};
    console_puts(d);
    console_puts(" booted\n");
    sched_run_ap();
}

/* ---- Phase 19: knob store (registry ops 11/12) ---- */
static uint64_t g_knobs[KNOB_COUNT];

int sched_knob_set(uint8_t idx, uint64_t val) {
    if (idx >= KNOB_COUNT)
        return -1;
    g_knobs[idx] = val;
    return 0;
}

uint64_t sched_knob_get(uint8_t idx) {
    return idx < KNOB_COUNT ? g_knobs[idx] : 0;
}

/* init-time knob defaults */
void sched_knobs_init(void) {
    g_knobs[KNOB_QUANTUM_US] = 5000;
    g_knobs[KNOB_LOG_LEVEL]  = 1;
    g_knobs[KNOB_AUDIT_MASK] = 255;
}
