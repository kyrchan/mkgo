#ifndef ENGINE_H
#define ENGINE_H
#include <stdint.h>

struct engine {
    void *env; /* IM3Environment */
    void *rt;  /* IM3Runtime */
    void *mod; /* IM3Module */
};

/* Parse + load + link WASI. 0 ok, 1 guest-image error, -1 internal. */
int  engine_init(struct engine *e, const uint8_t *blob, uint64_t len);
int  engine_start(struct engine *e); /* runs _start to completion */
const char *engine_errstr(struct engine *e);
void engine_shutdown(struct engine *e);

#endif
