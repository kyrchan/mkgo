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

/* Boot_info pointer. kmain() stores it so arch/x86_64/mp.cc can read
 * the MADT physical address. */
const struct boot_info *g_bi_ptr;
const struct boot_info *boot_info(void) { return g_bi_ptr; }

/* boot module paths (ESP, backslash form) -- now loaded via bi->mod_* */

void kmain(const struct boot_info *bi) {
    console_puts("[kmain] hello from the microkernel\n");
    cpu_dump_features();

    /* Store the boot_info pointer so arch/x86_64/mp.cc can read the
     * MADT physical address. */
    extern const struct boot_info *g_bi_ptr;
    g_bi_ptr = bi;

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
    vfio_init(bi);
    devblk_attach();

    /* preload service modules so registry SPAWN can resolve them */
    {
        const uint8_t *c_ = (const uint8_t *)(uintptr_t)bi->mod_console;
        const uint8_t *l_ = (const uint8_t *)(uintptr_t)bi->mod_login;
        const uint8_t *f_ = (const uint8_t *)(uintptr_t)bi->mod_fs;
        const uint8_t *s_ = (const uint8_t *)(uintptr_t)bi->mod_shell;
        const uint8_t *n_ = (const uint8_t *)(uintptr_t)bi->mod_net;
        const uint8_t *q_ = (const uint8_t *)(uintptr_t)bi->mod_p9;
        const uint8_t *g_ = (const uint8_t *)(uintptr_t)bi->mod_graphics;
        if (c_) sched_preload_image("console", c_, bi->mod_console_len);
        if (l_) sched_preload_image("login", l_, bi->mod_login_len);
        if (f_) sched_preload_image("fs", f_, bi->mod_fs_len);
        if (s_) sched_preload_image("shell", s_, bi->mod_shell_len);
        if (n_) sched_preload_image("net", n_, bi->mod_net_len);
        if (q_) sched_preload_image("p9", q_, bi->mod_p9_len);
        if (g_) sched_preload_image("graphics", g_, bi->mod_graphics_len);
    }

    const uint8_t *iimg = (const uint8_t *)(uintptr_t)bi->mod_init;
    if (iimg && bi->mod_init_len) {
        /* init-driven mode: kernel spawns ONLY init */
        sched_spawn_named_argv(
            "init", iimg, bi->mod_init_len, 0,
            SCHED_CAP_KILL | SCHED_CAP_DEVMAN | SCHED_CAP_POWER |
                SCHED_CAP_SPAWN | SCHED_CAP_FOCUS | SCHED_CAP_FSADM |
                SCHED_CAP_CONF | SCHED_CAP_NETADM | SCHED_CAP_PCI |
                SCHED_CAP_FB | SCHED_CAP_PORTBIND,
            iargv, iargv[1] ? 2 : 1);
    } else {
        /* legacy gate mode: dual payload slots with admin caps */
        console_puts("[kmain] legacy payload-slot mode\n");
        if (!prog) {
            console_puts("[kmain] no init and no payloads; halting\n");
            cpu_halt();
        }
         /* gate_mask overrides the default KILL (used by Phase 11 graphics test which needs
          * CAP_PCI|CAP_FB). CAP_PORTBIND lets payload sessions bind
          * kernel endpoints like "registry" for diagnostic LIST ops. */
          const uint64_t GATE = bi->gate_mask ? bi->gate_mask :
                                                 SCHED_CAP_KILL | SCHED_CAP_PORTBIND;
         /* boot services from ESP when present (Phase-4 style) */
          const uint8_t *c_ = (const uint8_t *)(uintptr_t)bi->mod_console;
          const uint8_t *l_ = (const uint8_t *)(uintptr_t)bi->mod_login;
          const uint8_t *f_ = (const uint8_t *)(uintptr_t)bi->mod_fs;
          const uint8_t *s_ = (const uint8_t *)(uintptr_t)bi->mod_shell;
          const uint8_t *g_ = (const uint8_t *)(uintptr_t)bi->mod_graphics;
          if (c_) sched_spawn_named("console", c_, bi->mod_console_len, 0, SCHED_CAP_PORTBIND);
          int sfs = -1;
          if (f_) {
            /* fs must be up before login binds its port */
            sfs = sched_spawn_named("fs", f_, bi->mod_fs_len, 0,
                                    SCHED_CAP_FSADM | SCHED_CAP_PORTBIND);
            if (sfs > 0)
              devblk_attach();
          }
          if (l_) sched_spawn_named("login", l_, bi->mod_login_len, 0, SCHED_CAP_PORTBIND);
          if (s_) sched_spawn_named("shell", s_, bi->mod_shell_len, 0, SCHED_CAP_FOCUS | SCHED_CAP_PORTBIND);
          if (g_) sched_spawn_named("graphics", g_, bi->mod_graphics_len,
                                        0, SCHED_CAP_FB | SCHED_CAP_PCI);

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

    /* Phase 8.2: bring up AP cores. Each AP runs its own cooperative-
     * under-interrupt scheduler over its own session pool; no session
     * migrates between cores. The Go runtime IS the preemption
     * mechanism (Go 1.14+ yields cooperatively in wasm at every
     * goroutine switch point), and multiple cores provide the
     * parallelism -- neither requires touching the opaque
     * interpreter state in m3_exec.c. */
    const struct madt *m = madt_parse();
    if (m) {
        console_puts("[ap] MADT found, ");
        console_hex64(m->n_cpus);
        console_puts(" cpus\n");
        int acked = ap_boot(m);
        console_puts("[ap] ");
        console_hex64(acked);
        console_puts(" APs acked\n");
        if (acked == (int)m->n_cpus - 1)
            console_puts("[kmain] KERNEL-OK all subsystems up, APs booted\n");
    } else {
        console_puts("[ap] no MADT (single CPU)\n");
        console_puts("[kmain] KERNEL-OK all subsystems up, single CPU\n");
    }

    /* Scheduler enters the round-robin loop. Preemption (Phase 8) is
     * driven by IRQ0 from the PIT -- conf_set_preempt() flips it on;
     * default is cooperative (preempt_off). The yield path in
     * sched_yield_current() checks preempt_pending on every call. */
    sti_impl();
    sched_run();
    cpu_halt();
}
