#include "cpu.h"
#include "plat.h"
#include <stdint.h>

extern "C" void irq0_stub(void);

static uint64_t gdt[8] __attribute__((aligned(64)));
static struct { uint16_t lo, sel; uint8_t ist, attr; uint16_t mid; uint32_t hi; uint32_t zero; }
    idt[256] __attribute__((aligned(4096)));

struct frame {
    uint64_t vector, err, rip, cs, rflags, rsp, ss;
};

extern "C" void isr_dump(const struct frame *f) {
    cli();
    console_puts("\n[PANIC] vector=");
    console_hex64(f->vector);
    console_puts(" err=");
    console_hex64(f->err);
    console_puts("\n[PANIC] rip=");
    console_hex64(f->rip);
    console_puts(" cs=");
    console_hex64(f->cs);
    console_puts(" rfl=");
    console_hex64(f->rflags);
    if (f->vector == 14) {
        uint64_t cr2;
        __asm__ volatile("mov %%cr2, %0" : "=r"(cr2));
        console_puts(" cr2=");
        console_hex64(cr2);
    }
    console_puts("\n");
    cpu_halt();
}

extern "C" void *isr_stub_table[256];

void gdt_install(void) {
    gdt[0] = 0;
    gdt[1] = 0x00AF9A000000FFFFULL; /* 64-bit code */
    gdt[2] = 0x00CF92000000FFFFULL; /* data */
    struct { uint16_t limit; uint64_t base; } __attribute__((packed)) dgdtr = {
        sizeof(gdt) - 1, (uint64_t)(uintptr_t)gdt };
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
    console_puts("[cpu] gdt installed\n");
}

void pic_remap(void);
void pit_init(uint32_t hz);

void idt_install(void) {
    pic_remap();
    pit_init(1000);
    for (int v = 0; v < 256; v++) {
        uint64_t h = (uint64_t)(uintptr_t)isr_stub_table[v];
        idt[v].lo = (uint16_t)h;
        idt[v].sel = 0x08;
        idt[v].ist = 0;
        idt[v].attr = 0x8E;
        idt[v].mid = (uint16_t)(h >> 16);
        idt[v].hi = (uint32_t)(h >> 32);
    }
    /* IRQ0 -> dedicated preemption stub */
    {
        uint64_t h_irq = (uint64_t)(uintptr_t)irq0_stub;
        idt[32].lo = (uint16_t)h_irq;
        idt[32].sel = 0x08;
        idt[32].ist = 0;
        idt[32].attr = 0x8E;
        idt[32].mid = (uint16_t)(h_irq >> 16);
        idt[32].hi = (uint32_t)(h_irq >> 32);
    }
    struct { uint16_t limit; uint64_t base; } __attribute__((packed)) didtr = {
        sizeof(idt) - 1, (uint64_t)(uintptr_t)idt };
    __asm__ volatile("lidt %0" :: "m"(didtr) : "memory");
    console_puts("[cpu] idt installed\n");
}
