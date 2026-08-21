#ifndef WASI_GLUE_H
#define WASI_GLUE_H
#include <stdint.h>

struct M3Module;
typedef struct M3Module *IM3Module;

void wasi_reset_session(void);
void wasi_set_argv(const char *const *argv, int argc);
bool wasi_exited(void);
int  wasi_exit_code(void);
void wasi_calibrate_clock(uint64_t tsc_khz);
uint64_t wasi_now_ns(void);

/* Link the frozen profile (+ linkage stubs) onto a parsed module.
 * Returns wasm3 M3Result (const char*), 0 == success. */
const char *wasi_link_module(IM3Module mod);

#endif
