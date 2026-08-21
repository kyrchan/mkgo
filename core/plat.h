#ifndef CORE_PLAT_H
#define CORE_PLAT_H
#include <stdint.h>

/* The entire arch contract. Implemented per-target under arch/<target>/;
 * core/ carries zero #ifdef, zero inline asm, zero direct HW access
 * (AGENTS.md hard rule 1). */

/* console (uart) */
void console_init(void);
void console_putc(char c);
void console_puts(const char *s);
void console_hex64(uint64_t v);

/* cpu */
void cpu_dump_features(void);
int  cpu_enable_vector(void); /* 0 = vector unit live */
void cpu_halt(void);

/* machine bring-up */
void gdt_install(void);
void idt_install(void);
void paging_identity_init(void);

#endif
