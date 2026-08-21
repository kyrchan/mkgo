#include "gdt_idt.h"
#include "cpu.h"
#include "serial.h"

static uint64_t gdt[8] __attribute__((aligned(64)));
static struct { uint16_t lo, sel; uint8_t ist, attr; uint16_t mid; uint32_t hi; uint32_t zero; }
    idt[256] __attribute__((aligned(4096)));
static uint16_t idt_limit;
static uint64_t gdt_limit;

struct frame {
    uint64_t vector, err, rip, cs, rflags, rsp, ss;
};

void isr_dump(const struct frame *f) {
    cli();
    serial_puts("\n[PANIC] vector=");
    serial_hex64(f->vector);
    serial_puts(" err=");
    serial_hex64(f->err);
    serial_puts("\n[PANIC] rip=");
    serial_hex64(f->rip);
    serial_puts(" cs=");
    serial_hex64(f->cs);
    serial_puts(" rfl=");
    serial_hex64(f->rflags);
    if (f->vector == 14) {
        uint64_t cr2;
        __asm__ volatile("mov %%cr2, %0" : "=r"(cr2));
        serial_puts(" cr2=");
        serial_hex64(cr2);
    }
    serial_puts("\n");
    for (;;)
        hlt();
}

void gdt_install(void) {
    gdt[0] = 0;
    gdt[1] = 0x00AF9A000000FFFFULL; /* 64-bit code */
    gdt[2] = 0x00CF92000000FFFFULL; /* data */
    gdt_limit = sizeof(gdt) - 1;
    struct { uint16_t limit; uint64_t base; } __attribute__((packed)) dgdtr = {
        (uint16_t)gdt_limit, (uint64_t)(uintptr_t)gdt };
    __asm__ volatile("lgdt %0" :: "m"(dgdtr) : "memory");
    /* reload segments: keep CS via far return */
    __asm__ volatile(
        "pushq $0x08\n"
        "leaq 1f(%%rip), %%rax\n"
        "pushq %%rax\n"
        "lretq\n"
        "1:\n"
        "movw $0x10, %%ax\n"
        "movw %%ax, %%ds\n"
        "movw %%ax, %%es\n"
        "movw %%ax, %%ss\n"
        "xorl %%eax, %%eax\n"
        "movw %%ax, %%fs\n"
        "movw %%ax, %%gs\n"
        :
        :
        : "rax", "memory");
    serial_puts("[cpu] gdt installed\n");
}

extern void *isr_stub_table[256];
extern void (*isr_dump_ptr)(const struct frame *); /* defined in gen_vectors.s */

void idt_install(void) {
    /* fault dumper lives in Plan 9 asm land; hand it the C handler */
    isr_dump_ptr = isr_dump;
    for (int v = 0; v < 256; v++) {
        uint64_t h = (uint64_t)(uintptr_t)isr_stub_table[v];
        idt[v].lo = (uint16_t)h;
        idt[v].sel = 0x08;
        idt[v].ist = 0;
        idt[v].attr = 0x8E;
        idt[v].mid = (uint16_t)(h >> 16);
        idt[v].hi = (uint32_t)(h >> 32);
    }
    struct { uint16_t limit; uint64_t base; } __attribute__((packed)) didtr = {
        sizeof(idt) - 1, (uint64_t)(uintptr_t)idt };
    __asm__ volatile("lidt %0" :: "m"(didtr) : "memory");
    serial_puts("[cpu] idt installed\n");
}
