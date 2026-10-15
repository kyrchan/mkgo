#include "wasi_bridge.h"

namespace wasm {
namespace {

constexpr int32_t kErrBadf = 8;
constexpr int32_t kErrInval = 28;
constexpr int32_t kErrNosys = 52;
/* Matches FSIMPORT_PATH_MAX. Duplicated rather than included because this
 * bridge is engine-side and must not pull in a kernel header (AGENTS rule 8);
 * fsimport_path_open clamps again on its side. */
constexpr uint32_t kRoutedPathMax = 192;
/* Root preopen fd; see fd_prestat_get for why it must be advertised. */
constexpr int32_t kPreopenFd = 3;

bool slice_equal(const Slice &slice, const char *text) {
    if (!slice.data || !text)
        return false;
    uint32_t length = 0;
    while (text[length] != 0)
        ++length;
    if (slice.size != length)
        return false;
    for (uint32_t index = 0; index < length; ++index)
        if (slice.data[index] != static_cast<uint8_t>(text[index]))
            return false;
    return true;
}

/* Conservative linkage stub for a preview1 import outside the profile.
 * The frozen profile is a behavioural contract, not a linkage contract:
 * stock runtimes import filestat, seek, readdir and more, and a module that
 * cannot link simply refuses to instantiate. The m3 bridge's documented
 * policy is "everything else in the namespace gets a conservative ENOSYS
 * stub so stock toolchain runtimes still LINK" -- the owned engine needs
 * the same, or a guest that links on wasm3 fails here with "unbound
 * imports" and the failure reads like an engine bug.
 *
 * The stub's shape comes from the module's OWN declaration (result count
 * and type), never from ours, so it round-trips whatever the guest
 * declared. Only wasi_snapshot_preview1 gets this: an unknown kernel import
 * is still a hard error, because no kernel-side contract exists for it. */
RunStatus generic_stub(void *, Runtime &, const Value *, Value *results,
                       uint32_t result_count, uint32_t *) {
    for (uint32_t index = 0; index < result_count; ++index)
        results[index].bits = 0;
    if (result_count >= 1)
        results[0].bits = static_cast<uint32_t>(kErrNosys);
    return RunStatus::Completed;
}

bool text_equal(const char *a, const char *b) {
    if (!a || !b)
        return false;
    while (*a && *a == *b) {
        a++;
        b++;
    }
    return *a == *b;
}

bool has_import(const Module &module, const char *module_name, const char *field_name) {
    const ModuleData &data = module.data();
    for (uint32_t index = 0; index < data.import_count; ++index) {
        const Import &import = data.imports[index];
        if (import.kind == 0 && slice_equal(import.module_name, module_name) &&
            slice_equal(import.field_name, field_name))
            return true;
    }
    return false;
}

bool memory_range(const Runtime &runtime, uint64_t address, uint64_t size) {
    return runtime.memory() != nullptr && address <= runtime.memory_size() &&
           size <= runtime.memory_size() - address;
}

uint32_t read_u32(const uint8_t *bytes) {
    return static_cast<uint32_t>(bytes[0]) |
           (static_cast<uint32_t>(bytes[1]) << 8) |
           (static_cast<uint32_t>(bytes[2]) << 16) |
           (static_cast<uint32_t>(bytes[3]) << 24);
}

void write_u32(uint8_t *bytes, uint32_t value) {
    bytes[0] = static_cast<uint8_t>(value);
    bytes[1] = static_cast<uint8_t>(value >> 8);
    bytes[2] = static_cast<uint8_t>(value >> 16);
    bytes[3] = static_cast<uint8_t>(value >> 24);
}

void write_u64(uint8_t *bytes, uint64_t value) {
    for (uint32_t index = 0; index < 8; ++index)
        bytes[index] = static_cast<uint8_t>(value >> (index * 8));
}

uint32_t args_size(const OwnedWasiContext *context, uint64_t *buffer_size) {
    uint32_t count = 0;
    uint64_t size = 0;
    if (!context)
        return 0;
    for (uint32_t index = 0; index < context->arg_count; ++index) {
        const char *text = context->args ? context->args[index] : nullptr;
        if (!text)
            break;
        uint64_t length = 0;
        while (text[length] != 0) {
            if (length == UINT32_MAX)
                return UINT32_MAX;
            ++length;
        }
        if (size > UINT32_MAX - length - 1)
            return UINT32_MAX;
        size += length + 1;
        ++count;
    }
    *buffer_size = size;
    return count;
}

RunStatus fd_write(void *opaque, Runtime &runtime, const Value *args,
                   Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !opaque || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    if (!context->write)
        return RunStatus::Error;
    int32_t fd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t iovs = static_cast<uint32_t>(args[1].bits);
    uint32_t count = static_cast<uint32_t>(args[2].bits);
    uint64_t written_address = static_cast<uint32_t>(args[3].bits);
    if (!memory_range(runtime, iovs, static_cast<uint64_t>(count) * 8) ||
        !memory_range(runtime, written_address, 4)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    uint32_t total = 0;
    for (uint32_t index = 0; index < count; ++index) {
        uint64_t vector = iovs + static_cast<uint64_t>(index) * 8;
        uint32_t data_address = read_u32(runtime.memory() + vector);
        uint32_t length = read_u32(runtime.memory() + vector + 4);
        if (!memory_range(runtime, data_address, length)) {
            runtime.set_error(Status::TrapOutOfBounds);
            return RunStatus::Trapped;
        }
        int32_t error = context->write(context->user, fd,
                                       runtime.memory() + data_address, length);
        if (error != 0) {
            results[0].bits = static_cast<uint32_t>(error);
            return RunStatus::Completed;
        }
        if (total > UINT32_MAX - length) {
            runtime.set_error(Status::TrapOutOfBounds);
            return RunStatus::Trapped;
        }
        total += length;
    }
    write_u32(runtime.memory() + written_address, total);
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus proc_exit(void *opaque, Runtime &, const Value *args,
                    Value *, uint32_t result_count, uint32_t *exit_code) {
    if (result_count != 0 || !opaque || !args || !exit_code)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    if (!context->exit)
        return RunStatus::Error;
    int32_t code = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    context->exit(context->user, code);
    *exit_code = static_cast<uint32_t>(code);
    return RunStatus::Exited;
}

RunStatus sched_yield(void *opaque, Runtime &, const Value *,
                      Value *results, uint32_t result_count, uint32_t *) {
    if (result_count > 1 || !opaque)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    if (!context->yield)
        return RunStatus::Error;
    context->yield(context->user);
    if (result_count == 1)
        results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus clock_time_get(void *opaque, Runtime &runtime, const Value *args,
                         Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !opaque || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    if (!context->now_ns)
        return RunStatus::Error;
    uint32_t clock_id = static_cast<uint32_t>(args[0].bits);
    uint64_t address = static_cast<uint32_t>(args[2].bits);
    if (clock_id > 3) {
        results[0].bits = kErrInval;
        return RunStatus::Completed;
    }
    if (!memory_range(runtime, address, 8)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    write_u64(runtime.memory() + address, context->now_ns(context->user));
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus random_get(void *opaque, Runtime &runtime, const Value *args,
                     Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !opaque || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    if (!context->random)
        return RunStatus::Error;
    uint64_t address = static_cast<uint32_t>(args[0].bits);
    uint32_t length = static_cast<uint32_t>(args[1].bits);
    if (!memory_range(runtime, address, length)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    for (uint32_t index = 0; index < length; ++index)
        runtime.memory()[address + index] =
            static_cast<uint8_t>(context->random(context->user) >> ((index & 3) * 8));
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus args_sizes_get(void *opaque, Runtime &runtime, const Value *args,
                         Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !opaque || !args)
        return RunStatus::Error;
    uint64_t size = 0;
    uint32_t count = args_size(static_cast<const OwnedWasiContext *>(opaque), &size);
    uint64_t pointers = static_cast<uint32_t>(args[0].bits);
    uint64_t buffer = static_cast<uint32_t>(args[1].bits);
    if (count == UINT32_MAX ||
        !memory_range(runtime, pointers, static_cast<uint64_t>(count) * 4) ||
        !memory_range(runtime, buffer, size)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    write_u32(runtime.memory() + pointers, count);
    write_u32(runtime.memory() + buffer, static_cast<uint32_t>(size));
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus args_get(void *opaque, Runtime &runtime, const Value *args,
                   Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !opaque || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    uint64_t size = 0;
    uint32_t count = args_size(context, &size);
    uint64_t pointers = static_cast<uint32_t>(args[0].bits);
    uint64_t buffer = static_cast<uint32_t>(args[1].bits);
    if (count == UINT32_MAX ||
        !memory_range(runtime, pointers, static_cast<uint64_t>(count) * 4) ||
        !memory_range(runtime, buffer, size)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    uint64_t offset = 0;
    for (uint32_t index = 0; index < count; ++index) {
        const char *text = context->args[index];
        uint64_t length = 0;
        while (text[length] != 0)
            ++length;
        write_u32(runtime.memory() + pointers + static_cast<uint64_t>(index) * 4,
                  static_cast<uint32_t>(buffer + offset));
        for (uint64_t byte = 0; byte <= length; ++byte)
            runtime.memory()[buffer + offset + byte] =
                static_cast<uint8_t>(text[byte]);
        offset += length + 1;
    }
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus environ_sizes_get(void *, Runtime &runtime, const Value *args,
                            Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    uint64_t count_address = static_cast<uint32_t>(args[0].bits);
    uint64_t size_address = static_cast<uint32_t>(args[1].bits);
    if (!memory_range(runtime, count_address, 4) || !memory_range(runtime, size_address, 4)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    write_u32(runtime.memory() + count_address, 0);
    write_u32(runtime.memory() + size_address, 0);
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus environ_get(void *, Runtime &, const Value *,
                      Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1)
        return RunStatus::Error;
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus poll_oneoff(void *, Runtime &runtime, const Value *args,
                      Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    uint64_t nevents = static_cast<uint32_t>(args[3].bits);
    if (!memory_range(runtime, nevents, 4)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    write_u32(runtime.memory() + nevents, 0);
    results[0].bits = 0;
    return RunStatus::Completed;
}

/* ---- kernel-routed preview1 file layer (AGENTS rule 4) ----
 * These four are the v2 convenience that lets a stock Go guest call
 * os.Open without knowing about ports. They are NOT in the frozen profile
 * and the host hooks may be absent, in which case each reports ENOSYS
 * instead of pretending to have worked. The routing, fd table and error
 * mapping are engine-neutral (core/fsimport.cc); everything here is guest
 * memory access, which is the one part that is engine-specific. */

RunStatus fd_read(void *opaque, Runtime &runtime, const Value *args,
                  Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    int32_t fd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t iovs = static_cast<uint32_t>(args[1].bits);
    uint32_t count = static_cast<uint32_t>(args[2].bits);
    uint64_t read_address = static_cast<uint32_t>(args[3].bits);
    if (fd < 3) {
        /* stdin is not implemented in v1, matching the wasm3 bridge. */
        results[0].bits = static_cast<uint32_t>(kErrBadf);
        return RunStatus::Completed;
    }
    if (!memory_range(runtime, iovs, static_cast<uint64_t>(count) * 8) ||
        !memory_range(runtime, read_address, 4)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    if (!context->read_fd) {
        results[0].bits = static_cast<uint32_t>(kErrNosys);
        return RunStatus::Completed;
    }
    /* Single-iovec fast path, as in the wasm3 bridge: a routed read is one
     * datagram anyway, and the clamp lives in the host. */
    uint64_t vector = iovs;
    uint32_t data_address = read_u32(runtime.memory() + vector);
    uint32_t length = read_u32(runtime.memory() + vector + 4);
    if (!memory_range(runtime, data_address, length)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    uint32_t done = 0;
    int32_t error = context->read_fd(context->user, fd,
                                     runtime.memory() + data_address, length,
                                     &done);
    if (error != 0) {
        results[0].bits = static_cast<uint32_t>(error);
        return RunStatus::Completed;
    }
    write_u32(runtime.memory() + read_address, done);
    results[0].bits = 0;
    return RunStatus::Completed;
}

/* Copy a guest path into a bounded kernel buffer. Returns false when the
 * guest path is out of bounds, unterminated within the bound, or empty --
 * the caller turns that into EINVAL, as the wasm3 bridge did. */
bool fetch_routed_path(Runtime &runtime, uint64_t address, int32_t length,
                       char *out, uint32_t capacity) {
    if (length < 0 || static_cast<uint32_t>(length) >= capacity)
        return false;
    if (!memory_range(runtime, address, static_cast<uint64_t>(length) + 1))
        return false;
    const uint8_t *source = runtime.memory() + address;
    if (source[static_cast<uint32_t>(length)] != 0)
        return false;
    for (int32_t index = 0; index < length; ++index)
        out[index] = static_cast<char>(source[index]);
    out[length] = 0;
    return true;
}

RunStatus path_open(void *opaque, Runtime &runtime, const Value *args,
                    Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    int32_t dirfd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t path_address = static_cast<uint32_t>(args[2].bits);
    int32_t path_length = static_cast<int32_t>(static_cast<uint32_t>(args[3].bits));
    int32_t oflags = static_cast<int32_t>(static_cast<uint32_t>(args[4].bits));
    uint64_t opened_address = static_cast<uint32_t>(args[8].bits);
    if (!memory_range(runtime, opened_address, 4)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    char path[kRoutedPathMax];
    if (!fetch_routed_path(runtime, path_address, path_length, path,
                           sizeof(path))) {
        results[0].bits = static_cast<uint32_t>(kErrInval);
        return RunStatus::Completed;
    }
    if (!context->path_open) {
        results[0].bits = static_cast<uint32_t>(kErrNosys);
        return RunStatus::Completed;
    }
    int32_t fd = 0;
    int32_t error = context->path_open(context->user, dirfd, path,
                                       static_cast<uint32_t>(path_length),
                                       oflags, &fd);
    if (error != 0) {
        results[0].bits = static_cast<uint32_t>(error);
        return RunStatus::Completed;
    }
    write_u32(runtime.memory() + opened_address, static_cast<uint32_t>(fd));
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus path_create_directory(void *opaque, Runtime &runtime,
                                const Value *args, Value *results,
                                uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    int32_t dirfd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t path_address = static_cast<uint32_t>(args[1].bits);
    int32_t path_length = static_cast<int32_t>(static_cast<uint32_t>(args[2].bits));
    char path[kRoutedPathMax];
    if (!fetch_routed_path(runtime, path_address, path_length, path,
                           sizeof(path))) {
        results[0].bits = static_cast<uint32_t>(kErrInval);
        return RunStatus::Completed;
    }
    if (!context->mkdir) {
        results[0].bits = static_cast<uint32_t>(kErrNosys);
        return RunStatus::Completed;
    }
    results[0].bits = static_cast<uint32_t>(context->mkdir(
        context->user, dirfd, path, static_cast<uint32_t>(path_length)));
    return RunStatus::Completed;
}

RunStatus fd_close(void *opaque, Runtime &, const Value *args,
                   Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    const OwnedWasiContext *context = static_cast<const OwnedWasiContext *>(opaque);
    int32_t fd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    /* A routed fd must be released or the session's table fills up; the
     * wasm3 bridge bound the routed variant last so it won over its own
     * stub. Stdio and the root preopen are not routed and just succeed. */
    if (context->close_fd && fd >= 3)
        results[0].bits = static_cast<uint32_t>(
            context->close_fd(context->user, fd));
    else
        results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus fd_fdstat_get(void *, Runtime &runtime, const Value *args,
                        Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    uint32_t fd = static_cast<uint32_t>(args[0].bits);
    uint64_t address = static_cast<uint32_t>(args[1].bits);
    if (!memory_range(runtime, address, 24)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    uint8_t *data = runtime.memory() + address;
    for (uint32_t index = 0; index < 24; ++index)
        data[index] = 0;
    if (fd == 1 || fd == 2)
        data[0] = 2; /* character_device */
    else if (fd == kPreopenFd)
        data[0] = 3; /* directory: the root preopen */
    write_u64(data + 8, UINT64_MAX);
    write_u64(data + 16, UINT64_MAX);
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus fd_fdstat_set_flags(void *, Runtime &, const Value *,
                             Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1)
        return RunStatus::Error;
    results[0].bits = 0;
    return RunStatus::Completed;
}

/* Root preopen on fd 3. This is what lets a stock runtime resolve a
 * RELATIVE path: Go's os.Open("/etc/motd") against the "/" preopen hands
 * us the remnant "etc/motd" with dirfd = 3, and the kernel-routed transport
 * is absolute-path based, so path_open rebases it (fsimport_rebase_path).
 * Without the preopen advertised there is no such fd, the guest gets EBADF
 * from its preopen scan, and every relative open resolves against nothing --
 * which is exactly how p10a's "open /home/u1/via-route1.txt" turned into
 * "open home/u1/..." and then ENOENT on the owned engine. */
RunStatus fd_prestat_get(void *, Runtime &runtime, const Value *args,
                         Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    int32_t fd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t address = static_cast<uint32_t>(args[1].bits);
    if (fd != kPreopenFd) {
        results[0].bits = static_cast<uint32_t>(kErrBadf);
        return RunStatus::Completed;
    }
    if (!memory_range(runtime, address, 8)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    uint8_t *prestat = runtime.memory() + address;
    for (uint32_t index = 0; index < 8; ++index)
        prestat[index] = 0;
    prestat[0] = 0; /* preopentype_dir */
    prestat[4] = 1; /* pr_name_len = 1, little endian */
    results[0].bits = 0;
    return RunStatus::Completed;
}

RunStatus fd_prestat_dir_name(void *, Runtime &runtime, const Value *args,
                              Value *results, uint32_t result_count, uint32_t *) {
    if (result_count != 1 || !args)
        return RunStatus::Error;
    int32_t fd = static_cast<int32_t>(static_cast<uint32_t>(args[0].bits));
    uint64_t address = static_cast<uint32_t>(args[1].bits);
    int32_t length = static_cast<int32_t>(static_cast<uint32_t>(args[2].bits));
    if (fd != kPreopenFd) {
        results[0].bits = static_cast<uint32_t>(kErrBadf);
        return RunStatus::Completed;
    }
    if (length < 1) {
        results[0].bits = static_cast<uint32_t>(kErrInval);
        return RunStatus::Completed;
    }
    if (!memory_range(runtime, address, 1)) {
        runtime.set_error(Status::TrapOutOfBounds);
        return RunStatus::Trapped;
    }
    runtime.memory()[address] = '/';
    results[0].bits = 0;
    return RunStatus::Completed;
}

FuncType i32_result_type(uint32_t params) {
    FuncType type{};
    type.param_count = params;
    for (uint32_t index = 0; index < params; ++index)
        type.params[index] = ValueType::I32;
    type.result_count = 1;
    type.results[0] = ValueType::I32;
    return type;
}

FuncType clock_type() {
    FuncType type{};
    type.param_count = 3;
    type.params[0] = ValueType::I32;
    type.params[1] = ValueType::I64;
    type.params[2] = ValueType::I32;
    type.result_count = 1;
    type.results[0] = ValueType::I32;
    return type;
}

FuncType fd_write_type() {
    return i32_result_type(4);
}

/* path_open: (i32 dirfd, i32 dirflags, i32 path, i32 path_len, i32 oflags,
 * i64 rights_base, i64 rights_inheriting, i32 fdflags, i32 opened_fd) -> i32 */
FuncType path_open_type() {
    FuncType type{};
    type.param_count = 9;
    type.params[0] = ValueType::I32;
    type.params[1] = ValueType::I32;
    type.params[2] = ValueType::I32;
    type.params[3] = ValueType::I32;
    type.params[4] = ValueType::I32;
    type.params[5] = ValueType::I64;
    type.params[6] = ValueType::I64;
    type.params[7] = ValueType::I32;
    type.params[8] = ValueType::I32;
    type.result_count = 1;
    type.results[0] = ValueType::I32;
    return type;
}

FuncType proc_exit_type() {
    FuncType type{};
    type.param_count = 1;
    type.params[0] = ValueType::I32;
    return type;
}

Status bind_one(OwnedSession &session, uint32_t mask, uint32_t bit,
                const char *field, const FuncType &type, ImportInvoke invoke,
                OwnedWasiContext &context) {
    if ((mask & bit) == 0)
        return Status::Ok;
    if (!has_import(session.module(), "wasi_snapshot_preview1", field))
        return Status::MissingImport;
    return session.bind_import_typed("wasi_snapshot_preview1", field, &type,
                                     invoke, &context);
}

Status bind_sched_yield(OwnedSession &session, OwnedWasiContext &context) {
    const ModuleData &data = session.module().data();
    bool found = false;
    for (uint32_t index = 0; index < data.import_count; ++index) {
        const Import &import = data.imports[index];
        if (import.kind != 0 ||
            !slice_equal(import.module_name, "wasi_snapshot_preview1") ||
            !slice_equal(import.field_name, "sched_yield"))
            continue;
        if (import.type_index >= data.type_count)
            return Status::TypeMismatch;
        const FuncType &actual = data.types[import.type_index];
        if (actual.param_count != 0 || actual.result_count > 1 ||
            (actual.result_count == 1 &&
             actual.results[0] != ValueType::I32))
            return Status::TypeMismatch;
        found = true;
    }
    if (!found)
        return Status::MissingImport;
    return session.bind_import("wasi_snapshot_preview1", "sched_yield",
                               sched_yield, &context);
}

}

/* Single source of truth for the profile: name -> mask bit. The mask says
 * which imports a module actually uses; the stub binder needs the same
 * table to know which names it must NOT clobber. */
struct WasiName {
    const char *field;
    uint32_t bit;
};
static const WasiName kWasiNames[] = {
    {"sched_yield", OwnedWasiSchedYield},
    {"proc_exit", OwnedWasiProcExit},
    {"args_get", OwnedWasiArgsGet},
    {"args_sizes_get", OwnedWasiArgsSizesGet},
    {"clock_time_get", OwnedWasiClockTimeGet},
    {"environ_get", OwnedWasiEnvironGet},
    {"environ_sizes_get", OwnedWasiEnvironSizesGet},
    {"fd_write", OwnedWasiFdWrite},
    {"random_get", OwnedWasiRandomGet},
    {"poll_oneoff", OwnedWasiPollOneoff},
    {"fd_close", OwnedWasiFdClose},
    {"fd_fdstat_get", OwnedWasiFdFdstatGet},
    {"fd_fdstat_set_flags", OwnedWasiFdFdstatSetFlags},
    {"fd_prestat_get", OwnedWasiFdPrestatGet},
    {"fd_prestat_dir_name", OwnedWasiFdPrestatDirName},
    {"fd_read", OwnedWasiFdRead},
    {"path_open", OwnedWasiPathOpen},
    {"path_create_directory", OwnedWasiPathCreateDirectory},
};

static bool mask_has(uint32_t mask, const char *field) {
    for (const auto &entry : kWasiNames)
        if (text_equal(field, entry.field))
            return (mask & entry.bit) != 0;
    return false;
}

uint32_t owned_wasi_import_mask(const Module &module) {
    uint32_t mask = 0;
    for (const auto &entry : kWasiNames)
        if (has_import(module, "wasi_snapshot_preview1", entry.field))
            mask |= entry.bit;
    return mask;
}

/* Copy a name slice into a NUL-terminated buffer. Import names are slices
 * into the blob with no terminator, so they cannot be handed to the C-string
 * bind API directly. Returns false if it does not fit. */
bool slice_to_buffer(const Slice &slice, char *out, uint32_t capacity) {
    if (!slice.data || slice.size + 1 > capacity)
        return false;
    for (uint32_t index = 0; index < slice.size; ++index)
        out[index] = static_cast<char>(slice.data[index]);
    out[slice.size] = 0;
    return true;
}

/* Bind conservative stubs for every preview1 import the profile did not
 * claim. `stub_log` receives the space-separated names so the caller can
 * print them; this file is arch-blind and has no console. */
Status bind_generic_wasi_stubs(OwnedSession &session, uint32_t mask,
                               char *stub_log, uint32_t stub_log_cap) {
    const ModuleData &data = session.module().data();
    uint32_t written = 0;
    if (stub_log && stub_log_cap)
        stub_log[0] = 0;
    for (uint32_t index = 0; index < data.import_count; ++index) {
        const Import &import = data.imports[index];
        if (import.kind != 0 ||
            !slice_equal(import.module_name, "wasi_snapshot_preview1"))
            continue;
        char name[80];
        if (!slice_to_buffer(import.field_name, name, sizeof(name)))
            return Status::Malformed;
        if (mask_has(mask, name))
            continue; /* the profile already bound it; never clobber */
        if (import.type_index >= data.type_count)
            return Status::TypeMismatch;
        const FuncType &type = data.types[import.type_index];
        if (type.result_count > 1)
            return Status::TypeMismatch;
        Status status = session.bind_import_typed("wasi_snapshot_preview1",
                                                  name, &type, generic_stub,
                                                  nullptr);
        if (status != Status::Ok)
            return status;
        if (stub_log) {
            uint32_t length = 0;
            while (name[length])
                length++;
            if (written + length + 2 <= stub_log_cap) {
                for (uint32_t at = 0; at < length; ++at)
                    stub_log[written + at] = name[at];
                stub_log[written + length] = ' ';
                written += length + 1;
                stub_log[written] = 0;
            }
        }
    }
    return Status::Ok;
}

Status bind_owned_wasi(OwnedSession &session, OwnedWasiContext &context,
                       uint32_t mask, char *stub_log, uint32_t stub_log_cap) {
    uint32_t bound = 0;
    FuncType i32 = i32_result_type(2);
    FuncType i32_one = i32_result_type(1);
    FuncType i32_three = i32_result_type(3);
    FuncType i32_four = fd_write_type();
    Status status = Status::Ok;
    if ((mask & OwnedWasiSchedYield) != 0) {
        status = bind_sched_yield(session, context);
        if (status != Status::Ok)
            return status;
    }
    bound += (mask & OwnedWasiSchedYield) != 0;
    status = bind_one(session, mask, OwnedWasiProcExit, "proc_exit",
                      proc_exit_type(), proc_exit, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiProcExit) != 0;
    status = bind_one(session, mask, OwnedWasiArgsGet, "args_get", i32,
                      args_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiArgsGet) != 0;
    status = bind_one(session, mask, OwnedWasiArgsSizesGet, "args_sizes_get", i32,
                      args_sizes_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiArgsSizesGet) != 0;
    status = bind_one(session, mask, OwnedWasiClockTimeGet, "clock_time_get",
                      clock_type(), clock_time_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiClockTimeGet) != 0;
    status = bind_one(session, mask, OwnedWasiEnvironGet, "environ_get", i32,
                      environ_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiEnvironGet) != 0;
    status = bind_one(session, mask, OwnedWasiEnvironSizesGet, "environ_sizes_get",
                      i32, environ_sizes_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiEnvironSizesGet) != 0;
    status = bind_one(session, mask, OwnedWasiFdWrite, "fd_write", i32_four,
                      fd_write, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdWrite) != 0;
    status = bind_one(session, mask, OwnedWasiRandomGet, "random_get", i32,
                      random_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiRandomGet) != 0;
    status = bind_one(session, mask, OwnedWasiPollOneoff, "poll_oneoff", i32_four,
                      poll_oneoff, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiPollOneoff) != 0;
    status = bind_one(session, mask, OwnedWasiFdClose, "fd_close", i32_one,
                      fd_close, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdClose) != 0;
    status = bind_one(session, mask, OwnedWasiFdRead, "fd_read", i32_four,
                      fd_read, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdRead) != 0;
    status = bind_one(session, mask, OwnedWasiPathOpen, "path_open",
                      path_open_type(), path_open, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiPathOpen) != 0;
    status = bind_one(session, mask, OwnedWasiPathCreateDirectory,
                      "path_create_directory", i32_three, path_create_directory,
                      context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiPathCreateDirectory) != 0;
    status = bind_one(session, mask, OwnedWasiFdFdstatGet, "fd_fdstat_get", i32,
                      fd_fdstat_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdFdstatGet) != 0;
    status = bind_one(session, mask, OwnedWasiFdFdstatSetFlags,
                      "fd_fdstat_set_flags", i32, fd_fdstat_set_flags, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdFdstatSetFlags) != 0;
    status = bind_one(session, mask, OwnedWasiFdPrestatGet, "fd_prestat_get", i32,
                      fd_prestat_get, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdPrestatGet) != 0;
    status = bind_one(session, mask, OwnedWasiFdPrestatDirName,
                      "fd_prestat_dir_name", i32_three, fd_prestat_dir_name, context);
    if (status != Status::Ok)
        return status;
    bound += (mask & OwnedWasiFdPrestatDirName) != 0;
    /* Anything left in the preview1 namespace gets a conservative stub, so a
     * guest that links on wasm3 also links here. */
    status = bind_generic_wasi_stubs(session, mask, stub_log, stub_log_cap);
    if (status != Status::Ok)
        return status;
    return bound == 0 ? Status::MissingImport : Status::Ok;
}

}
