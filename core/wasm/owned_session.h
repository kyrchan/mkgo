#ifndef KERNEL_WASM_OWNED_SESSION_H
#define KERNEL_WASM_OWNED_SESSION_H

#include "wasm.h"

namespace wasm {

class OwnedSession {
public:
    OwnedSession();
    ~OwnedSession();
    OwnedSession(const OwnedSession &) = delete;
    OwnedSession &operator=(const OwnedSession &) = delete;

    Status init(const uint8_t *blob, uint64_t length, Allocator allocator,
                Deallocator deallocator, void *context,
                const RuntimeConfig &config);
    Status init_view(const uint8_t *blob, uint64_t length, Allocator allocator,
                     Deallocator deallocator, void *context,
                     const RuntimeConfig &config);
    Status bind_import(const char *module_name, const char *field_name,
                       ImportInvoke invoke, void *context);
    Status bind_import_typed(const char *module_name, const char *field_name,
                             const FuncType *expected, ImportInvoke invoke,
                             void *context);
    RunStatus start(Value *results, uint32_t result_capacity);
    RunStatus resume(Value *results, uint32_t result_capacity);
    void request_preempt();
    bool initialized() const;
    Module &module();
    Runtime &runtime();

private:
    Module module_;
    Runtime runtime_;
    bool initialized_;
};

}

#endif
