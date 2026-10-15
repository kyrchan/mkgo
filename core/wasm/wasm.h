#ifndef KERNEL_WASM_H
#define KERNEL_WASM_H

#include <stddef.h>
#include <stdint.h>

namespace wasm {

using Allocator = void *(*)(uint64_t, void *);
using Deallocator = void (*)(void *, void *);

enum class Status : uint16_t {
    Ok,
    Malformed,
    Unsupported,
    TypeMismatch,
    OutOfMemory,
    MissingImport,
    Internal,
    TrapUnreachable,
    TrapOutOfBounds,
    TrapDivideByZero,
    TrapIndirectCall,
    TrapStackOverflow,
    TrapIntegerOverflow,
    TrapInvalidConversion,
};

/* Sentinel import index: no host import was involved in the failure. */
constexpr uint32_t kNoImport = 0xffffffffu;

enum class RunStatus : uint8_t {
    Completed,
    Yield,
    Exited,
    Trapped,
    Error,
};

enum class ValueType : uint8_t {
    I32 = 0x7f,
    I64 = 0x7e,
    F32 = 0x7d,
    F64 = 0x7c,
    FuncRef = 0x70,
    ExternRef = 0x6f,
};

enum class Op : uint8_t {
    Unreachable,
    Nop,
    Block,
    Loop,
    If,
    Else,
    End,
    Br,
    BrIf,
    BrTable,
    Return,
    Drop,
    Select,
    I32Const,
    I32Add,
    I32Sub,
    I32Mul,
    I32Eqz,
    I32Cmp,
    I32Clz,
    I32Ctz,
    I32Popcnt,
    I32Rotl,
    I32Rotr,
    I32DivS,
    I32DivU,
    I32RemS,
    I32RemU,
    I32And,
    I32Or,
    I32Xor,
    I32Shl,
    I32ShrS,
    I32ShrU,
    I64Eqz,
    I64Cmp,
    I64Clz,
    I64Ctz,
    I64Popcnt,
    I64Rotl,
    I64Rotr,
    I64Sub,
    I64Mul,
    I64DivS,
    I64DivU,
    I64RemS,
    I64RemU,
    I64And,
    I64Or,
    I64Xor,
    I64Shl,
    I64ShrS,
    I64ShrU,
    LocalGet,
    LocalSet,
    LocalTee,
    GlobalGet,
    GlobalSet,
    Call,
    CallIndirect,
    MemorySize,
    MemoryGrow,
    I32Load,
    I32Store,
    I64Const,
    I64ExtendI32S,
    I64ExtendI32U,
    I32WrapI64,
    I64Add,
    I64Load,
    I64Store,
    F32Const,
    F64Const,
    F32Load,
    F64Load,
    F32Store,
    F64Store,
    F32Cmp,
    F64Cmp,
    F32Unary,
    F32Binary,
    F64Unary,
    F64Binary,
    FloatConvert,
    F32DemoteF64,
    F64PromoteF32,
    Reinterpret,
    SignExtend,
    TruncSat,
    MemoryCopy,
    MemoryFill,
};

struct Slice {
    const uint8_t *data;
    uint32_t size;
};

struct Value {
    uint64_t bits;
};

struct FuncType {
    uint32_t param_count;
    uint32_t result_count;
    ValueType params[16];
    ValueType results[4];
};

struct Import {
    Slice module_name;
    Slice field_name;
    uint8_t kind;
    uint32_t type_index;
};

struct Export {
    Slice name;
    uint8_t kind;
    uint32_t index;
};

struct MemoryDesc {
    uint32_t min_pages;
    uint32_t max_pages;
    bool has_max;
};

struct TableDesc {
    uint32_t min_size;
    uint32_t max_size;
    bool has_max;
};

struct GlobalDesc {
    uint8_t value_type;
    bool mutable_value;
    bool constant_init;
    int64_t initial_value;
};

struct ElementSegment {
    uint32_t table_index;
    bool has_offset;
    int32_t offset;
    uint32_t function_count;
    uint32_t *functions;
};

/* 16 bytes, down from 24. The IR is the dominant per-session cost on the
 * owned path: services/shell/shell.wasm decodes to 1.06M instructions, so
 * every byte here is ~1 MiB of pool per guest module, and six services
 * have to fit the pool at once.
 *
 * br_table needed two fields (first target index, target count) on top of
 * the general `operand`, and br_table has no immediate of its own, so the
 * count moved into the union and the index took over `operand`. That is
 * where the 8 bytes went. block_type deliberately stays OUT of the union:
 * an `if` needs both its block type and control.else_pc, and aliasing them
 * corrupts every if/else in the corpus. */
struct Instruction {
    union {
        uint64_t immediate;
        uint32_t operand2; /* i64 const high half, or br_table count */
        struct {
            uint32_t else_pc;
            uint32_t end_pc;
        } control;
    };
    uint32_t operand; /* immediate, br depth, memarg offset, or br_table index */
    Op op;
    uint8_t block_type;
    uint8_t memory_width;
    uint8_t memory_signed;
};

struct ArenaBlock;

struct FunctionBody {
    uint32_t type_index;
    uint32_t local_count;
    uint8_t *local_types;
    uint32_t instruction_count;
    Instruction *instructions;
    uint32_t max_stack;
    /* Offset into the module-wide local-type block; local_types is patched
     * to point into it once every body has been decoded. */
    uint32_t local_types_base;
    /* br_table targets live in one module-wide array. A BrTable
     * instruction's operand is the index *within this body* and
     * branch_base is where this body's range starts: an absolute index
     * would not survive re-lowering one function on demand, because the
     * re-lowered body would land at a different offset. */
    uint32_t branch_base;
    uint32_t branch_count;
    /* This body's bytes in the module blob. instructions is null until
     * the body is first entered: every body is validated at instantiate,
     * but only the ones that actually run get lowered. */
    const uint8_t *code;
    uint32_t code_size;
};

struct DataSegment {
    uint32_t offset;
    uint32_t size;
    const uint8_t *bytes;
};

struct ModuleData {
    uint32_t type_count;
    FuncType *types;
    uint32_t import_count;
    Import *imports;
    uint32_t imported_function_count;
    uint32_t defined_function_count;
    uint32_t *function_types;
    bool has_table;
    TableDesc table;
    bool has_memory;
    MemoryDesc memory;
    uint32_t global_count;
    GlobalDesc *globals;
    uint32_t element_count;
    ElementSegment *elements;
    uint32_t export_count;
    Export *exports;
    bool has_start;
    uint32_t start_function;
    uint32_t data_count;
    DataSegment *data;
    uint32_t branch_target_count;
    uint32_t *branch_targets;
    uint32_t body_count;
    FunctionBody *bodies;
    bool has_abi_version;
    uint8_t abi_version;
};

struct ArenaBlock;

class Arena {
public:
    Arena();
    ~Arena();
    Arena(const Arena &) = delete;
    Arena &operator=(const Arena &) = delete;

    void init(Allocator allocator, Deallocator deallocator, void *context);
    void destroy();
    void *allocate(uint64_t size, uint64_t alignment);
    /* Hands back the owning block so the caller can free it with
     * release_block. Exclusive blocks are the hot allocation of module
     * decode; searching the block list per release is quadratic in
     * module size, so the handle is mandatory, not an optimisation. */
    void *allocate_exclusive(uint64_t size, uint64_t alignment, ArenaBlock **out_block);
    bool release_block(ArenaBlock *block);

private:
    Allocator allocator_;
    Deallocator deallocator_;
    void *context_;
    ArenaBlock *blocks_;
};

class Module {
public:
    Module();
    ~Module();
    Module(const Module &) = delete;
    Module &operator=(const Module &) = delete;

    Status decode(const uint8_t *blob, uint64_t length, Allocator allocator,
                  Deallocator deallocator, void *context);
    Status decode_view(const uint8_t *blob, uint64_t length, Allocator allocator,
                       Deallocator deallocator, void *context);
    /* Lowers a function body that has not run yet. Called from the
     * interpreter's single function-entry point, so a body's first call
     * pays for its own decode and later calls are unchanged. */
    Status ensure_lowered(uint32_t function_index);
    const ModuleData &data() const;

private:
    Status decode_impl(const uint8_t *blob, uint64_t length, Allocator allocator,
                       Deallocator deallocator, void *context, bool copy_blob);

    Arena arena_;
    ModuleData data_;
};

class Runtime;

using ImportInvoke = RunStatus (*)(void *context, Runtime &runtime,
                                   const Value *args, Value *results,
                                   uint32_t result_count, uint32_t *exit_code);

struct HostImport {
    const char *module_name;
    const char *field_name;
    ImportInvoke invoke;
    void *context;
};

struct RuntimeConfig {
    uint32_t max_values;
    uint32_t max_frames;
    uint32_t max_labels;
    uint32_t max_table_entries;
    uint64_t max_memory_bytes;
};

class Runtime {
public:
    Runtime();
    ~Runtime();
    Runtime(const Runtime &) = delete;
    Runtime &operator=(const Runtime &) = delete;

    Status instantiate(Module &module, Allocator allocator,
                       Deallocator deallocator, void *context,
                       const RuntimeConfig &config);
    Status bind_import(const char *module_name, const char *field_name,
                       ImportInvoke invoke, void *context);
    Status bind_import_typed(const char *module_name, const char *field_name,
                             const FuncType *expected, ImportInvoke invoke,
                             void *context);
    RunStatus invoke(const Slice &name, const Value *args, uint32_t arg_count,
                     Value *results, uint32_t result_capacity);
    RunStatus resume(Value *results, uint32_t result_capacity);
    void request_preempt();
    bool imports_bound() const;
    uint8_t *memory() const;
    uint64_t memory_size() const;
    uint32_t exit_code() const;
    Status error() const;
    // Where a trap stopped execution: the innermost frame's function
    // index and the pc of the faulting instruction. A kernel that only
    // knows "the guest trapped" cannot triage a decoder or interpreter
    // bug -- it needs the location. Both are 0 when no trap was recorded.
    uint32_t trap_function() const;
    uint32_t trap_pc() const;
    // Import index whose host call ended in a trap/error, or kNoImport.
    // A guest trap is very often a host import reporting failure, and
    // without this the kernel cannot tell the guest's own fault from a
    // refused syscall.
    uint32_t error_import() const;
    void set_error(Status status);
    // Raise a trap, recording the trapping function/pc for diagnostics.
    void raise(Status status);
    // Grow linear memory so it covers min_bytes, in ONE reallocation.
    // Device windows are mapped into guest linear memory at offsets fixed
    // by the frozen ABI (ABI.md 6 places the net RX/TX rings at 64 MiB),
    // which is above the default per-session bound -- so this lifts that
    // bound rather than refusing. The allocator/pool is still the real
    // budget, so lifting the bound costs nothing until a session actually
    // grows. Fails only on a declared max_pages violation or allocation
    // failure. Never shrinks.
    bool ensure_memory_bytes(uint64_t min_bytes);

private:
    struct Frame {
        uint32_t function_index;
        uint32_t pc;
        uint32_t locals_base;
        uint32_t values_base;
        uint32_t labels_base;
    };

    struct Label {
        uint32_t kind;
        uint32_t start_pc;
        uint32_t else_pc;
        uint32_t end_pc;
        uint32_t value_height;
        uint32_t result_count;
        uint8_t result_type;
    };

    Status initialize_storage(Allocator allocator, Deallocator deallocator,
                              void *context, const RuntimeConfig &config);
    Status start_function(const Slice &name, const Value *args,
                          uint32_t arg_count, Value *results,
                          uint32_t result_capacity);
    Status enter_frame(uint32_t function_index);
    RunStatus step(Value *results, uint32_t result_capacity);
    RunStatus finish_function(Value *results, uint32_t result_capacity);
    RunStatus return_from_function(Value *results, uint32_t result_capacity);
    bool branch_to(uint32_t depth);
    RunStatus execute_import(uint32_t function_index);
    uint32_t function_type_index(uint32_t function_index) const;
    const FunctionBody *function_body(uint32_t function_index) const;
    bool find_function(const Slice &name, uint32_t *function_index) const;
    bool local_type(uint32_t function_index, uint32_t local_index,
                    ValueType *type) const;
    bool pop_value(Value *value);
    bool push_value(const Value &value);
    bool grow_memory(uint32_t pages, uint32_t *old_pages);
    bool check_memory(uint64_t offset, uint64_t size) const;

    Arena arena_;
    Module *module_;
    Allocator allocator_;
    Deallocator deallocator_;
    void *allocator_context_;
    RuntimeConfig config_;
    Value *values_;
    uint32_t value_count_;
    Frame *frames_;
    uint32_t frame_count_;
    Label *labels_;
    uint32_t label_count_;
    Value *globals_;
    uint32_t global_count_;
    uint32_t *table_;
    uint32_t table_size_;
    uint8_t *memory_;
    uint64_t memory_size_;
    uint64_t memory_capacity_;
    bool instantiated_;
    volatile bool running_;
    volatile bool preempt_pending_;
    bool exit_requested_;
    uint32_t pending_function_;
    uint32_t exit_code_;
    Status error_;
    uint32_t trap_function_;
    uint32_t trap_pc_;
    uint32_t error_import_;
    Value *pending_results_;
    uint32_t pending_result_count_;
    uint32_t pending_result_capacity_;
    HostImport *imports_;
    uint32_t import_count_;
};

}

#endif
