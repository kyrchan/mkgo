#ifndef ENGINE_H
#define ENGINE_H
#include <stdint.h>

/* The engine. There is exactly one: the project-owned decoder, validator,
 * lowered IR and interpreter (core/wasm). The vendored wasm3 engine that
 * used to sit here is gone from the link -- AGENTS rule 8 and 9 make it
 * neither a runtime requirement nor a permitted fallback -- so the handle
 * is a single pointer and there is no backend tag to get wrong.
 *
 * `owned` is the OwnedEngineState from core/engine.cc: the session, its
 * WASI context, the file-layer hooks' target and the PRNG state. */
struct engine {
    void *owned;
};

/* Decode + validate + link. 0 ok, 1 guest-image error, -1 internal. */
int engine_init(struct engine *e, const uint8_t *blob, uint64_t len);
int engine_set_args(struct engine *e, const char *const *args, uint32_t count);
int engine_start(struct engine *e);
void engine_request_preempt(struct engine *e);
const char *engine_errstr(struct engine *e);
void engine_shutdown(struct engine *e);

/* Guest linear memory of a live engine: the base pointer plus the
 * guest-visible size, or null when there is no live engine. Device windows
 * (ABI.md 6 net RX/TX rings, VFIO BARs) MUST go through this rather than
 * caching a pointer -- growth can relocate the buffer, so a cached pointer
 * would read freed memory. */
uint8_t *engine_memory(struct engine *e, uint32_t *size);
/* Ensure linear memory covers min_bytes, in one step. 0 on success
 * (including already large enough), -1 if there is no live engine, the
 * target is refused, or the allocation failed. */
int engine_ensure_memory(struct engine *e, uint32_t min_bytes);

#endif
