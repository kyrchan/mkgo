#ifndef KERNEL_WASM_WASI_BRIDGE_H
#define KERNEL_WASM_WASI_BRIDGE_H

#include "owned_session.h"

namespace wasm {

enum OwnedWasiImport : uint32_t {
    OwnedWasiSchedYield = 1u << 0,
    OwnedWasiProcExit = 1u << 1,
    OwnedWasiArgsGet = 1u << 2,
    OwnedWasiArgsSizesGet = 1u << 3,
    OwnedWasiClockTimeGet = 1u << 4,
    OwnedWasiEnvironGet = 1u << 5,
    OwnedWasiEnvironSizesGet = 1u << 6,
    OwnedWasiFdWrite = 1u << 7,
    OwnedWasiRandomGet = 1u << 8,
    OwnedWasiPollOneoff = 1u << 9,
    OwnedWasiFdClose = 1u << 10,
    OwnedWasiFdFdstatGet = 1u << 11,
    OwnedWasiFdFdstatSetFlags = 1u << 12,
    OwnedWasiFdPrestatGet = 1u << 13,
    OwnedWasiFdPrestatDirName = 1u << 14,
    // Kernel-routed preview1 file layer (AGENTS rule 4, "retained as a v2
    // convenience"). NOT part of the frozen profile: these three are what
    // make a stock Go guest's os.Open work, and they are what wasm3-only
    // routing used to make unreachable. Host hooks below; the routing
    // itself is engine-neutral in core/fsimport.cc.
    OwnedWasiFdRead = 1u << 15,
    OwnedWasiPathOpen = 1u << 16,
    OwnedWasiPathCreateDirectory = 1u << 17,
};

struct OwnedWasiContext {
    void *user;
    int32_t (*write)(void *user, int32_t fd, const uint8_t *data, uint32_t length);
    void (*exit)(void *user, int32_t code);
    void (*yield)(void *user);
    uint64_t (*now_ns)(void *user);
    uint32_t (*random)(void *user);
    // Kernel-routed file layer. Each returns a preview1 errno (0 = success);
    // a NULL hook means the host does not offer the layer, and the import
    // then reports ENOSYS rather than pretending it worked.
    int32_t (*path_open)(void *user, int32_t dirfd, const char *path,
                         uint32_t path_len, int32_t oflags, int32_t *out_fd);
    int32_t (*mkdir)(void *user, int32_t dirfd, const char *path,
                     uint32_t path_len);
    int32_t (*read_fd)(void *user, int32_t fd, uint8_t *buf, uint32_t len,
                       uint32_t *done);
    int32_t (*close_fd)(void *user, int32_t fd);
    const char *const *args;
    uint32_t arg_count;
};

uint32_t owned_wasi_import_mask(const Module &module);
/* Bind conservative ENOSYS stubs for any preview1 import outside the
 * profile, and write the space-separated stubbed names into stub_log so the
 * caller can report them (the bridge is arch-blind and cannot print). */
Status bind_generic_wasi_stubs(OwnedSession &session, uint32_t mask,
                               char *stub_log, uint32_t stub_log_cap);
/* stub_log/stub_log_cap receive the space-separated names of any import that
 * received a conservative stub, so the caller can report them. */
Status bind_owned_wasi(OwnedSession &session, OwnedWasiContext &context,
                       uint32_t mask, char *stub_log = nullptr,
                       uint32_t stub_log_cap = 0);

}

#endif
