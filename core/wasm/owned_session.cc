#include "owned_session.h"

namespace wasm {

OwnedSession::OwnedSession() : initialized_(false) {}

OwnedSession::~OwnedSession() = default;

Status OwnedSession::init(const uint8_t *blob, uint64_t length,
                          Allocator allocator, Deallocator deallocator,
                          void *context, const RuntimeConfig &config) {
    initialized_ = false;
    if (!blob || length < 8)
        return Status::Malformed;
    Status status = module_.decode(blob, length, allocator, deallocator, context);
    if (status != Status::Ok)
        return status;
    if (!module_.data().has_abi_version || module_.data().abi_version != 2)
        return Status::Unsupported;
    status = runtime_.instantiate(module_, allocator, deallocator, context, config);
    if (status == Status::Ok)
        initialized_ = true;
    return status;
}

Status OwnedSession::init_view(const uint8_t *blob, uint64_t length,
                              Allocator allocator, Deallocator deallocator,
                              void *context, const RuntimeConfig &config) {
    initialized_ = false;
    if (!blob || length < 8)
        return Status::Malformed;
    Status status = module_.decode_view(blob, length, allocator, deallocator, context);
    if (status != Status::Ok)
        return status;
    if (!module_.data().has_abi_version || module_.data().abi_version != 2)
        return Status::Unsupported;
    status = runtime_.instantiate(module_, allocator, deallocator, context, config);
    if (status == Status::Ok)
        initialized_ = true;
    return status;
}

Status OwnedSession::bind_import(const char *module_name, const char *field_name,
                                 ImportInvoke invoke, void *context) {
    if (!initialized_)
        return Status::Internal;
    return runtime_.bind_import(module_name, field_name, invoke, context);
}

Status OwnedSession::bind_import_typed(const char *module_name,
                                       const char *field_name,
                                       const FuncType *expected,
                                       ImportInvoke invoke, void *context) {
    if (!initialized_)
        return Status::Internal;
    return runtime_.bind_import_typed(module_name, field_name, expected, invoke, context);
}

RunStatus OwnedSession::start(Value *results, uint32_t result_capacity) {
    if (!initialized_)
        return RunStatus::Error;
    const Slice start_name{reinterpret_cast<const uint8_t *>("_start"), 6};
    return runtime_.invoke(start_name, nullptr, 0, results, result_capacity);
}

RunStatus OwnedSession::resume(Value *results, uint32_t result_capacity) {
    if (!initialized_)
        return RunStatus::Error;
    return runtime_.resume(results, result_capacity);
}

void OwnedSession::request_preempt() {
    if (initialized_)
        runtime_.request_preempt();
}

bool OwnedSession::initialized() const {
    return initialized_;
}

Module &OwnedSession::module() {
    return module_;
}

Runtime &OwnedSession::runtime() {
    return runtime_;
}

}
