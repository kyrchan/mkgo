#include "core/wasm/wasm.h"
#include "core/wasm/owned_session.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <vector>

namespace {

uint64_t live_bytes;
uint64_t peak_bytes;
uint64_t alloc_count;

/* Counting allocator. Instantiation footprint is a shipped budget
 * (AGENTS.md Phase 20 boot/code/RAM budgets), and a guest that merely
 * decodes fine can still exhaust the mm pool at runtime: the owned engine
 * holds lowered IR, per-function metadata, the value stack and the guest
 * linear-memory reserve at once, and every one of those scales with the
 * artifact. Measuring the peak here is the only way to see a regression
 * before a boot gate fails with a bare out-of-memory. */
void *counting_alloc(uint64_t size, void *) {
    void *pointer = std::malloc(static_cast<size_t>(size) + 16);
    if (!pointer)
        return nullptr;
    *static_cast<uint64_t *>(pointer) = size;
    live_bytes += size;
    if (live_bytes > peak_bytes)
        peak_bytes = live_bytes;
    alloc_count++;
    return static_cast<uint8_t *>(pointer) + 16;
}

void counting_free(void *pointer, void *) {
    if (!pointer)
        return;
    void *base = static_cast<uint8_t *>(pointer) - 16;
    live_bytes -= *static_cast<uint64_t *>(base);
    std::free(base);
}

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
    bool result = bytes->empty() || std::fread(bytes->data(), 1, bytes->size(), file) == bytes->size();
    std::fclose(file);
    return result;
}

}

int main(int argc, char **argv) {
    if (argc < 2)
        return 2;
    /* The kernel's owned-engine configuration (core/engine.cc). A budget
     * measured under a different config would not be the boot budget. */
    wasm::RuntimeConfig config{};
    config.max_values = 1u << 16;
    config.max_frames = 2048;
    config.max_labels = 4096;
    config.max_table_entries = 1u << 18;
    config.max_memory_bytes = 64u * 1024u * 1024u;
    /* Instantiation peak budget for a BOOT service. The owned engine holds
     * lowered IR at ~10-12x the module blob (measured across the tree), so
     * this is the real per-session pool cost the scheduler has to fit.
     * 96 MiB covers the worst shipped boot service (shell.wasm, 83 MiB: 49
     * of IR plus its 34 MiB declared minimum) with headroom, and catches a
     * regression in any service. */
    const uint64_t budget_bytes = 96ull * 1024 * 1024;
    /* Artifacts that are not boot services are measured and printed but
     * budget-exempt, by exact path so a new heavy artifact cannot slip in
     * under a stale entry. services/goc/go.wasm is the Go toolchain test
     * binary (13431 functions, 221 MiB) -- it exercises the decoder, not
     * the boot path, and no image ever spawns it. */
    const char *const budget_exempt[] = {"services/goc/go.wasm"};
    int failures = 0;
    for (int index = 1; index < argc; ++index) {
        std::vector<uint8_t> bytes;
        if (!read_file(argv[index], &bytes)) {
            std::fprintf(stderr, "wasm artifact read failed: %s\n", argv[index]);
            return 1;
        }
        wasm::Module module;
        wasm::Status status = module.decode(bytes.data(), bytes.size(), host_alloc, host_free, nullptr);
        if (status != wasm::Status::Ok) {
            std::fprintf(stderr, "wasm artifact decode failed: %s status=%u\n",
                         argv[index], static_cast<unsigned>(status));
            return 1;
        }
        live_bytes = 0;
        peak_bytes = 0;
        alloc_count = 0;
        wasm::OwnedSession session;
        status = session.init_view(bytes.data(), bytes.size(), counting_alloc,
                                   counting_free, nullptr, config);
        if (status != wasm::Status::Ok) {
            std::fprintf(stderr, "wasm artifact init failed: %s status=%u\n",
                         argv[index], static_cast<unsigned>(status));
            failures++;
            continue;
        }
        const uint64_t peak = peak_bytes;
        const uint64_t reserve = session.runtime().memory_size();
        bool exempt = false;
        for (const char *path : budget_exempt) {
            if (std::strcmp(path, argv[index]) == 0) {
                exempt = true;
                break;
            }
        }
        std::printf("%-28s peak=%6llu KiB live=%6llu KiB allocs=%6llu reserve=%5llu MiB%s\n",
                    argv[index], static_cast<unsigned long long>(peak / 1024),
                    static_cast<unsigned long long>(live_bytes / 1024),
                    static_cast<unsigned long long>(alloc_count),
                    static_cast<unsigned long long>(reserve / (1024 * 1024)),
                    exempt ? "  [budget-exempt: not a boot service]" : "");
        if (peak > budget_bytes && !exempt) {
            std::fprintf(stderr, "wasm artifact RAM budget exceeded: %s peak=%llu MiB budget=%llu MiB\n",
                         argv[index], static_cast<unsigned long long>(peak / (1024 * 1024)),
                         static_cast<unsigned long long>(budget_bytes / (1024 * 1024)));
            failures++;
        }
    }
    if (failures) {
        std::fprintf(stderr, "wasm artifact decode: FAIL (%d)\n", failures);
        return 1;
    }
    std::printf("wasm artifact decode: PASS (%d)\n", argc - 1);
    return 0;
}
