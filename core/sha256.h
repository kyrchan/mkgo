#ifndef SHA256_H
#define SHA256_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Minimal SHA-256 (FIPS 180-4) for cache-integrity hashing. Freestanding:
 * no libc, no heap, caller-provided context. Used by core/jit.cc to bind
 * a native cache blob to its source .wasm bytes. */

struct sha256_ctx {
    uint32_t h[8];
    uint64_t len; /* total input bytes */
    uint8_t buf[64];
    uint32_t buflen;
};

void sha256_init(struct sha256_ctx *c);
void sha256_update(struct sha256_ctx *c, const uint8_t *data, uint64_t len);
void sha256_final(struct sha256_ctx *c, uint8_t out[32]);

static inline void sha256_once(const uint8_t *data, uint64_t len,
                               uint8_t out[32]) {
    struct sha256_ctx c;
    sha256_init(&c);
    sha256_update(&c, data, len);
    sha256_final(&c, out);
}

#ifdef __cplusplus
}
#endif

#endif /* SHA256_H */
