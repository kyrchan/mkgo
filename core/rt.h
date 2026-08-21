#ifndef RT_H
#define RT_H
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* heap over the mm pool (header-tracked, segregated freelist reuse) */
void *rt_malloc(uint64_t n);
void  rt_free(void *p);
void *rt_realloc(void *p, uint64_t n);
void *rt_calloc(uint64_t n, uint64_t sz);

#ifdef __cplusplus
}
#endif
#endif
