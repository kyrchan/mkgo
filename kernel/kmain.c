#include "boot.h"
#include "cpu.h"
#include "serial.h"
#include "mm.h"
#include "gdt_idt.h"
#include "vm/vm.h"

void kmain(const struct boot_info *bi) {
    serial_puts("[kmain] hello from the microkernel\n");
    cpu_dump_features();

    int r = cpu_enable_avx2();
    if (r != 0) {
        serial_puts("[kmain] AVX2 unavailable (err ");
        serial_hex64((uint64_t)-r);
        serial_puts(") - vector unit disabled\n");
    } else {
        serial_puts("[cpu] SSE/AVX/AVX2 enabled, XCR0=7\n");
    }

    gdt_install();
    idt_install();
    mm_init(&bi->mmap);
    paging_identity_init();

    if (!bi->prog) {
        serial_puts("[kmain] no guest program; halting\n");
        for (;;)
            hlt();
    }

    struct vm vm;
    if (vm_create(&vm, bi->prog, bi->prog_len) != 0) {
        serial_puts("[kmain] invalid program image; halting\n");
        for (;;)
            hlt();
    }
    serial_puts("[vm] launching guest\n");
    int rc = vm_run(&vm);
    serial_puts("[vm] guest exited rc=");
    serial_hex64((uint64_t)(long)rc);
    serial_puts("\n");

    serial_puts("[kmain] KERNEL-OK all subsystems up, guest ran clean\n");
    for (;;)
        hlt();
}
