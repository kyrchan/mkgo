#include "sched.h"
#include "engine.h"
#include "wasi_glue.h"
#include "plat.h"

static constexpr int MAX_SESSIONS = 8;

struct session {
    bool live;
    struct engine eng;
    int exit_code;
};
static struct session sessions[MAX_SESSIONS];
static uint32_t n_live;

void sched_init(void) {
    for (int i = 0; i < MAX_SESSIONS; i++)
        sessions[i].live = false;
    n_live = 0;
}

int sched_spawn(const uint8_t *blob, uint64_t len,
                const char *const *argv, int argc) {
    wasi_reset_session();
    wasi_set_argv(argv, argc);
    for (int i = 0; i < MAX_SESSIONS; i++) {
        if (sessions[i].live)
            continue;
        session *s = &sessions[i];
        s->exit_code = -1;
        int r = engine_init(&s->eng, blob, len);
        if (r != 0) {
            console_puts("[sched] engine_init failed\n");
            return -1;
        }
        s->live = true;
        n_live++;
        /* run immediately (cooperative single-slot); Phase 4 interleaves */
        r = engine_start(&s->eng);
        s->exit_code = wasi_exited() ? wasi_exit_code() : (r ? 1 : 0);
        engine_shutdown(&s->eng);
        s->live = false;
        n_live--;
        console_puts("[sched] session exited rc=");
        console_hex64((uint64_t)s->exit_code);
        console_puts("\n");
        return 0;
    }
    return -1;
}

void sched_run(void) {
    /* all scheduling happens at spawn in cooperative mode */
}

uint32_t sched_live(void) { return n_live; }
