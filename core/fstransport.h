/* FS transport for kernel-routed preview1 path ops (abi/ABI.md v1.1,
 * Phase 5 routing decision: BOTH routes exist).
 *
 * The fs.wasm session exports:
 *   _fsbuf()     -> i32 offset of its request buffer in its own memory
 *   _fsrespbuf() -> i32 offset of its reply buffer
 *   _fsreq(req_len i32, resp_cap i32) -> i32 reply length
 * The kernel writes a framed request into that buffer, calls _fsreq on
 * the fs session's runtime (synchronous, serialized by fs_lock), and
 * reads the framed reply back. Framing: {u16 op,u16 seq,u32 uid,
 * u16 path_len, path bytes, payload...}. */
#ifndef FSTRANSPORT_H
#define FSTRANSPORT_H
#include <stdint.h>

/* returns reply len (>0) or negative on transport failure */
#ifdef __cplusplus
extern "C" {
#endif

int fs_call(const uint8_t *req, uint32_t req_len, uint8_t *resp,
            uint32_t resp_cap);
uint16_t fs_next_seq(void);

#ifdef __cplusplus
}
#endif

#endif
