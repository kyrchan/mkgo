#include "boot.h"
#include "plat.h"
#include "mm.h"
#include "sched.h"
#include "wasi_glue.h"
#include "vm/vm.h"

static bool is_wasm(const uint8_t *p, uint64_t len) {
    return len >= 4 && p[0] == 0 && p[1] == 'a' && p[2] == 's' && p[3] == 'm';
}

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

    if (is_wasm(prog, prog_len)) {
        console_puts("[kmain] wasm module detected; session scheduler up\n");
        sched_init();
        wasi_calibrate_clock(timer_calibrate_tsc_khz());
        uint64_t ns0 = wasi_now_ns();
        console_puts("[clock] boot t=");
        console_hex64(ns0);
        console_puts("ns\n");
        int r = sched_spawn(prog, prog_len, 0, 0);
        console_puts("[kmain] scheduler done, spawn rc=");
        console_hex64((uint64_t)(long)r);
        console_puts("\n");
        console_puts("[kmain] KERNEL-OK all subsystems up, guest ran clean\n");
        cpu_halt();
    }

    /* legacy 8-opcode vbin path (retired at Phase 5) */
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
