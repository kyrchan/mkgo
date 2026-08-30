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

static constexpr int MAX_IMAGES = 8;
static constexpr uint64_t STACK_BYTES = 1024 * 1024;

enum st { S_FREE = 0, S_RUNNABLE = 1, S_RUNNING = 2, S_ZOMBIE = 3 };

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

struct image {
    bool used;
    char name[16];
    const uint8_t *blob;
    uint64_t len;
};

static session sessions[MAX_SESSIONS];
static image images[MAX_IMAGES];
static uint32_t next_rr;
static session *cur;          /* running on ITS stack right now */
static uint64_t *kern_sp;     /* scheduler/boot stack */

sched_wasi_state *sched_wasi_current(void) {
    return cur ? &cur->wctx : 0;
}

void *sched_runtime_of(uint32_t sid) {
    if (sid >= MAX_SESSIONS || !sessions[sid].eng_live)
        return 0;
    return sessions[sid].eng.rt;
}

uint32_t sched_current_sid(void) { return cur ? cur->sid : 0; }
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
    }
    next_rr = 1;
    cur = 0;
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
    session *s = cur;
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
    ctx_switch(&s->sp, kern_sp); /* never returns here */
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
    session *s = cur;
    s->state = S_RUNNABLE;
    ctx_switch(&s->sp, kern_sp);
}

void sched_exit_current(int rc) {
    (void)rc; /* exit path handled by session_main */
    ctx_switch(&cur->sp, kern_sp);
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
        console_puts("[sched] identity '");
        console_puts(sessions[sid].name);
        console_puts("' uid=");
        console_hex64(uid);
        console_puts(" caps=");
        console_hex64(capmask);
        console_puts("\n");
    }
}

extern "C" bool ports_name_owned_by(uint32_t sid, const char *name);
bool sched_is_login(uint32_t sid) {
    return ports_name_owned_by(sid, "login");
}

int sched_kill(uint32_t sid) {
    if (!sched_alive(sid)) {
        audit("KILL", "nosession", "registry");
        return -1;
    }
    if (!(cur && (cur->capmask & SCHED_CAP_KILL))) {
        audit("KILL", "cap", "registry");
        return -1;
    }
    sessions[sid].state = S_ZOMBIE;
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
        for (int k = 0; k < 16; k++)
            names[n][k] = sessions[i].name[k];
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

extern "C" void virtio_net_dbg(void);

void sched_run(void) {
    extern void devblk_poll(void);
    extern void input_poll(void);
    extern void virtio_net_poll(void);
    while (!all_dead()) {
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
