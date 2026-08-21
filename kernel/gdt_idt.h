#ifndef GDT_IDT_H
#define GDT_IDT_H
#include <stdint.h>

void gdt_install(void);
void idt_install(void);

#endif
