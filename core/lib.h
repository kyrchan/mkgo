#ifndef CORE_LIB_H
#define CORE_LIB_H
#include <stdint.h>
#include <stddef.h>

#ifdef HOST_BUILD
#include <string.h>
#else
#ifdef __cplusplus
extern "C" {
#endif

void *memset(void *d, int c, size_t n);
void *memcpy(void *d, const void *s, size_t n);
void *memmove(void *d, const void *s, size_t n);
int memcmp(const void *a, const void *b, size_t n);
uint64_t strlen(const char *s);
int strcmp(const char *a, const char *b);
int strncmp(const char *a, const char *b, size_t n);

/* Wall clock for clock_time_get, in nanoseconds. TSC-derived and calibrated
 * once at boot; lives here rather than in a WASI bridge because it is engine
 * independent -- it used to sit in the wasm3 bridge and the owned engine had
 * to reach across into it. */
void wasi_calibrate_clock(uint64_t tsc_khz);
uint64_t wasi_now_ns(void);

#ifdef __cplusplus
}
#endif
#endif
#endif /* !HOST_BUILD */
