/* Host driver: full Phase-4 scenario on the real kernel objects --
 * console + login + ppa(admin) + ppb, then sched_run. */
#include <cstdio>
#include <cstdlib>
#include <cstdint>
#include <cstring>
#include <ctime>

#include "plat.h"
#include <csignal>
#include <cstring>
#include <unistd.h>

static void segv_handler(int, siginfo_t *si, void *uc) {
    ucontext_t *u = (ucontext_t *)uc;
    FILE *mf = fopen("/proc/self/maps", "r");
    char line[512], firstExec[128] = "";
    while (mf && fgets(line, sizeof(line), mf)) {
        if (strstr(line, "hostp5") && strstr(line, "x")) {
            strncpy(firstExec, line, 127);
            break;
        }
    }
    if (mf)
        fclose(mf);
    fprintf(stderr, "[segv] addr=%p rip=%p map=%s", si->si_addr,
            (void *)u->uc_mcontext.gregs[REG_RIP], firstExec);
    _exit(9);
}

#include "mm.h"
#include "sched.h"
#include "ports.h"
#include "devblk.h"
#include "sched.h"
#include "ports.h"

void *mm_alloc(uint64_t n, uint64_t align) {
    if (!align || align < 16)
        align = 16;
    return aligned_alloc(align, ((n + align - 1) & ~(align - 1)));
}

static uint8_t *slurp(const char *path, uint64_t *len) {
    FILE *f = fopen(path, "rb");
    if (!f) { perror(path); exit(2); }
    fseek(f, 0, SEEK_END);
    long n = ftell(f);
    fseek(f, 0, SEEK_SET);
    uint8_t *b = (uint8_t *)malloc(n);
    if (fread(b, 1, n, f) != (size_t)n) exit(2);
    fclose(f);
    *len = (uint64_t)n;
    return b;
}

int main(int argc, char **argv) {
    if (argc != 6) {
        fprintf(stderr, "usage: hostp5 console.wasm login.wasm fs.wasm app.wasm app2.wasm\n");
        return 2;
    }
    extern void wasi_calibrate_clock(uint64_t);
    wasi_calibrate_clock(timer_calibrate_tsc_khz());

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_sigaction = segv_handler;
    sa.sa_flags = SA_SIGINFO;
    sigaction(SIGSEGV, &sa, 0);

    sched_init();
    ports_init();

    uint64_t cl, ll, fl, al, bl;
    uint8_t *c = slurp(argv[1], &cl);
    uint8_t *l = slurp(argv[2], &ll);
    uint8_t *f = slurp(argv[3], &fl);
    uint8_t *a = slurp(argv[4], &al);
    uint8_t *b = slurp(argv[5], &bl);

    int sfs = sched_spawn_named("fs", f, fl, 0, 0);
    if (sfs > 0)
        devblk_attach((unsigned)sfs);
    sched_spawn_named("console", c, cl, 0, 0);
    sched_spawn_named("login", l, ll, 0, 0);
    sched_spawn_named("ppa", a, al, 0,
                      0x1 | 0x2 | 0x4 | 0x40 /*KILL|DEVMAN|POWER|SPAWN*/);
    sched_spawn_named("ppb", b, bl, 0, 0);
    sched_run();
    fprintf(stderr, "[host] scheduler drained\n");
    return 0;
}
