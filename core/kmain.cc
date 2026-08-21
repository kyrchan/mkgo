#include "boot.h"
#include "plat.h"
#include "mm.h"
#include "vm/vm.h"

void kmain(const struct boot_info *bi) {
    console_puts("[kmain] hello from the microkernel\n");
    cpu_dump_features();

    if (cpu_enable_vector() != 0) {
        console_puts("[kmain] vector unit unavailable - disabled\n");
    } else {
        console_puts("[cpu] SSE/AVX/AVX2 enabled, XCR0=7\n");
    }

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

    struct vm vm;
    if (vm_create(&vm, (const uint8_t *)(uintptr_t)bi->prog, bi->prog_len) != 0) {
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
