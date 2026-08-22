#ifndef CTX_H
#define CTX_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Coroutine switch primitives (arch/x86_64/ctx.S). */
void ctx_switch(uint64_t **old_sp_out, uint64_t *new_sp);
void ctx_trampoline(void);

#ifdef __cplusplus
}

/* C++ helper: build initial stack frame for a fresh session.
 * Returns the saved sp to hand to ctx_switch. */
uint64_t *ctx_make(uint8_t *stack_base, uint64_t size);
#endif
#endif
