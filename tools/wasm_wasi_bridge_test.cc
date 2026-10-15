#include "core/wasm/wasi_bridge.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace {

void *host_alloc(uint64_t size, void *) {
    return std::malloc(static_cast<size_t>(size));
}

void host_free(void *pointer, void *) {
    std::free(pointer);
}

bool read_file(const char *path, std::vector<uint8_t> *bytes) {
    std::FILE *file = std::fopen(path, "rb");
    if (!file)
        return false;
    if (std::fseek(file, 0, SEEK_END) != 0) {
        std::fclose(file);
        return false;
    }
    long size = std::ftell(file);
    if (size < 0 || std::fseek(file, 0, SEEK_SET) != 0) {
        std::fclose(file);
        return false;
    }
    bytes->resize(static_cast<size_t>(size));
    bool result = bytes->empty() ||
                  std::fread(bytes->data(), 1, bytes->size(), file) == bytes->size();
    std::fclose(file);
    return result;
}

void append_uleb(std::vector<uint8_t> &output, uint32_t value) {
    do {
        uint8_t byte = static_cast<uint8_t>(value & 0x7f);
        value >>= 7;
        if (value != 0)
            byte |= 0x80;
        output.push_back(byte);
    } while (value != 0);
}

void append_name(std::vector<uint8_t> &output, const char *text) {
    uint32_t length = static_cast<uint32_t>(std::strlen(text));
    append_uleb(output, length);
    output.insert(output.end(), text, text + length);
}

void append_section(std::vector<uint8_t> &output, uint8_t id,
                    const std::vector<uint8_t> &payload) {
    output.push_back(id);
    append_uleb(output, static_cast<uint32_t>(payload.size()));
    output.insert(output.end(), payload.begin(), payload.end());
}

std::vector<uint8_t> make_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> custom;
    append_name(custom, "abi_ver");
    custom.push_back(2);
    append_section(output, 0, custom);
    std::vector<uint8_t> types;
    append_uleb(types, 3);
    types.push_back(0x60);
    append_uleb(types, 4);
    for (uint32_t index = 0; index < 4; ++index)
        types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    types.push_back(0x60);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_uleb(types, 0);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 0);
    append_section(output, 1, types);
    std::vector<uint8_t> imports;
    append_uleb(imports, 2);
    append_name(imports, "wasi_snapshot_preview1");
    append_name(imports, "fd_write");
    imports.push_back(0);
    append_uleb(imports, 0);
    append_name(imports, "wasi_snapshot_preview1");
    append_name(imports, "proc_exit");
    imports.push_back(0);
    append_uleb(imports, 1);
    append_section(output, 2, imports);
    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 2);
    append_section(output, 3, functions);
    std::vector<uint8_t> memory;
    append_uleb(memory, 1);
    memory.push_back(0);
    append_uleb(memory, 1);
    append_section(output, 5, memory);
    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "_start");
    exports.push_back(0);
    append_uleb(exports, 2);
    append_section(output, 7, exports);
    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{
        0x00,
        0x41, 0x00, 0x41, 0x08, 0x36, 0x02, 0x00,
        0x41, 0x04, 0x41, 0x03, 0x36, 0x02, 0x00,
        0x41, 0x01, 0x41, 0x00, 0x41, 0x01, 0x41, 0x10,
        0x10, 0x00, 0x1a,
        0x41, 0x07, 0x10, 0x01,
        0x0b,
    };
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    std::vector<uint8_t> data;
    append_uleb(data, 1);
    append_uleb(data, 0);
    data.push_back(0x41);
    data.push_back(0x08);
    data.push_back(0x0b);
    append_uleb(data, 3);
    data.push_back('h');
    data.push_back('e');
    data.push_back('y');
    append_section(output, 11, data);
    return output;
}

std::vector<uint8_t> make_sched_yield_module(int signature) {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> custom;
    append_name(custom, "abi_ver");
    custom.push_back(2);
    append_section(output, 0, custom);

    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    if (signature == 0) {
        append_uleb(types, 0);
        append_uleb(types, 0);
    } else if (signature == 1) {
        append_uleb(types, 0);
        append_uleb(types, 1);
        types.push_back(0x7f);
    } else {
        append_uleb(types, 1);
        types.push_back(0x7f);
        append_uleb(types, 0);
    }
    append_section(output, 1, types);

    std::vector<uint8_t> imports;
    append_uleb(imports, 1);
    append_name(imports, "wasi_snapshot_preview1");
    append_name(imports, "sched_yield");
    imports.push_back(0);
    append_uleb(imports, 0);
    append_section(output, 2, imports);

    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 0);
    append_section(output, 3, functions);

    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "_start");
    exports.push_back(0);
    append_uleb(exports, 1);
    append_section(output, 7, exports);

    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{0x00};
    if (signature != 0) {
        body.push_back(0x41);
        body.push_back(0x00);
    }
    body.push_back(0x10);
    body.push_back(0x00);
    body.push_back(0x0b);
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}

/* Kernel-routed file layer stubs for the artifact runner. The real routing
 * is engine-neutral (core/fsimport.cc) and is covered live by the kernel
 * gates; what matters here is that a guest importing path_open / fd_read
 * can instantiate and run against a host that offers the layer, so the
 * import SURFACE is proven rather than the filesystem. */
struct RoutedState {
    static constexpr int32_t kFd = 42;
    int32_t next_fd = kFd;
    bool opened = false;
    bool closed = false;
    bool mkdir = false;
    int32_t open_dirfd = -1;
    int32_t open_oflags = -1;
    uint32_t open_path_len = 0;
    uint32_t mkdir_path_len = 0;
    char open_path[64] = {};
    char mkdir_path[64] = {};
    uint8_t read_data[16] = {'f', 'i', 'l', 'e', '\n', 0};
    uint32_t read_len = 5;
};

struct WriteState {
    std::vector<uint8_t> bytes;
    int32_t exit_code = -1;
    RoutedState routed;
};

int32_t open_path_bytes(void *opaque, int32_t dirfd, const char *path,
                        uint32_t path_len, int32_t oflags, int32_t *out_fd) {
    RoutedState *state = &static_cast<WriteState *>(opaque)->routed;
    state->opened = true;
    state->open_dirfd = dirfd;
    state->open_oflags = oflags;
    state->open_path_len = path_len;
    for (uint32_t i = 0; i < path_len && i + 1 < sizeof(state->open_path); ++i)
        state->open_path[i] = path[i];
    state->open_path[path_len < sizeof(state->open_path) ? path_len
                                                         : sizeof(state->open_path) - 1] = 0;
    if (out_fd)
        *out_fd = state->next_fd;
    return 0;
}

int32_t mkdir_bytes(void *opaque, int32_t, const char *path, uint32_t path_len) {
    RoutedState *state = &static_cast<WriteState *>(opaque)->routed;
    state->mkdir = true;
    state->mkdir_path_len = path_len;
    for (uint32_t i = 0; i < path_len && i + 1 < sizeof(state->mkdir_path); ++i)
        state->mkdir_path[i] = path[i];
    state->mkdir_path[path_len < sizeof(state->mkdir_path) ? path_len
                                                            : sizeof(state->mkdir_path) - 1] = 0;
    return 0;
}

int32_t read_fd_bytes(void *opaque, int32_t, uint8_t *buf, uint32_t length,
                      uint32_t *done) {
    RoutedState *state = &static_cast<WriteState *>(opaque)->routed;
    uint32_t count = state->read_len;
    if (count > length)
        count = length;
    for (uint32_t i = 0; i < count; ++i)
        buf[i] = state->read_data[i];
    *done = count;
    return 0;
}

int32_t close_fd_bytes(void *opaque, int32_t) {
    static_cast<WriteState *>(opaque)->routed.closed = true;
    return 0;
}

int32_t write_bytes(void *opaque, int32_t fd, const uint8_t *data, uint32_t length) {
    if (fd != 1 && fd != 2)
        return 8;
    WriteState *state = static_cast<WriteState *>(opaque);
    state->bytes.insert(state->bytes.end(), data, data + length);
    return 0;
}

void exit_bytes(void *opaque, int32_t code) {
    static_cast<WriteState *>(opaque)->exit_code = code;
}

void yield_bytes(void *) {}

uint64_t now_bytes(void *) {
    return 123456789;
}

uint32_t random_bytes(void *) {
    static uint32_t state = 0x12345678;
    state = state * 1664525u + 1013904223u;
    return state;
}

bool run_artifact(const char *path) {
    std::vector<uint8_t> bytes;
    if (!read_file(path, &bytes))
        return false;
    wasm::OwnedSession session;
    wasm::RuntimeConfig config{};
    config.max_values = 1u << 20;
    config.max_frames = 4096;
    config.max_labels = 8192;
    config.max_memory_bytes = 64u * 1024u * 1024u;
    if (session.init(bytes.data(), bytes.size(), host_alloc, host_free, nullptr, config) !=
        wasm::Status::Ok)
        return false;
    WriteState state;
    const char *args[] = {"artifact", nullptr};
    wasm::OwnedWasiContext context{&state,
                                   write_bytes,
                                   exit_bytes,
                                   yield_bytes,
                                   now_bytes,
                                   random_bytes,
                                   open_path_bytes,
                                   mkdir_bytes,
                                   read_fd_bytes,
                                   close_fd_bytes,
                                   args,
                                   1};
    uint32_t mask = wasm::owned_wasi_import_mask(session.module());
    if (mask == 0 || wasm::bind_owned_wasi(session, context, mask) != wasm::Status::Ok)
        return false;
    wasm::RunStatus status = session.start(nullptr, 0);
    if (status != wasm::RunStatus::Completed && status != wasm::RunStatus::Exited)
        return false;
    std::string output(reinterpret_cast<const char *>(state.bytes.data()), state.bytes.size());
    const char *expected = std::strstr(path, "hello2") ? "hello from Rust" :
                           (std::strstr(path, "hello3") ? "hello from Go" : "hello from C");
    return output.find(expected) != std::string::npos && state.exit_code == 0;
}

/* ---- kernel-routed file layer: direct coverage of the new imports ----
 * No boot service imports path_open/fd_read (they all use the ports route
 * via guests/lib), so nothing else in the tree exercises these callbacks.
 * The routing is engine-neutral and covered by the QEMU gates; what is
 * engine-specific, and worth pinning here, is the arg decoding at fixed
 * offsets, the errno pass-through, the fd write-back and the
 * "host does not offer the layer" path.
 *
 * The guest calls, in order, fd_read / path_open / path_create_directory /
 * fd_close, exiting immediately with the first non-zero errno. If all four
 * return 0 it exits with the fd the bridge wrote through the out-param --
 * so a 42 means "reached the host hook AND the write-back worked", and a 52
 * (ENOSYS) means the host offered no layer.
 *
 * Returns the guest's exit code, or -1 if the module would not run. */
int run_routed_module(bool offer_layer, WriteState *state) {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> abi;
    append_name(abi, "abi_ver");
    abi.push_back(2);
    append_section(output, 0, abi);

    std::vector<uint8_t> types;
    append_uleb(types, 6);
    types.push_back(0x60); /* 0: fd_read (i32 x4) -> i32 */
    append_uleb(types, 4);
    for (uint32_t i = 0; i < 4; ++i) types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    types.push_back(0x60); /* 1: fd_close (i32) -> i32 */
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    types.push_back(0x60); /* 2: path_open (5 i32, 2 i64, 2 i32) -> i32 */
    append_uleb(types, 9);
    for (uint32_t i = 0; i < 5; ++i) types.push_back(0x7f);
    types.push_back(0x7e);
    types.push_back(0x7e);
    types.push_back(0x7f);
    types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    types.push_back(0x60); /* 3: proc_exit (i32) -> void */
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_uleb(types, 0);
    types.push_back(0x60); /* 4: path_create_directory (i32 x3) -> i32 */
    append_uleb(types, 3);
    for (uint32_t i = 0; i < 3; ++i) types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    /* 5: _start () -> void. start() invokes _start with NO arguments, so
     * its type must take none; reusing proc_exit's (i32) -> void fails in
     * enter_frame rather than at decode, which is a confusing way to learn
     * this. */
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 0);
    append_section(output, 1, types);

    std::vector<uint8_t> imports;
    append_uleb(imports, 5);
    const struct { const char *field; uint32_t type; } kImports[] = {
        {"fd_read", 0}, {"path_open", 2}, {"path_create_directory", 4},
        {"fd_close", 1}, {"proc_exit", 3},
    };
    for (const auto &entry : kImports) {
        append_name(imports, "wasi_snapshot_preview1");
        append_name(imports, entry.field);
        imports.push_back(0);
        append_uleb(imports, entry.type);
    }
    append_section(output, 2, imports);

    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 5); /* () -> void */
    append_section(output, 3, functions);

    std::vector<uint8_t> memory;
    append_uleb(memory, 1);
    memory.push_back(0);
    append_uleb(memory, 1);
    append_section(output, 5, memory);

    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "_start");
    exports.push_back(0);
    append_uleb(exports, 5);
    append_section(output, 7, exports);

    std::vector<uint8_t> body;
    body.push_back(0x01); /* one local group */
    body.push_back(0x01); /* one local */
    body.push_back(0x7f); /* i32: last errno */
    /* Per call: push args, call, local.tee 0, if -> local.get 0, proc_exit. */
    /* i32.const is decoded as a SIGNED LEB (see core/wasm/wasm.cc: Go emits
     * signed LEB and never the 5-byte unsigned form), so a value like 68
     * must be C4 00 -- encoding it as the single byte 0x44 means -60, and the
     * guest then hands the host a 0xFFFFFFC4 offset and traps. */
    auto call_and_bail = [&body](const std::vector<uint8_t> &args, uint32_t func) {
        body.insert(body.end(), args.begin(), args.end());
        body.push_back(0x10); /* call */
        append_uleb(body, func);
        body.push_back(0x22); /* local.tee */
        append_uleb(body, 0);
        body.push_back(0x04); /* if, void block type */
        body.push_back(0x40);
        body.push_back(0x20); /* local.get */
        append_uleb(body, 0);
        body.push_back(0x10); /* proc_exit */
        append_uleb(body, 4);
        body.push_back(0x0b); /* end if */
    };
    call_and_bail({0x41, 0x09, 0x41, 0x00, 0x41, 0x01, 0x41, 0xC4, 0x00}, 0);
    call_and_bail({0x41, 0x03, 0x41, 0x00, 0x41, 0x10, 0x41, 0x06, 0x41, 0x00,
                   0x42, 0x00, 0x42, 0x00, 0x41, 0x00, 0x41, 0xC0, 0x00}, 1);
    call_and_bail({0x41, 0x03, 0x41, 0x10, 0x41, 0x06}, 2);
    call_and_bail({0x41, 0x09}, 3);
    /* All four clean: exit with the fd the bridge wrote at 64. */
    body.push_back(0x41); /* i32.const 64 */
    body.push_back(0xC0); /* signed LEB: 64 needs two bytes (0x40 == -64) */
    body.push_back(0x00);
    body.push_back(0x28); /* i32.load align=2 offset=0 */
    append_uleb(body, 2);
    append_uleb(body, 0);
    body.push_back(0x10); /* proc_exit */
    append_uleb(body, 4);
    body.push_back(0x0b); /* end function */

    std::vector<uint8_t> code;
    append_uleb(code, 1);
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);

    /* data: iovs at 0 = {32, 4}; the path "/tmp/x" at 16. An active segment
     * is {flags, <offset expr>, size, bytes} and the offset is an
     * initializer expression -- 0x41 <sleb> 0x0b -- not a bare integer.
     * That is the encoding every real toolchain emits for memidx 0, MVP and
     * bulk-memory forms alike. */
    std::vector<uint8_t> data;
    append_uleb(data, 2);
    append_uleb(data, 0); /* flags: active, memidx 0 */
    data.push_back(0x41); /* i32.const */
    data.push_back(0x00); /* offset 0 */
    data.push_back(0x0b); /* end */
    append_uleb(data, 8);
    data.push_back(32); data.push_back(0); data.push_back(0); data.push_back(0);
    data.push_back(4);  data.push_back(0); data.push_back(0); data.push_back(0);
    append_uleb(data, 0); /* flags */
    data.push_back(0x41); /* i32.const */
    data.push_back(16);  /* offset 16, single-byte sleb */
    data.push_back(0x0b); /* end */
    const char *path = "/tmp/x";
    append_uleb(data, 6);
    data.insert(data.end(), path, path + 6);
    append_section(output, 11, data);

    wasm::OwnedSession session;
    wasm::RuntimeConfig config{};
    config.max_values = 256;
    config.max_frames = 16;
    config.max_labels = 32;
    if (session.init(output.data(), output.size(), host_alloc, host_free, nullptr,
                     config) != wasm::Status::Ok)
        return -1;
    wasm::OwnedWasiContext context{
        state,
        nullptr,
        exit_bytes,
        nullptr,
        nullptr,
        nullptr,
        offer_layer ? open_path_bytes : nullptr,
        offer_layer ? mkdir_bytes : nullptr,
        offer_layer ? read_fd_bytes : nullptr,
        offer_layer ? close_fd_bytes : nullptr,
        nullptr,
        0};
    uint32_t mask = wasm::owned_wasi_import_mask(session.module());
    const uint32_t want = wasm::OwnedWasiFdRead | wasm::OwnedWasiPathOpen |
                          wasm::OwnedWasiPathCreateDirectory;
    if ((mask & want) != want)
        return -1;
    if (wasm::bind_owned_wasi(session, context, mask) != wasm::Status::Ok)
        return -1;
    if (session.start(nullptr, 0) != wasm::RunStatus::Exited)
        return -1;
    return state->exit_code;
}

bool expect(bool condition, const char *message) {
    if (condition)
        return true;
    std::fprintf(stderr, "owned WASI bridge test failed: %s\n", message);
    return false;
}

}

int main(int argc, char **argv) {
    /* Kernel-routed file layer, host offers it: all four imports reach the
     * hook with correctly decoded args, and the guest exits with the fd the
     * bridge wrote through the out-param. */
    WriteState routed_state;
    int code = run_routed_module(true, &routed_state);
    if (code != RoutedState::kFd) {
        std::fprintf(stderr, "routed layer: guest exit code %d (want %d = fd written back)\n",
                     code, RoutedState::kFd);
        return 1;
    }
    if (!expect(routed_state.routed.opened, "routed layer: path_open reached"))
        return 1;
    if (!expect(routed_state.routed.open_dirfd == 3,
                "routed layer: path_open dirfd decoded"))
        return 1;
    if (!expect(std::strcmp(routed_state.routed.open_path, "/tmp/x") == 0,
                "routed layer: path_open path decoded"))
        return 1;
    if (!expect(routed_state.routed.open_path_len == 6,
                "routed layer: path_open path_len decoded"))
        return 1;
    if (!expect(routed_state.routed.mkdir &&
                    std::strcmp(routed_state.routed.mkdir_path, "/tmp/x") == 0,
                "routed layer: path_create_directory decoded"))
        return 1;
    if (!expect(routed_state.routed.closed, "routed layer: fd_close routed"))
        return 1;

    /* Same module, host offers no layer: every import must report ENOSYS
     * (52) rather than trapping or reporting a success it did not do. The
     * guest exits with the first errno, and fd_read is called first. */
    WriteState absent_state;
    if (!expect(run_routed_module(false, &absent_state) == 52,
                "routed layer: absent host reports ENOSYS"))
        return 1;
    if (!expect(!absent_state.routed.opened,
                "routed layer: absent host touches no hook"))
        return 1;

    std::vector<uint8_t> bytes = make_module();
    wasm::OwnedSession session;
    wasm::RuntimeConfig config{};
    config.max_values = 128;
    config.max_frames = 8;
    config.max_labels = 16;
    if (!expect(session.init(bytes.data(), bytes.size(), host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "bridge session init"))
        return 1;
    WriteState state;
    /* No file-layer hooks here on purpose: this session is the coverage
     * for "host does not offer the layer", and the calls must report
     * ENOSYS (52) rather than trap or lie about success. */
    wasm::OwnedWasiContext context{&state,
                                   write_bytes,
                                   exit_bytes,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   nullptr,
                                   0};
    if (!expect(wasm::bind_owned_wasi(session, context,
                                     wasm::OwnedWasiFdWrite | wasm::OwnedWasiProcExit) == wasm::Status::Ok,
                "bridge import binding"))
        return 1;
    if (!expect(session.start(nullptr, 0) == wasm::RunStatus::Exited,
                "bridge proc_exit status"))
        return 1;
    if (!expect(state.exit_code == 7, "bridge exit code"))
        return 1;
    if (!expect(state.bytes.size() == 3 && state.bytes[0] == 'h' &&
                    state.bytes[1] == 'e' && state.bytes[2] == 'y',
                "bridge fd_write data"))
        return 1;
    if (!expect(session.runtime().memory()[16] == 3 &&
                    session.runtime().memory()[17] == 0,
                "bridge fd_write nwritten"))
        return 1;

    std::vector<uint8_t> yield_bytes_module = make_sched_yield_module(0);
    wasm::OwnedSession yield_session;
    if (!expect(yield_session.init(yield_bytes_module.data(), yield_bytes_module.size(),
                                   host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "sched_yield session init"))
        return 1;
    WriteState yield_state;
    wasm::OwnedWasiContext yield_context{&yield_state,
                                         write_bytes,
                                         exit_bytes,
                                         yield_bytes,
                                         nullptr,
                                         nullptr,
                                         nullptr,
                                         nullptr,
                                         nullptr,
                                         nullptr,
                                         nullptr,
                                         0};
    if (!expect(wasm::bind_owned_wasi(yield_session, yield_context,
                                      wasm::OwnedWasiSchedYield) == wasm::Status::Ok,
                "sched_yield typed binding"))
        return 1;
    if (!expect(yield_session.start(nullptr, 0) == wasm::RunStatus::Completed,
                "sched_yield execution"))
        return 1;

    std::vector<uint8_t> bad_yield_module = make_sched_yield_module(2);
    wasm::OwnedSession bad_yield_session;
    if (!expect(bad_yield_session.init(bad_yield_module.data(), bad_yield_module.size(),
                                       host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "bad sched_yield session init"))
        return 1;
    if (!expect(wasm::bind_owned_wasi(bad_yield_session, yield_context,
                                      wasm::OwnedWasiSchedYield) == wasm::Status::TypeMismatch,
                "sched_yield signature rejection"))
        return 1;

    for (int index = 1; index < argc; ++index)
        if (!expect(run_artifact(argv[index]), "artifact WASI execution"))
            return 1;
    std::puts("owned WASI bridge test: PASS");
    return 0;
}
