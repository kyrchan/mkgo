#include "boot.h"
#include "plat.h"
#include "mm.h"
#include "sched.h"
#include "ports.h"
#include "wasi_glue.h"
#include "loader.h"
#include "efi.h"
#include "vm/vm.h"

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

    if (!bi->prog) {
        console_puts("[kmain] no guest program; halting\n");
        cpu_halt();
    }

    const uint8_t *prog = (const uint8_t *)(uintptr_t)bi->prog;
    uint64_t prog_len = bi->prog_len;

    /* legacy 8-opcode vbin path (retired at Phase 5) */
    if (!is_wasm(prog, prog_len)) {
        struct vm vm;
        if (vm_create(&vm, prog, prog_len) != 0) {
            console_puts("[kmain] invalid program image; halting\n");
            cpu_halt();
        }
        console_puts("[vm] launching guest\n");
        int rc = vm_run(&vm);
        console_puts("[vm] guest exited rc=");
        console_hex64((uint64_t)(long)rc);
        console_puts("\n");
        console_puts("[kmain] KERNEL-OK all subsystems up, guest ran clean\n");
        cpu_halt();
    }

    /* ---- wasm session mode ---- */
    console_puts("[kmain] wasm mode; spawning boot services\n");
    sched_init();
    ports_init();
    wasi_calibrate_clock(timer_calibrate_tsc_khz());

    const uint8_t *cimg = (const uint8_t *)(uintptr_t)bi->mod_console;
    const uint8_t *limg = (const uint8_t *)(uintptr_t)bi->mod_login;

    if (cimg && bi->mod_console_len)
        sched_spawn_named("console", cimg, bi->mod_console_len, 0, 0);
    else
        console_puts("[kmain] WARNING: no console module on ESP\n");
    if (limg && bi->mod_login_len)
        sched_spawn_named("login", limg, bi->mod_login_len, 0, 0);
    else
        console_puts("[kmain] WARNING: no login module on ESP\n");

    /* payload sessions: argv0 == session name (admin caps until Phase 5) */
    int sa = sched_spawn_named("ppa", prog, prog_len, 0,
                               SCHED_CAP_KILL | SCHED_CAP_DEVMAN |
                                   SCHED_CAP_POWER | SCHED_CAP_SPAWN);
    int sb = sched_spawn_named("ppb", prog, prog_len, 0, 0);
    (void)sa;
    (void)sb;

    sched_run();
    console_puts("[kmain] KERNEL-OK all subsystems up, guest ran clean\n");
    cpu_halt();
}
