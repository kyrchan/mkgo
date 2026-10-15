/* Backend-neutral kernel-routed preview1 file layer.
 *
 * AGENTS rule 4 retains path_open/fd_read/fd_write(>=3)/fd_close/
 * path_create_directory as a v2 convenience so stock Go os.Open works
 * cross-platform. The routing is NOT engine-dependent: each op is framed
 * as a §1 datagram to fs.wasm using the same op encoding as direct-port
 * clients (STAT=1 LIST=2 READ=3 WRITE=4 CREATE=5 MKDIR=6 DELETE=7),
 * replies are canonical 24-byte-header frames (status i32 @24, body @28),
 * and the server is stateless/path-based, so preview1 fds map to
 * {path, cursor} held kernel-side here.
 *
 * This used to live inside core/wasi_glue.cc, which made it unreachable
 * from the owned engine: the file layer's guest-facing imports were m3
 * raw functions, so a stock Go guest could not os.Open() anything unless
 * the whole system ran on wasm3. Both bridges now call this module, and
 * the routing logic exists exactly once.
 *
 * Engine-specific work (reading the path string out of guest memory,
 * bounds-checking iovecs, returning a preview1 errno) belongs to the
 * bridge, not here.
 */
#ifndef FSIMPORT_H
#define FSIMPORT_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* preview1 errno numbering (abi/ABI.md / wasi_snapshot_preview1). Shared
 * so the two bridges cannot drift on a code. */
enum {
    FSIMPORT_ESUCCESS = 0,
    FSIMPORT_EACCES = 2,
    FSIMPORT_EEXIST = 20,
    FSIMPORT_EBADF = 8,
    FSIMPORT_EINVAL = 28,
    FSIMPORT_EIO = 29,
    FSIMPORT_EISDIR = 31,
    FSIMPORT_ENAMETOOLONG = 37,
    FSIMPORT_ENOENT = 44,
    FSIMPORT_ENOSPC = 51,
    FSIMPORT_ENOSYS = 52,
    FSIMPORT_ENOTDIR = 54,
    FSIMPORT_ENOTEMPTY = 55
};

#define FSIMPORT_PREOPEN_FD 3
#define FSIMPORT_FD_PREOPEN_ROOT 0x7FFFFFFEu
#define FSIMPORT_PATH_MAX 192
/* Single-datagram payload budget; a routed read/write is clamped to it. */
#define FSIMPORT_MAX_IO 8192

/* Map a services/fs wire status to a preview1 errno. */
int32_t fsimport_errno_for_status(int32_t fs_status);

/* fd table. Allocation lazily publishes the root preopen on fd 3, which
 * is what lets stock runtimes open "/" paths. */
int32_t fsimport_alloc_fd(void);
int32_t fsimport_fd_is_open(int32_t fd);
void fsimport_release_fd(int32_t fd);

/* Rebase a guest-relative path onto the root preopen. Stock runtimes
 * strip the preopen prefix and hand us a relative remnant, while the
 * routed transport is absolute-path based. Returns 0 on success. */
int32_t fsimport_rebase_path(char *path, uint32_t capacity, int32_t dirfd);

/* Ops. Each returns a preview1 errno (0 = success) and performs the
 * fs.wasm roundtrip(s) itself. */
int32_t fsimport_path_open(const char *path, int32_t oflags, int32_t *out_fd);
int32_t fsimport_mkdir(const char *path);
int32_t fsimport_read(int32_t fd, uint8_t *buf, uint32_t len, uint32_t *done);
int32_t fsimport_write(int32_t fd, const uint8_t *buf, uint32_t len,
                       uint32_t *done);
/* Shared read/write for a routed fd: returns a preview1 errno and sets
 * *done. The local-JIT trampoline's native fd_write calls this directly. */
int32_t routed_rw(int32_t fd, int write, uint8_t *buf, uint32_t len,
                  uint32_t *done);

#ifdef __cplusplus
}
#endif

#endif
