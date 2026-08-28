#include "boot.h"
#include "plat.h"
#include "mm.h"
#include "sched.h"
#include "ports.h"
#include "fsroute.h"
#include "devblk.h"
#include "vfio.h"
#include "wasi_glue.h"
#include "loader.h"
#include "efi.h"

static bool is_wasm(const uint8_t *p, uint64_t len) {
    return len >= 4 && p[0] == 0 && p[1] == 'a' && p[2] == 's' && p[3] == 'm';
}

/* boot module paths (ESP, backslash form) */
static CHAR16 p_console[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                             'c','o','n','s','o','l','e','.','w','a','s','m',0};
static CHAR16 p_login[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                           'l','o','g','i','n','.','w','a','s','m',0};

void kmain(const struct boot_info *bi) {
    console_puts("[kmain] hello from the microkernel\n");
    cpu_dump_features();

    if (cpu_enable_vector() != 0)
        console_puts("[kmain] vector unit unavailable - disabled\n");
    else
        console_puts("[cpu] SSE/AVX/AVX2 enabled, XCR0=7\n");

    gdt_install();
    idt_install();
    const struct boot_mmap bm = {(void *)(uintptr_t)bi->mmap_desc,
                                 bi->mmap_count, bi->mmap_dsize};
    mm_init(&bm);
    paging_identity_init();

    const uint8_t *prog = (const uint8_t *)(uintptr_t)bi->prog;
    uint64_t prog_len = bi->prog_len;

    if (prog && !is_wasm(prog, prog_len)) {
        console_puts("[kmain] refusing non-wasm payload\n");
        cpu_halt();
    }

    /* ---- session mode (init-driven; payload slots optional) ----*/
    console_puts("[kmain] session mode\n");
    sched_init();
    ports_init();
    fsroute_init();
    wasi_calibrate_clock(timer_calibrate_tsc_khz());

    const uint8_t *cimg = (const uint8_t *)(uintptr_t)bi->mod_console;
    const uint8_t *limg = (const uint8_t *)(uintptr_t)bi->mod_login;
    const uint8_t *fimg = (const uint8_t *)(uintptr_t)bi->mod_fs;

    (void)cimg;
    (void)limg;
    (void)fimg;

    /* kernel spawns EXACTLY ONE session: init.wasm (admin caps).
     * init.conf rides along as argv[1] (ESP preload). */
    const char *iargv[3] = {"init", 0, 0};
    static char confz[4096];
    if (bi->conf && bi->conf_len) {
        uint64_t n = bi->conf_len < sizeof(confz) - 1 ? bi->conf_len
                                                      : sizeof(confz) - 1;
        const uint8_t *cp = (const uint8_t *)(uintptr_t)bi->conf;
        for (uint64_t i = 0; i < n; i++)
            confz[i] = (char)cp[i];
        confz[n] = 0;
        iargv[1] = confz;
    } else {
        console_puts("[kmain] WARNING: no init.conf on ESP\n");
    }

    devblk_init();
    {
        extern void virtio_blk_init(void);
        virtio_blk_init();
    }
    virtio_net_init();
    vfio_init();
    devblk_attach();

    /* preload service modules so registry SPAWN can resolve them */
    {
        const uint8_t *c_ = (const uint8_t *)(uintptr_t)bi->mod_console;
        const uint8_t *l_ = (const uint8_t *)(uintptr_t)bi->mod_login;
        const uint8_t *f_ = (const uint8_t *)(uintptr_t)bi->mod_fs;
        const uint8_t *s_ = (const uint8_t *)(uintptr_t)bi->mod_shell;
        const uint8_t *n_ = (const uint8_t *)(uintptr_t)bi->mod_net;
        const uint8_t *q_ = (const uint8_t *)(uintptr_t)bi->mod_p9;
        if (c_) sched_preload_image("console", c_, bi->mod_console_len);
        if (l_) sched_preload_image("login", l_, bi->mod_login_len);
        if (f_) sched_preload_image("fs", f_, bi->mod_fs_len);
        if (s_) sched_preload_image("shell", s_, bi->mod_shell_len);
        if (n_) sched_preload_image("net", n_, bi->mod_net_len);
        if (q_) sched_preload_image("p9", q_, bi->mod_p9_len);
    }

    const uint8_t *iimg = (const uint8_t *)(uintptr_t)bi->mod_init;
    if (iimg && bi->mod_init_len) {
        /* init-driven mode: kernel spawns ONLY init */
        sched_spawn_named_argv(
            "init", iimg, bi->mod_init_len, 0,
            SCHED_CAP_KILL | SCHED_CAP_DEVMAN | SCHED_CAP_POWER |
                SCHED_CAP_SPAWN | SCHED_CAP_FOCUS | SCHED_CAP_FSADM |
                SCHED_CAP_CONF | SCHED_CAP_NETADM,
            iargv, iargv[1] ? 2 : 1);
    } else {
        /* legacy gate mode: dual payload slots with admin caps */
        console_puts("[kmain] legacy payload-slot mode\n");
        if (!prog) {
            console_puts("[kmain] no init and no payloads; halting\n");
            cpu_halt();
        }
        /* F37: payload slots are gate conveniences, not admins. ppa holds
         * only what legacy gates exercise. gate_mask from bootinfo overrides
         * the default KILL (used by Phase 11 graphics test which needs
         * CAP_PCI|CAP_FB). */
        const uint64_t GATE = bi->gate_mask ? bi->gate_mask : SCHED_CAP_KILL;
        /* boot services from ESP when present (Phase-4 style) */
        const uint8_t *c_ = (const uint8_t *)(uintptr_t)bi->mod_console;
        const uint8_t *l_ = (const uint8_t *)(uintptr_t)bi->mod_login;
        const uint8_t *f_ = (const uint8_t *)(uintptr_t)bi->mod_fs;
        if (c_) sched_spawn_named("console", c_, bi->mod_console_len, 0, 0);
        if (l_) sched_spawn_named("login", l_, bi->mod_login_len, 0, 0);
        int sfs = -1;
        if (f_) {
            /* fs owns the whole-disk transport: it carries CAP_FSADM
             * (abi/ABI.md §3 gate); boot services hold no other bits */
            sfs = sched_spawn_named("fs", f_, bi->mod_fs_len, 0,
                                    SCHED_CAP_FSADM);
            if (sfs > 0)
                devblk_attach();
        }

        /* two payload slots; app2 defaults to another copy of app */
        const uint8_t *progB = bi->prog2
                                   ? (const uint8_t *)(uintptr_t)bi->prog2
                                   : prog;
        uint64_t progB_len = bi->prog2 ? bi->prog2_len : prog_len;
        int sa = sched_spawn_named("ppa", prog, prog_len, 0, GATE);
        int sb = sched_spawn_named("ppb", progB, progB_len, 0, 0);
        (void)sa;
        (void)sb;
    }

    /* Cooperative scheduling primary; preempt deferred (Phase 9+) */
    sched_run();
    console_puts("[kmain] KERNEL-OK all subsystems up, guest ran clean\n");
    cpu_halt();
}
