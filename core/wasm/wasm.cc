#include "wasm.h"

namespace wasm {
namespace {

constexpr uint32_t kPageSize = 65536;
constexpr uint32_t kMaxSections = 64;
constexpr uint32_t kMaxTypes = 4096;
constexpr uint32_t kMaxImports = 4096;
constexpr uint32_t kMaxFunctions = 65536;
constexpr uint32_t kMaxExports = 65536;
constexpr uint32_t kMaxLocals = 1024;
constexpr uint32_t kMaxInstructions = 1000000;
constexpr uint32_t kMaxControlDepth = 4096;
constexpr uint32_t kMaxTypeStack = 16384;
constexpr uint32_t kMaxBranchTargets = 65536;
constexpr uint32_t kNoPc = 0xffffffffu;

bool add_overflow_u64(uint64_t left, uint64_t right, uint64_t *result) {
    return __builtin_add_overflow(left, right, result);
}

bool mul_overflow_u64(uint64_t left, uint64_t right, uint64_t *result) {
    return __builtin_mul_overflow(left, right, result);
}

uint64_t align_up_u64(uint64_t value, uint64_t alignment) {
    if (alignment < 2 || (alignment & (alignment - 1)) != 0)
        return value;
    uint64_t mask = alignment - 1;
    if (value > UINT64_MAX - mask)
        return 0;
    return (value + mask) & ~mask;
}

bool valid_value_type(uint8_t value) {
    return value == static_cast<uint8_t>(ValueType::I32) ||
           value == static_cast<uint8_t>(ValueType::I64) ||
           value == static_cast<uint8_t>(ValueType::F32) ||
           value == static_cast<uint8_t>(ValueType::F64);
}

bool valid_block_type(uint8_t value) {
    return value == 0x40 || valid_value_type(value);
}

bool same_function_type(const FuncType &left, const FuncType &right) {
    if (left.param_count != right.param_count || left.result_count != right.result_count)
        return false;
    for (uint32_t index = 0; index < left.param_count; ++index) {
        if (left.params[index] != right.params[index])
            return false;
    }
    for (uint32_t index = 0; index < left.result_count; ++index) {
        if (left.results[index] != right.results[index])
            return false;
    }
    return true;
}

uint64_t compare_i32(uint8_t opcode, uint32_t left, uint32_t right) {
    int32_t signed_left = static_cast<int32_t>(left);
    int32_t signed_right = static_cast<int32_t>(right);
    switch (opcode) {
    case 0x46: return left == right;
    case 0x47: return left != right;
    case 0x48: return signed_left < signed_right;
    case 0x49: return left < right;
    case 0x4a: return signed_left > signed_right;
    case 0x4b: return left > right;
    case 0x4c: return signed_left <= signed_right;
    case 0x4d: return left <= right;
    case 0x4e: return signed_left >= signed_right;
    case 0x4f: return left >= right;
    default: return 0;
    }
}

uint64_t compare_i64(uint8_t opcode, uint64_t left, uint64_t right) {
    int64_t signed_left = static_cast<int64_t>(left);
    int64_t signed_right = static_cast<int64_t>(right);
    switch (opcode) {
    case 0x51: return left == right;
    case 0x52: return left != right;
    case 0x53: return signed_left < signed_right;
    case 0x54: return left < right;
    case 0x55: return signed_left > signed_right;
    case 0x56: return left > right;
    case 0x57: return signed_left <= signed_right;
    case 0x58: return left <= right;
    case 0x59: return signed_left >= signed_right;
    case 0x5a: return left >= right;
    default: return 0;
    }
}

uint32_t shift_right_i32(uint32_t value, uint32_t count, bool arithmetic) {
    count &= 31;
    if (count == 0)
        return value;
    uint32_t result = value >> count;
    if (arithmetic && (value & 0x80000000u) != 0)
        result |= UINT32_MAX << (32 - count);
    return result;
}

uint64_t shift_right_i64(uint64_t value, uint32_t count, bool arithmetic) {
    count &= 63;
    if (count == 0)
        return value;
    uint64_t result = value >> count;
    if (arithmetic && (value & (static_cast<uint64_t>(1) << 63)) != 0)
        result |= UINT64_MAX << (64 - count);
    return result;
}

uint32_t count_leading_zeroes32(uint32_t value) {
    if (value == 0)
        return 32;
    uint32_t count = 0;
    while ((value & 0x80000000u) == 0) {
        value <<= 1;
        ++count;
    }
    return count;
}

uint32_t count_trailing_zeroes32(uint32_t value) {
    if (value == 0)
        return 32;
    uint32_t count = 0;
    while ((value & 1u) == 0) {
        value >>= 1;
        ++count;
    }
    return count;
}

uint32_t population_count32(uint32_t value) {
    uint32_t count = 0;
    while (value != 0) {
        count += value & 1u;
        value >>= 1;
    }
    return count;
}

uint64_t count_leading_zeroes64(uint64_t value) {
    if (value == 0)
        return 64;
    uint32_t count = 0;
    while ((value & (static_cast<uint64_t>(1) << 63)) == 0) {
        value <<= 1;
        ++count;
    }
    return count;
}

uint64_t count_trailing_zeroes64(uint64_t value) {
    if (value == 0)
        return 64;
    uint32_t count = 0;
    while ((value & 1u) == 0) {
        value >>= 1;
        ++count;
    }
    return count;
}

uint64_t population_count64(uint64_t value) {
    uint32_t count = 0;
    while (value != 0) {
        count += value & 1u;
        value >>= 1;
    }
    return count;
}

uint32_t rotate_left32(uint32_t value, uint32_t count) {
    count &= 31;
    if (count == 0)
        return value;
    return (value << count) | (value >> (32 - count));
}

uint32_t rotate_right32(uint32_t value, uint32_t count) {
    count &= 31;
    if (count == 0)
        return value;
    return (value >> count) | (value << (32 - count));
}

uint64_t rotate_left64(uint64_t value, uint32_t count) {
    count &= 63;
    if (count == 0)
        return value;
    return (value << count) | (value >> (64 - count));
}

uint64_t rotate_right64(uint64_t value, uint32_t count) {
    count &= 63;
    if (count == 0)
        return value;
    return (value >> count) | (value << (64 - count));
}

float f32_from_bits(uint32_t bits) {
    float value = 0.0f;
    __builtin_memcpy(&value, &bits, sizeof(value));
    return value;
}

uint32_t f32_to_bits(float value) {
    uint32_t bits = 0;
    __builtin_memcpy(&bits, &value, sizeof(bits));
    return bits;
}

double f64_from_bits(uint64_t bits) {
    double value = 0.0;
    __builtin_memcpy(&value, &bits, sizeof(value));
    return value;
}

uint64_t f64_to_bits(double value) {
    uint64_t bits = 0;
    __builtin_memcpy(&bits, &value, sizeof(bits));
    return bits;
}

bool f32_is_nan(uint32_t bits);
bool f64_is_nan(uint64_t bits);

float f32_min(float left, float right) {
    if (f32_is_nan(f32_to_bits(left)) || f32_is_nan(f32_to_bits(right)))
        return f32_from_bits(0x7fc00000u);
    if (left == 0.0f && right == 0.0f)
        return (f32_to_bits(left) & 0x80000000u) || (f32_to_bits(right) & 0x80000000u)
                   ? -0.0f
                   : 0.0f;
    return left < right ? left : right;
}

float f32_max(float left, float right) {
    if (f32_is_nan(f32_to_bits(left)) || f32_is_nan(f32_to_bits(right)))
        return f32_from_bits(0x7fc00000u);
    if (left == 0.0f && right == 0.0f)
        return (f32_to_bits(left) & 0x80000000u) && (f32_to_bits(right) & 0x80000000u)
                   ? -0.0f
                   : 0.0f;
    return left > right ? left : right;
}

double f64_min(double left, double right) {
    if (f64_is_nan(f64_to_bits(left)) || f64_is_nan(f64_to_bits(right)))
        return f64_from_bits(0x7ff8000000000000ull);
    if (left == 0.0 && right == 0.0)
        return (f64_to_bits(left) & 0x8000000000000000ull) ||
                       (f64_to_bits(right) & 0x8000000000000000ull)
                   ? -0.0
                   : 0.0;
    return left < right ? left : right;
}

double f64_max(double left, double right) {
    if (f64_is_nan(f64_to_bits(left)) || f64_is_nan(f64_to_bits(right)))
        return f64_from_bits(0x7ff8000000000000ull);
    if (left == 0.0 && right == 0.0)
        return (f64_to_bits(left) & 0x8000000000000000ull) &&
                       (f64_to_bits(right) & 0x8000000000000000ull)
                   ? -0.0
                   : 0.0;
    return left > right ? left : right;
}

float f32_nearest(float value) {
    uint32_t bits = f32_to_bits(value);
    if ((bits & 0x7f800000u) == 0x7f800000u)
        return value;
    bool negative = (bits & 0x80000000u) != 0;
    float magnitude = negative ? -value : value;
    float truncated = __builtin_truncf(magnitude);
    float fraction = magnitude - truncated;
    uint32_t truncated_bits = f32_to_bits(truncated);
    if (fraction > 0.5f ||
        (fraction == 0.5f && (truncated_bits & 1u) != 0))
        truncated += 1.0f;
    return negative ? -truncated : truncated;
}

double f64_nearest(double value) {
    uint64_t bits = f64_to_bits(value);
    if ((bits & 0x7ff0000000000000ull) == 0x7ff0000000000000ull)
        return value;
    bool negative = (bits & 0x8000000000000000ull) != 0;
    double magnitude = negative ? -value : value;
    double truncated = __builtin_trunc(magnitude);
    double fraction = magnitude - truncated;
    uint64_t truncated_bits = f64_to_bits(truncated);
    if (fraction > 0.5 ||
        (fraction == 0.5 && (truncated_bits & 1ull) != 0))
        truncated += 1.0;
    return negative ? -truncated : truncated;
}

bool f32_is_nan(uint32_t bits) {
    return (bits & 0x7f800000u) == 0x7f800000u && (bits & 0x007fffffu) != 0;
}

bool f64_is_nan(uint64_t bits) {
    return (bits & 0x7ff0000000000000ull) == 0x7ff0000000000000ull &&
           (bits & 0x000fffffffffffffull) != 0;
}

bool trunc_i32_s(double value, uint32_t *result) {
    if (!(value >= -2147483648.0 && value < 2147483648.0))
        return false;
    *result = static_cast<uint32_t>(static_cast<int32_t>(value));
    return true;
}

bool trunc_i32_u(double value, uint32_t *result) {
    if (!(value >= 0.0 && value < 4294967296.0))
        return false;
    *result = static_cast<uint32_t>(value);
    return true;
}

bool trunc_i64_s(double value, uint64_t *result) {
    if (!(value >= -9223372036854775808.0 && value < 9223372036854775808.0))
        return false;
    *result = static_cast<uint64_t>(static_cast<int64_t>(value));
    return true;
}

bool trunc_i64_u(double value, uint64_t *result) {
    if (!(value >= 0.0 && value < 18446744073709551616.0))
        return false;
    *result = static_cast<uint64_t>(value);
    return true;
}

uint32_t saturate_i32_s(double value) {
    if (!(value == value))
        return 0;
    if (value <= -2147483648.0)
        return 0x80000000u;
    if (value >= 2147483648.0)
        return 0x7fffffffu;
    return static_cast<uint32_t>(static_cast<int32_t>(value));
}

uint32_t saturate_i32_u(double value) {
    if (!(value == value))
        return 0;
    if (value <= 0.0)
        return 0;
    if (value >= 4294967296.0)
        return 0xffffffffu;
    return static_cast<uint32_t>(value);
}

uint64_t saturate_i64_s(double value) {
    if (!(value == value))
        return 0;
    if (value <= -9223372036854775808.0)
        return 0x8000000000000000ull;
    if (value >= 9223372036854775808.0)
        return 0x7fffffffffffffffull;
    return static_cast<uint64_t>(static_cast<int64_t>(value));
}

uint64_t saturate_i64_u(double value) {
    if (!(value == value))
        return 0;
    if (value <= 0.0)
        return 0;
    if (value >= 18446744073709551616.0)
        return 0xffffffffffffffffull;
    return static_cast<uint64_t>(value);
}

bool slice_equal_cstr(const Slice &slice, const char *text) {
    if (!slice.data || !text)
        return false;
    uint64_t length = 0;
    while (text[length] != 0) {
        if (length >= slice.size)
            return false;
        ++length;
    }
    if (slice.size != length)
        return false;
    for (uint32_t i = 0; i < slice.size; ++i) {
        if (slice.data[i] != static_cast<uint8_t>(text[i]))
            return false;
    }
    return true;
}

struct Reader {
    const uint8_t *data;
    uint64_t size;
    uint64_t position;

    bool remaining(uint64_t amount) const {
        return amount <= size - position;
    }

    bool u8(uint8_t *value) {
        if (!remaining(1))
            return false;
        *value = data[position++];
        return true;
    }

    bool bytes(uint64_t amount, const uint8_t **value) {
        if (!remaining(amount))
            return false;
        *value = data + position;
        position += amount;
        return true;
    }

    bool uleb32(uint32_t *value) {
        uint64_t result = 0;
        uint32_t shift = 0;
        for (uint32_t index = 0; index < 5; ++index) {
            uint8_t byte = 0;
            if (!u8(&byte))
                return false;
            if (shift == 28 && (byte & 0xf0) != 0)
                return false;
            result |= static_cast<uint64_t>(byte & 0x7f) << shift;
            if ((byte & 0x80) == 0) {
                *value = static_cast<uint32_t>(result);
                return true;
            }
            shift += 7;
        }
        return false;
    }

    bool sleb32(int32_t *value) {
        int64_t result = 0;
        uint32_t shift = 0;
        uint8_t byte = 0;
        for (uint32_t index = 0; index < 5; ++index) {
            if (!u8(&byte))
                return false;
            if (shift == 28) {
                uint8_t extension = byte & 0x70;
                if (extension != 0 && extension != 0x70)
                    return false;
            }
            result |= static_cast<int64_t>(static_cast<uint64_t>(byte & 0x7f) << shift);
            shift += 7;
            if ((byte & 0x80) == 0) {
                if (shift < 32 && (byte & 0x40) != 0)
                    result |= static_cast<int64_t>(UINT64_MAX << shift);
                *value = static_cast<int32_t>(result);
                return true;
            }
        }
        return false;
    }

    bool sleb64(int64_t *value) {
        uint64_t result = 0;
        uint32_t shift = 0;
        uint8_t byte = 0;
        for (uint32_t index = 0; index < 10; ++index) {
            if (!u8(&byte))
                return false;
            if (shift == 63) {
                uint8_t extension = byte & 0x7e;
                if (extension != 0 && extension != 0x7e)
                    return false;
            }
            result |= static_cast<uint64_t>(byte & 0x7f) << shift;
            shift += 7;
            if ((byte & 0x80) == 0) {
                if (shift < 64 && (byte & 0x40) != 0)
                    result |= UINT64_MAX << shift;
                *value = static_cast<int64_t>(result);
                return true;
            }
        }
        return false;
    }

    bool name(Slice *value) {
        uint32_t length = 0;
        if (!uleb32(&length))
            return false;
        const uint8_t *bytes = nullptr;
        if (!bytes_with_length(length, &bytes))
            return false;
        value->data = bytes;
        value->size = length;
        return true;
    }

    bool bytes_with_length(uint32_t length, const uint8_t **value) {
        return bytes(length, value);
    }
};

template <typename T>
struct TempChunk {
    TempChunk<T> *next;
    TempChunk<T> *previous;
    ArenaBlock *block;
    uint32_t count;
    uint32_t capacity;
    T values[];
};

template <typename T>
class TempVector {
public:
    explicit TempVector(Arena *arena) : arena_(arena), first_(nullptr), last_(nullptr),
                                       previous_(nullptr), count_(0) {}
    ~TempVector() { release(); }

    bool reserve(uint32_t capacity) {
        if (capacity == 0 || first_)
            return false;
        uint64_t bytes = 0;
        if (mul_overflow_u64(sizeof(TempChunk<T>), 1, &bytes) ||
            mul_overflow_u64(capacity, sizeof(T), &bytes) ||
            add_overflow_u64(bytes, sizeof(TempChunk<T>), &bytes))
            return false;
        ArenaBlock *block = nullptr;
        TempChunk<T> *chunk = static_cast<TempChunk<T> *>(
            arena_->allocate_exclusive(bytes, alignof(TempChunk<T>), &block));
        if (!chunk)
            return false;
        chunk->block = block;
        chunk->next = nullptr;
        chunk->previous = nullptr;
        chunk->count = 0;
        chunk->capacity = capacity;
        first_ = chunk;
        last_ = chunk;
        previous_ = nullptr;
        return true;
    }

    T *append_ptr(const T &value) {
        if (count_ == UINT32_MAX)
            return nullptr;
        if (!last_ || last_->count == last_->capacity) {
            constexpr uint32_t chunk_capacity = 64;
            uint64_t bytes = 0;
            if (mul_overflow_u64(sizeof(TempChunk<T>), 1, &bytes) ||
                mul_overflow_u64(chunk_capacity, sizeof(T), &bytes) ||
                add_overflow_u64(bytes, sizeof(TempChunk<T>), &bytes))
                return nullptr;
            ArenaBlock *block = nullptr;
            TempChunk<T> *chunk = static_cast<TempChunk<T> *>(
                arena_->allocate_exclusive(bytes, alignof(TempChunk<T>), &block));
            if (!chunk)
                return nullptr;
            chunk->block = block;
            chunk->next = nullptr;
            chunk->count = 0;
            chunk->capacity = chunk_capacity;
            if (last_) {
                last_->next = chunk;
                chunk->previous = last_;
                previous_ = last_;
            } else {
                first_ = chunk;
                chunk->previous = nullptr;
                previous_ = nullptr;
            }
            last_ = chunk;
        }
        T *result = &last_->values[last_->count++];
        *result = value;
        ++count_;
        return result;
    }

    bool append(const T &value) {
        return append_ptr(value) != nullptr;
    }

    uint32_t count() const {
        return count_;
    }

    T *top() {
        for (TempChunk<T> *chunk = last_; chunk; chunk = chunk->previous) {
            if (chunk->count != 0)
                return &chunk->values[chunk->count - 1];
        }
        return nullptr;
    }

    void pop() {
        for (TempChunk<T> *chunk = last_; chunk; chunk = chunk->previous) {
            if (chunk->count != 0) {
                --chunk->count;
                --count_;
                return;
            }
        }
    }

    /* at() counts back from the newest chunk -- it serves top()/pop() and
     * takes an offset from the *end*. nth() counts forward from the
     * oldest, which is what instruction and branch-target pc/offset
     * lookups mean. */
    struct Cursor {
        TempChunk<T> *chunk;
        uint32_t base;
    };

    Cursor cursor() const { return Cursor{first_, 0}; }

    /* Forward lookup through a caller-held cursor: O(1) amortized for
     * ascending indices, which is how pc lookups run. */
    T *nth_from(uint32_t index, Cursor &state) const {
        if (!state.chunk)
            state.chunk = first_;
        while (state.chunk && index >= state.base + state.chunk->count) {
            state.base += state.chunk->count;
            state.chunk = state.chunk->next;
        }
        if (!state.chunk)
            return nullptr;
        return &state.chunk->values[index - state.base];
    }

    T *at(uint32_t index) const {
        TempChunk<T> *chunk = last_;
        while (chunk && index >= chunk->count) {
            index -= chunk->count;
            chunk = chunk->previous;
        }
        return chunk ? &chunk->values[index] : nullptr;
    }

    bool set(uint32_t index, const T &value) {
        T *target = at(index);
        if (!target)
            return false;
        *target = value;
        return true;
    }

    void release() {
        TempChunk<T> *chunk = first_;
        while (chunk) {
            TempChunk<T> *next = chunk->next;
            arena_->release_block(chunk->block);
            chunk = next;
        }
        first_ = nullptr;
        last_ = nullptr;
        previous_ = nullptr;
        count_ = 0;
    }

    T *finish() {
        if (count_ == 0) {
            release();
            return nullptr;
        }
        uint64_t bytes = 0;
        if (mul_overflow_u64(sizeof(T), count_, &bytes)) {
            release();
            return nullptr;
        }
        T *result = static_cast<T *>(arena_->allocate(bytes, alignof(T)));
        if (!result) {
            release();
            return nullptr;
        }
        uint32_t offset = 0;
        for (TempChunk<T> *chunk = first_; chunk; chunk = chunk->next) {
            for (uint32_t index = 0; index < chunk->count; ++index)
                result[offset++] = chunk->values[index];
        }
        release();
        return result;
    }

private:
    Arena *arena_;
    TempChunk<T> *first_;
    TempChunk<T> *last_;
    TempChunk<T> *previous_;
    uint32_t count_;
};

struct OpenControl {
    Instruction *opening;
    uint8_t opcode;
    bool has_else;
};

struct Builder {
    explicit Builder(Arena *arena) : arena(arena), type_count(0), import_count(0),
                                    imported_function_count(0), function_count(0),
                                    table_count(0), memory_count(0), export_count(0),
                                    start_count(0), data_count_declared(0),
                                    has_data_count(false), body_count(0) {}

    Arena *arena;
    TempVector<FuncType> types{arena};
    TempVector<Import> imports{arena};
    TempVector<uint32_t> function_types{arena};
    TempVector<GlobalDesc> globals{arena};
    TempVector<ElementSegment> elements{arena};
    TempVector<Export> exports{arena};
    TempVector<FunctionBody> bodies{arena};
    TempVector<DataSegment> data{arena};
    TempVector<uint32_t> branch_targets{arena};
    /* Every body's local types share one block. A per-body allocation costs
     * the arena's 4 KiB floor each, and a Go service has ~1,500 functions
     * holding ~6 KiB of local types in total: 5.7 MiB of pure granularity
     * waste per module. */
    TempVector<uint8_t> local_types_all{arena};
    /* Off when a body is being lowered on demand: its br_table targets
     * are already recorded from the eager pass, so re-recording them
     * would shift every later body. */
    bool emit_branch_targets{true};
    uint32_t type_count;
    uint32_t import_count;
    uint32_t imported_function_count;
    uint32_t function_count;
    uint32_t table_count;
    uint32_t memory_count;
    uint32_t export_count;
    uint32_t start_count;
    uint32_t data_count_declared;
    bool has_data_count;
    uint32_t body_count;
};

/* Defined after ValidationContext, below: the per-body validation pass. */
Status validate_all_bodies(ModuleData &data, Builder &builder);

const FuncType *function_type(const ModuleData &data, uint32_t function_index) {
    uint32_t imported = 0;
    for (uint32_t i = 0; i < data.import_count; ++i) {
        if (data.imports[i].kind != 0)
            continue;
        if (function_index == imported) {
            if (data.imports[i].type_index >= data.type_count)
                return nullptr;
            return &data.types[data.imports[i].type_index];
        }
        ++imported;
    }
    uint32_t defined_index = function_index - imported;
    if (defined_index >= data.defined_function_count ||
        defined_index >= data.body_count)
        return nullptr;
    uint32_t type_index = data.function_types[defined_index];
    if (type_index >= data.type_count)
        return nullptr;
    return &data.types[type_index];
}

const FunctionBody *body_for(const ModuleData &data, uint32_t function_index) {
    if (function_index < data.imported_function_count)
        return nullptr;
    uint32_t defined_index = function_index - data.imported_function_count;
    if (defined_index >= data.body_count)
        return nullptr;
    return &data.bodies[defined_index];
}

bool read_limits(Reader &reader, uint32_t *minimum, uint32_t *maximum, bool *has_max) {
    uint8_t flags = 0;
    if (!reader.u8(&flags) || flags > 1)
        return false;
    if (!reader.uleb32(minimum))
        return false;
    *has_max = flags == 1;
    *maximum = 0;
    if (*has_max && !reader.uleb32(maximum))
        return false;
    return !*has_max || *maximum >= *minimum;
}

Status decode_type_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxTypes)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        uint8_t tag = 0;
        if (!reader.u8(&tag) || tag != 0x60)
            return Status::Malformed;
        FuncType type{};
        uint32_t params = 0;
        if (!reader.uleb32(&params) || params > 16)
            return Status::Unsupported;
        type.param_count = params;
        for (uint32_t p = 0; p < params; ++p) {
            uint8_t value_type = 0;
            if (!reader.u8(&value_type) || !valid_value_type(value_type))
                return Status::Malformed;
            type.params[p] = static_cast<ValueType>(value_type);
        }
        uint32_t results = 0;
        if (!reader.uleb32(&results) || results > 4)
            return Status::Unsupported;
        type.result_count = results;
        for (uint32_t r = 0; r < results; ++r) {
            uint8_t value_type = 0;
            if (!reader.u8(&value_type) || !valid_value_type(value_type))
                return Status::Malformed;
            type.results[r] = static_cast<ValueType>(value_type);
        }
        if (!builder.types.append(type))
            return Status::OutOfMemory;
    }
    builder.type_count = count;
    return Status::Ok;
}

Status decode_import_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxImports)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        Import import{};
        if (!reader.name(&import.module_name) || !reader.name(&import.field_name))
            return Status::Malformed;
        if (!reader.u8(&import.kind))
            return Status::Malformed;
        if (import.kind != 0)
            return Status::Unsupported;
        if (!reader.uleb32(&import.type_index))
            return Status::Malformed;
        if (!builder.imports.append(import))
            return Status::OutOfMemory;
        ++builder.imported_function_count;
    }
    builder.import_count = count;
    return Status::Ok;
}

Status decode_function_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxFunctions)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        uint32_t type_index = 0;
        if (!reader.uleb32(&type_index))
            return Status::Malformed;
        if (!builder.function_types.append(type_index))
            return Status::OutOfMemory;
    }
    builder.function_count = count;
    return Status::Ok;
}

Status decode_table_section(Builder &builder, Reader &reader, ModuleData &data) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > 1)
        return Status::Unsupported;
    if (count == 0)
        return Status::Ok;
    uint8_t element_type = 0;
    if (!reader.u8(&element_type) || element_type != 0x70)
        return Status::Unsupported;
    uint32_t minimum = 0;
    uint32_t maximum = 0;
    bool has_max = false;
    if (!read_limits(reader, &minimum, &maximum, &has_max))
        return Status::Malformed;
    data.has_table = true;
    data.table.min_size = minimum;
    data.table.max_size = maximum;
    data.table.has_max = has_max;
    builder.table_count = count;
    return Status::Ok;
}

Status decode_memory_section(Builder &builder, Reader &reader, ModuleData &data) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > 1)
        return Status::Unsupported;
    if (count == 0)
        return Status::Ok;
    uint32_t minimum = 0;
    uint32_t maximum = 0;
    bool has_max = false;
    if (!read_limits(reader, &minimum, &maximum, &has_max))
        return Status::Malformed;
    data.has_memory = true;
    data.memory.min_pages = minimum;
    data.memory.max_pages = maximum;
    data.memory.has_max = has_max;
    builder.memory_count = count;
    return Status::Ok;
}

Status decode_global_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxTypes)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        GlobalDesc global{};
        uint8_t mutable_value = 0;
        if (!reader.u8(&global.value_type) || !valid_value_type(global.value_type) ||
            !reader.u8(&mutable_value) || mutable_value > 1)
            return Status::Malformed;
        global.mutable_value = mutable_value != 0;
        uint8_t opcode = 0;
        if (!reader.u8(&opcode))
            return Status::Malformed;
        if (opcode == 0x41) {
            if (global.value_type != static_cast<uint8_t>(ValueType::I32))
                return Status::Malformed;
            int32_t value = 0;
            if (!reader.sleb32(&value))
                return Status::Malformed;
            global.initial_value = value;
        } else if (opcode == 0x42) {
            if (global.value_type != static_cast<uint8_t>(ValueType::I64))
                return Status::Malformed;
            int64_t value = 0;
            if (!reader.sleb64(&value))
                return Status::Malformed;
            global.initial_value = value;
        } else if (opcode == 0x43) {
            if (global.value_type != static_cast<uint8_t>(ValueType::F32))
                return Status::Malformed;
            const uint8_t *bytes = nullptr;
            if (!reader.bytes(4, &bytes))
                return Status::Malformed;
            uint32_t value = static_cast<uint32_t>(bytes[0]) |
                             (static_cast<uint32_t>(bytes[1]) << 8) |
                             (static_cast<uint32_t>(bytes[2]) << 16) |
                             (static_cast<uint32_t>(bytes[3]) << 24);
            global.initial_value = static_cast<int32_t>(value);
        } else if (opcode == 0x44) {
            if (global.value_type != static_cast<uint8_t>(ValueType::F64))
                return Status::Malformed;
            const uint8_t *bytes = nullptr;
            if (!reader.bytes(8, &bytes))
                return Status::Malformed;
            uint64_t value = 0;
            for (uint32_t byte_index = 0; byte_index < 8; ++byte_index)
                value |= static_cast<uint64_t>(bytes[byte_index]) << (byte_index * 8);
            global.initial_value = static_cast<int64_t>(value);
        } else {
            return Status::Unsupported;
        }
        if (!reader.u8(&opcode) || opcode != 0x0b)
            return Status::Malformed;
        global.constant_init = true;
        if (!builder.globals.append(global))
            return Status::OutOfMemory;
    }
    return Status::Ok;
}

Status decode_element_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxExports)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        uint32_t flags = 0;
        if (!reader.uleb32(&flags) || flags != 0)
            return Status::Unsupported;
        uint8_t opcode = 0;
        int32_t offset = 0;
        if (!reader.u8(&opcode) || opcode != 0x41 || !reader.sleb32(&offset))
            return Status::Unsupported;
        if (!reader.u8(&opcode) || opcode != 0x0b)
            return Status::Malformed;
        uint32_t function_count = 0;
        if (!reader.uleb32(&function_count) || function_count > kMaxFunctions)
            return Status::Malformed;
        ElementSegment segment{};
        segment.table_index = 0;
        segment.has_offset = true;
        segment.offset = offset;
        segment.function_count = function_count;
        segment.functions = nullptr;
        if (function_count != 0) {
            uint64_t bytes = 0;
            if (mul_overflow_u64(sizeof(uint32_t), function_count, &bytes))
                return Status::OutOfMemory;
            segment.functions = static_cast<uint32_t *>(builder.arena->allocate(bytes, alignof(uint32_t)));
            if (!segment.functions)
                return Status::OutOfMemory;
            for (uint32_t function_index = 0; function_index < function_count; ++function_index) {
                if (!reader.uleb32(&segment.functions[function_index]))
                    return Status::Malformed;
            }
        }
        if (!builder.elements.append(segment))
            return Status::OutOfMemory;
    }
    return Status::Ok;
}

Status decode_export_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count) || count > kMaxExports)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        Export export_value{};
        if (!reader.name(&export_value.name) || !reader.u8(&export_value.kind))
            return Status::Malformed;
        if (export_value.kind != 0 && export_value.kind != 2)
            return Status::Unsupported;
        if (!reader.uleb32(&export_value.index))
            return Status::Malformed;
        if (!builder.exports.append(export_value))
            return Status::OutOfMemory;
    }
    builder.export_count = count;
    return Status::Ok;
}

Status decode_start_section(Builder &builder, Reader &reader, ModuleData &data) {
    uint32_t function_index = 0;
    if (!reader.uleb32(&function_index))
        return Status::Malformed;
    data.has_start = true;
    data.start_function = function_index;
    builder.start_count = 1;
    return Status::Ok;
}

Status decode_data_count_section(Builder &builder, Reader &reader) {
    uint32_t count = 0;
    if (!reader.uleb32(&count))
        return Status::Malformed;
    builder.data_count_declared = count;
    builder.has_data_count = true;
    return Status::Ok;
}

Status decode_data_section(Builder &builder, Reader &reader, const ModuleData &data) {
    uint32_t count = 0;
    if (!reader.uleb32(&count))
        return Status::Malformed;
    if (builder.has_data_count && count != builder.data_count_declared)
        return Status::Malformed;
    if (count > 0 && !data.has_memory)
        return Status::Malformed;
    for (uint32_t index = 0; index < count; ++index) {
        uint32_t flags = 0;
        if (!reader.uleb32(&flags) || flags != 0)
            return Status::Unsupported;
        uint8_t opcode = 0;
        int32_t offset = 0;
        if (!reader.u8(&opcode) || opcode != 0x41 || !reader.sleb32(&offset))
            return Status::Unsupported;
        if (!reader.u8(&opcode) || opcode != 0x0b)
            return Status::Malformed;
        uint32_t size = 0;
        const uint8_t *bytes = nullptr;
        if (!reader.uleb32(&size) || !reader.bytes(size, &bytes))
            return Status::Malformed;
        DataSegment segment{};
        segment.offset = static_cast<uint32_t>(offset);
        segment.size = size;
        segment.bytes = bytes;
        if (!builder.data.append(segment))
            return Status::OutOfMemory;
    }
    return Status::Ok;
}

/* With `sink` non-null the lowered instructions stay in that chunked
 * vector instead of being consolidated into the arena, so the caller can
 * validate a body and drop its IR without the module ever holding it.
 * `body->instructions` is then null and the caller reads through
 * ValidationContext::instruction_at. */
Status decode_body(Builder &builder, Reader &reader, FunctionBody *body,
                   TempVector<Instruction> *sink = nullptr) {
    body->branch_count = 0;
    uint32_t local_groups = 0;
    if (!reader.uleb32(&local_groups))
        return Status::Malformed;
    const uint32_t local_types_base = builder.local_types_all.count();
    uint32_t local_count = 0;
    for (uint32_t group = 0; group < local_groups; ++group) {
        uint32_t group_count = 0;
        uint8_t value_type = 0;
        if (!reader.uleb32(&group_count) || !reader.u8(&value_type) ||
            !valid_value_type(value_type))
            return Status::Malformed;
        if (group_count > kMaxLocals - local_count)
            return Status::Unsupported;
        for (uint32_t index = 0; index < group_count; ++index) {
            if (!builder.local_types_all.append(value_type))
                return Status::OutOfMemory;
        }
        local_count += group_count;
    }
    body->local_count = local_count;
    /* Patched into the shared block once the code section is complete;
     * nothing reads local_types before validate_module runs. */
    body->local_types = nullptr;
    body->local_types_base = local_types_base;

    TempVector<Instruction> owned_operations(builder.arena);
    TempVector<Instruction> &operations = sink ? *sink : owned_operations;
    uint32_t operation_capacity = static_cast<uint32_t>(reader.size / 2 + 1);
    if (operation_capacity > 8192)
        operation_capacity = 8192;
    if (operation_capacity != 0)
        operations.reserve(operation_capacity);
    TempVector<OpenControl> controls(builder.arena);
    if (!controls.reserve(64))
        return Status::OutOfMemory;
    bool function_ended = false;
    while (reader.position < reader.size) {
        if (operations.count() >= kMaxInstructions)
            return Status::Unsupported;
        uint8_t opcode = 0;
        if (!reader.u8(&opcode))
            return Status::Malformed;
        Instruction operation{};
        operation.op = static_cast<Op>(opcode);
        operation.control.else_pc = kNoPc;
        operation.control.end_pc = kNoPc;
        switch (opcode) {
        case 0x00:
            operation.op = Op::Unreachable;
            break;
        case 0x01:
            operation.op = Op::Nop;
            break;
        case 0x02:
        case 0x03:
        case 0x04: {
            uint8_t block_type = 0;
            if (!reader.u8(&block_type) || !valid_block_type(block_type))
                return Status::Malformed;
            operation.op = opcode == 0x02 ? Op::Block : (opcode == 0x03 ? Op::Loop : Op::If);
            operation.block_type = block_type == 0x40 ? 0 : block_type;
            OpenControl control{nullptr, static_cast<uint8_t>(operation.op == Op::Block ? 0x02 : (operation.op == Op::Loop ? 0x03 : 0x04)), false};
            if (controls.count() >= kMaxControlDepth - 1)
                return Status::Unsupported;
            if (!controls.append(control))
                return Status::OutOfMemory;
            break;
        }
        case 0x05: {
            operation.op = Op::Else;
            if (controls.count() == 0 || controls.top()->opcode != 0x04 ||
                controls.top()->has_else)
                return Status::Malformed;
            OpenControl *control = controls.top();
            Instruction *opening = control->opening;
            if (!opening)
                return Status::Internal;
            opening->control.else_pc = operations.count();
            control->has_else = true;
            break;
        }
        case 0x0b: {
            operation.op = Op::End;
            if (controls.count() == 0) {
                function_ended = true;
                if (reader.position != reader.size)
                    return Status::Malformed;
            } else {
                OpenControl *control = controls.top();
                Instruction *opening = control->opening;
                if (!opening)
                    return Status::Internal;
                opening->control.end_pc = operations.count();
                controls.pop();
            }
            break;
        }
        case 0x0c:
        case 0x0d:
            operation.op = opcode == 0x0c ? Op::Br : Op::BrIf;
            if (!reader.uleb32(&operation.operand))
                return Status::Malformed;
            break;
        case 0x0e: {
            uint32_t target_count = 0;
            if (!reader.uleb32(&target_count) || target_count > kMaxBranchTargets - 1)
                return Status::Unsupported;
            if (builder.emit_branch_targets &&
                builder.branch_targets.count() > UINT32_MAX - target_count - 1)
                return Status::OutOfMemory;
            uint32_t default_target = 0;
            for (uint32_t index = 0; index < target_count; ++index) {
                if (!reader.uleb32(&default_target))
                    return Status::Malformed;
                if (builder.emit_branch_targets &&
                    !builder.branch_targets.append(default_target))
                    return Status::OutOfMemory;
            }
            if (!reader.uleb32(&default_target))
                return Status::Malformed;
            if (builder.emit_branch_targets && !builder.branch_targets.append(default_target))
                return Status::OutOfMemory;
            /* br_table: first-target index in `operand`, count in the
             * union. The default target is the LAST entry of the range,
             * so it needs no field of its own -- the decoder used to
             * stash its depth in `operand` and a validator cross-check
             * compared the two, which cannot survive `operand` doubling
             * as the index. */
            operation.operand = body->branch_count;
            operation.operand2 = target_count + 1;
            body->branch_count += target_count + 1;
            operation.op = Op::BrTable;
            break;
        }
        case 0x0f:
            operation.op = Op::Return;
            break;
        case 0x1a:
            operation.op = Op::Drop;
            break;
        case 0x1b:
            operation.op = Op::Select;
            break;
        case 0x20:
        case 0x21:
        case 0x22:
            operation.op = opcode == 0x20 ? Op::LocalGet : (opcode == 0x21 ? Op::LocalSet : Op::LocalTee);
            if (!reader.uleb32(&operation.operand))
                return Status::Malformed;
            break;
        case 0x23:
        case 0x24:
            operation.op = opcode == 0x23 ? Op::GlobalGet : Op::GlobalSet;
            if (!reader.uleb32(&operation.operand))
                return Status::Malformed;
            break;
        case 0x28:
        case 0x29:
        case 0x2a:
        case 0x2b:
        case 0x2c:
        case 0x2d:
        case 0x2e:
        case 0x2f:
        case 0x30:
        case 0x31:
        case 0x32:
        case 0x33:
        case 0x34:
        case 0x35:
        case 0x36:
        case 0x37:
        case 0x38:
        case 0x39:
        case 0x3a:
        case 0x3b:
        case 0x3c:
        case 0x3d:
        case 0x3e:
            if (opcode == 0x28 || (opcode >= 0x2c && opcode <= 0x2f))
                operation.op = Op::I32Load;
            else if (opcode == 0x29 || (opcode >= 0x30 && opcode <= 0x35))
                operation.op = Op::I64Load;
            else if (opcode == 0x2a)
                operation.op = Op::F32Load;
            else if (opcode == 0x2b)
                operation.op = Op::F64Load;
            else if (opcode == 0x36 || (opcode >= 0x3a && opcode <= 0x3b))
                operation.op = Op::I32Store;
            else if (opcode == 0x37 || (opcode >= 0x3c && opcode <= 0x3e))
                operation.op = Op::I64Store;
            else if (opcode == 0x38)
                operation.op = Op::F32Store;
            else
                operation.op = Op::F64Store;
            operation.memory_width = 4;
            operation.memory_signed = false;
            switch (opcode) {
            case 0x29:
            case 0x37:
            case 0x2b:
            case 0x39:
                operation.memory_width = 8;
                break;
            case 0x2c:
            case 0x2d:
                operation.memory_width = 1;
                operation.memory_signed = opcode == 0x2c;
                break;
            case 0x2e:
            case 0x2f:
                operation.memory_width = 2;
                operation.memory_signed = opcode == 0x2e;
                break;
            case 0x30:
            case 0x31:
                operation.memory_width = 1;
                operation.memory_signed = opcode == 0x30;
                break;
            case 0x32:
            case 0x33:
                operation.memory_width = 2;
                operation.memory_signed = opcode == 0x32;
                break;
            case 0x34:
            case 0x35:
                operation.memory_width = 4;
                operation.memory_signed = opcode == 0x34;
                break;
            case 0x3a:
            case 0x3c:
                operation.memory_width = 1;
                break;
            case 0x3b:
            case 0x3d:
                operation.memory_width = 2;
                break;
            case 0x3e:
                operation.memory_width = 4;
                break;
            default:
                break;
            }
            if (!reader.uleb32(&operation.operand) || !reader.uleb32(&operation.operand2))
                return Status::Malformed;
            break;
        case 0x3f:
        case 0x40:
            operation.op = opcode == 0x3f ? Op::MemorySize : Op::MemoryGrow;
            if (!reader.u8(&opcode) || opcode != 0)
                return Status::Malformed;
            break;
        case 0x41: {
            operation.op = Op::I32Const;
            /* i32.const is specified as u32, but it is DECODED as s32
             * here, and that is deliberate. The byte is ambiguous:
             * 0x40 is "64" unsigned and "-64" signed, 0x7F is "127" and
             * "-1". Go's compiler writes i32.const as a *signed* LEB and
             * never emits the 5-byte unsigned form -- no shipped Go
             * artifact here contains `41 ff ff ff ff 0f`, while all of
             * them contain `41 7f` / `41 40`. Reading unsigned makes
             * fs.wasm trap out-of-bounds (every -1 becomes 127), and
             * reading signed makes wat2wasm output for (i32.const 64)
             * read as -64. Every engine that runs Go/wasm resolves it
             * the same way; we do too. See the i32.const section of
             * tools/wasm_owned_session_test.cc, which pins both
             * readings, and MEMORY.md for the evidence. */
            int32_t value = 0;
            if (!reader.sleb32(&value))
                return Status::Malformed;
            operation.immediate = static_cast<uint32_t>(value);
            break;
        }
        case 0x42: {
            operation.op = Op::I64Const;
            int64_t value = 0;
            if (!reader.sleb64(&value))
                return Status::Malformed;
            operation.immediate = static_cast<uint64_t>(value);
            break;
        }
        case 0x43: {
            const uint8_t *bytes = nullptr;
            if (!reader.bytes(4, &bytes))
                return Status::Malformed;
            operation.op = Op::F32Const;
            operation.immediate = static_cast<uint32_t>(bytes[0]) |
                                  (static_cast<uint32_t>(bytes[1]) << 8) |
                                  (static_cast<uint32_t>(bytes[2]) << 16) |
                                  (static_cast<uint32_t>(bytes[3]) << 24);
            break;
        }
        case 0x44: {
            const uint8_t *bytes = nullptr;
            if (!reader.bytes(8, &bytes))
                return Status::Malformed;
            operation.op = Op::F64Const;
            operation.immediate = 0;
            for (uint32_t byte_index = 0; byte_index < 8; ++byte_index)
                operation.immediate |= static_cast<uint64_t>(bytes[byte_index]) << (byte_index * 8);
            break;
        }
        case 0x45:
            operation.op = Op::I32Eqz;
            break;
        case 0x46:
        case 0x47:
        case 0x48:
        case 0x49:
        case 0x4a:
        case 0x4b:
        case 0x4c:
        case 0x4d:
        case 0x4e:
        case 0x4f:
            operation.op = Op::I32Cmp;
            operation.immediate = opcode;
            break;
        case 0x67:
        case 0x68:
        case 0x69:
            operation.op = opcode == 0x67 ? Op::I32Clz :
                           (opcode == 0x68 ? Op::I32Ctz : Op::I32Popcnt);
            break;
        case 0x77:
        case 0x78:
            operation.op = opcode == 0x77 ? Op::I32Rotl : Op::I32Rotr;
            break;
        case 0x79:
        case 0x7a:
        case 0x7b:
            operation.op = opcode == 0x79 ? Op::I64Clz :
                           (opcode == 0x7a ? Op::I64Ctz : Op::I64Popcnt);
            break;
        case 0x89:
        case 0x8a:
            operation.op = opcode == 0x89 ? Op::I64Rotl : Op::I64Rotr;
            break;
        case 0x50:
            operation.op = Op::I64Eqz;
            break;
        case 0x51:
        case 0x52:
        case 0x53:
        case 0x54:
        case 0x55:
        case 0x56:
        case 0x57:
        case 0x58:
        case 0x59:
        case 0x5a:
            operation.op = Op::I64Cmp;
            operation.immediate = opcode;
            break;
        case 0x5b:
        case 0x5c:
        case 0x5d:
        case 0x5e:
        case 0x5f:
        case 0x60:
            operation.op = Op::F32Cmp;
            operation.immediate = opcode;
            break;
        case 0x61:
        case 0x62:
        case 0x63:
        case 0x64:
        case 0x65:
        case 0x66:
            operation.op = Op::F64Cmp;
            operation.immediate = opcode;
            break;
        case 0x6d:
        case 0x6e:
        case 0x6f:
        case 0x70:
            operation.op = opcode == 0x6d ? Op::I32DivS :
                           (opcode == 0x6e ? Op::I32DivU :
                            (opcode == 0x6f ? Op::I32RemS : Op::I32RemU));
            break;
        case 0x71:
        case 0x72:
        case 0x73:
        case 0x74:
        case 0x75:
        case 0x76:
            operation.op = opcode == 0x71 ? Op::I32And :
                           (opcode == 0x72 ? Op::I32Or :
                            (opcode == 0x73 ? Op::I32Xor :
                             (opcode == 0x74 ? Op::I32Shl :
                              (opcode == 0x75 ? Op::I32ShrS : Op::I32ShrU))));
            break;
        case 0x83:
        case 0x84:
        case 0x85:
        case 0x86:
        case 0x87:
        case 0x88:
            operation.op = opcode == 0x83 ? Op::I64And :
                           (opcode == 0x84 ? Op::I64Or :
                            (opcode == 0x85 ? Op::I64Xor :
                             (opcode == 0x86 ? Op::I64Shl :
                              (opcode == 0x87 ? Op::I64ShrS : Op::I64ShrU))));
            break;
        case 0x7c:
            operation.op = Op::I64Add;
            break;
        case 0x7d:
        case 0x7e:
        case 0x7f:
        case 0x80:
        case 0x81:
        case 0x82:
            operation.op = opcode == 0x7d ? Op::I64Sub :
                           (opcode == 0x7e ? Op::I64Mul :
                            (opcode == 0x7f ? Op::I64DivS :
                             (opcode == 0x80 ? Op::I64DivU :
                              (opcode == 0x81 ? Op::I64RemS : Op::I64RemU))));
            break;
        case 0x8b:
        case 0x8c:
        case 0x8d:
        case 0x8e:
        case 0x8f:
        case 0x90:
        case 0x91:
            operation.op = Op::F32Unary;
            operation.immediate = opcode;
            break;
        case 0x92:
        case 0x93:
        case 0x94:
        case 0x95:
        case 0x96:
        case 0x97:
        case 0x98:
            operation.op = Op::F32Binary;
            operation.immediate = opcode;
            break;
        case 0x99:
        case 0x9a:
        case 0x9b:
        case 0x9c:
        case 0x9d:
        case 0x9e:
        case 0x9f:
            operation.op = Op::F64Unary;
            operation.immediate = opcode;
            break;
        case 0xa0:
        case 0xa1:
        case 0xa2:
        case 0xa3:
        case 0xa4:
        case 0xa5:
        case 0xa6:
            operation.op = Op::F64Binary;
            operation.immediate = opcode;
            break;
        case 0xa7:
            operation.op = Op::I32WrapI64;
            break;
        case 0xac:
            operation.op = Op::I64ExtendI32S;
            break;
        case 0xad:
            operation.op = Op::I64ExtendI32U;
            break;
        case 0x6a:
            operation.op = Op::I32Add;
            break;
        case 0x6b:
            operation.op = Op::I32Sub;
            break;
        case 0x6c:
            operation.op = Op::I32Mul;
            break;
        case 0xa8:
        case 0xa9:
        case 0xaa:
        case 0xab:
        case 0xae:
        case 0xaf:
        case 0xb0:
        case 0xb1:
        case 0xb2:
        case 0xb3:
        case 0xb4:
        case 0xb5:
        case 0xb7:
        case 0xb8:
        case 0xb9:
        case 0xba:
            operation.op = Op::FloatConvert;
            operation.immediate = opcode;
            break;
        case 0xb6:
            operation.op = Op::F32DemoteF64;
            break;
        case 0xbb:
            operation.op = Op::F64PromoteF32;
            break;
        case 0xbc:
        case 0xbd:
        case 0xbe:
        case 0xbf:
            operation.op = Op::Reinterpret;
            operation.immediate = opcode;
            break;
        case 0x10:
            operation.op = Op::Call;
            if (!reader.uleb32(&operation.operand))
                return Status::Malformed;
            break;
        case 0x11:
            operation.op = Op::CallIndirect;
            if (!reader.uleb32(&operation.operand) || !reader.uleb32(&operation.operand2))
                return Status::Malformed;
            break;
        case 0xc0:
        case 0xc1:
        case 0xc2:
        case 0xc3:
        case 0xc4:
            operation.op = Op::SignExtend;
            operation.immediate = opcode;
            break;
        case 0xfc: {
            uint32_t subopcode = 0;
            if (!reader.uleb32(&subopcode))
                return Status::Malformed;
            if (subopcode <= 7) {
                operation.op = Op::TruncSat;
                operation.operand = subopcode;
            } else if (subopcode == 10) {
                uint8_t destination_memory = 0;
                uint8_t source_memory = 0;
                if (!reader.u8(&destination_memory) || !reader.u8(&source_memory) ||
                    destination_memory != 0 || source_memory != 0)
                    return Status::Malformed;
                operation.op = Op::MemoryCopy;
            } else if (subopcode == 11) {
                uint8_t memory_index = 0;
                if (!reader.u8(&memory_index) || memory_index != 0)
                    return Status::Malformed;
                operation.op = Op::MemoryFill;
            } else {
                return Status::Unsupported;
            }
            break;
        }
        default:
            return Status::Unsupported;
        }
        Instruction *stored_operation = operations.append_ptr(operation);
        if (!stored_operation)
            return Status::OutOfMemory;
        if (opcode == 0x02 || opcode == 0x03 || opcode == 0x04) {
            if (controls.count() == 0 || !controls.top())
                return Status::Internal;
            controls.top()->opening = stored_operation;
        }
        if (function_ended)
            break;
    }
    if (!function_ended || reader.position != reader.size || controls.count() != 0)
        return Status::Malformed;
    body->instruction_count = operations.count();
    body->instructions = sink ? nullptr : operations.finish();
    return Status::Ok;
}

}

Status validate_module(ModuleData &data, Arena *arena);

struct ArenaBlock {
    ArenaBlock *next;
    /* Address of the pointer that references this block: either &blocks_ or
     * some other block's `next`. release() used to find its block by
     * scanning the whole list, which is O(blocks) per call -- and it is
     * called once per TempVector at consolidation, so decode was O(n^2) in
     * live blocks. Measured on services/goc/go.wasm: 58,429 release calls
     * over a 29,653-block peak walked 127 million list steps and accounted
     * for 13,989 ms of a 14,308 ms instantiate. The back-pointer makes
     * unlink O(1) and changes nothing about what is freed, or when.
     *
     * Cost: 8 bytes per live block. Measured on the real corpus that is
     * +38 KiB on usb.wasm (4.8k blocks) and +237 KiB on go.wasm (29.7k),
     * i.e. ~0.1% of each artifact's instantiation peak -- and it buys the
     * removal of a genuine O(n^2) from the decode path. Verified unchanged
     * by tools/wasm_artifact_test.cc apart from exactly that per-block
     * delta. */
    ArenaBlock **prev;
    uint64_t used;
    uint64_t capacity;
    uint8_t *data;
};

Arena::Arena() : allocator_(nullptr), deallocator_(nullptr), context_(nullptr), blocks_(nullptr) {}

Arena::~Arena() {
    destroy();
}

void Arena::init(Allocator allocator, Deallocator deallocator, void *context) {
    destroy();
    allocator_ = allocator;
    deallocator_ = deallocator;
    context_ = context;
}

void Arena::destroy() {
    ArenaBlock *block = blocks_;
    while (block) {
        ArenaBlock *next = block->next;
        if (deallocator_)
            deallocator_(block, context_);
        block = next;
    }
    blocks_ = nullptr;
    allocator_ = nullptr;
    deallocator_ = nullptr;
    context_ = nullptr;
}

void *Arena::allocate(uint64_t size, uint64_t alignment) {
    if (!allocator_ || !deallocator_)
        return nullptr;
    if (size == 0)
        size = 1;
    if (alignment < 2)
        alignment = 2;
    if ((alignment & (alignment - 1)) != 0)
        return nullptr;
    if (blocks_) {
        ArenaBlock *block = blocks_;
        uint64_t aligned = align_up_u64(block->used, alignment);
        if (aligned != 0 && aligned <= block->capacity && size <= block->capacity - aligned) {
            void *result = block->data + aligned;
            block->used = aligned + size;
            return result;
        }
    }
    /* One block per request.  Two alternatives were measured and are
     * worse: geometric doubling sums to twice the largest block (bad for
     * PEAK, which is what the pool cares about), and a 1 MiB floor turns
     * every TempChunk refill -- 1 KiB at a time -- into a 1 MiB block,
     * which exhausts the pool on the first module.  Rounding waste is
     * better attacked where it happens; note that a 16-byte Instruction
     * makes TempChunk's 64-element chunks exactly 1 KiB, a perfect fit
     * for the heap's power-of-two classes. */
    uint64_t capacity = size > 4096 ? size : 4096;
    if (capacity > UINT64_MAX - alignment)
        return nullptr;
    capacity += alignment;
    uint64_t raw_size = 0;
    if (add_overflow_u64(sizeof(ArenaBlock), capacity, &raw_size))
        return nullptr;
    void *raw = allocator_(raw_size, context_);
    if (!raw)
        return nullptr;
    uintptr_t start = reinterpret_cast<uintptr_t>(raw) + sizeof(ArenaBlock);
    uintptr_t aligned = (start + alignment - 1) & ~(static_cast<uintptr_t>(alignment) - 1);
    uintptr_t offset = aligned - reinterpret_cast<uintptr_t>(raw);
    if (offset > raw_size || capacity > raw_size - offset) {
        deallocator_(raw, context_);
        return nullptr;
    }
    ArenaBlock *block = static_cast<ArenaBlock *>(raw);
    block->next = blocks_;
    if (blocks_)
        blocks_->prev = &block->next;
    block->prev = &blocks_;
    block->used = size;
    block->capacity = capacity - alignment;
    block->data = reinterpret_cast<uint8_t *>(aligned);
    blocks_ = block;
    return block->data;
}

void *Arena::allocate_exclusive(uint64_t size, uint64_t alignment, ArenaBlock **out_block) {
    if (out_block)
        *out_block = nullptr;
    if (!allocator_ || !deallocator_)
        return nullptr;
    if (size == 0)
        size = 1;
    if (alignment < 2)
        alignment = 2;
    if ((alignment & (alignment - 1)) != 0)
        return nullptr;
    uint64_t capacity = size > 4096 ? size : 4096;
    if (capacity > UINT64_MAX - alignment)
        return nullptr;
    capacity += alignment;
    uint64_t raw_size = 0;
    if (add_overflow_u64(sizeof(ArenaBlock), capacity, &raw_size))
        return nullptr;
    void *raw = allocator_(raw_size, context_);
    if (!raw)
        return nullptr;
    uintptr_t start = reinterpret_cast<uintptr_t>(raw) + sizeof(ArenaBlock);
    uintptr_t aligned = (start + alignment - 1) & ~(static_cast<uintptr_t>(alignment) - 1);
    uintptr_t offset = aligned - reinterpret_cast<uintptr_t>(raw);
    if (offset > raw_size || capacity > raw_size - offset) {
        deallocator_(raw, context_);
        return nullptr;
    }
    ArenaBlock *block = static_cast<ArenaBlock *>(raw);
    block->next = blocks_;
    if (blocks_)
        blocks_->prev = &block->next;
    block->prev = &blocks_;
    block->used = capacity - alignment;
    block->capacity = capacity - alignment;
    block->data = reinterpret_cast<uint8_t *>(aligned);
    blocks_ = block;
    if (out_block)
        *out_block = block;
    return block->data;
}

bool Arena::release_block(ArenaBlock *block) {
    /* Unlink in O(1) through ArenaBlock::prev; no search. */
    if (!block)
        return false;
    ArenaBlock *next = block->next;
    ArenaBlock **link = block->prev ? block->prev : &blocks_;
    *link = next;
    if (next)
        next->prev = link;
    block->next = nullptr;
    block->prev = nullptr;
    if (deallocator_)
        deallocator_(block, context_);
    return true;
}

Status Module::ensure_lowered(uint32_t function_index) {
    if (function_index < data_.imported_function_count)
        return Status::Ok;
    uint32_t index = function_index - data_.imported_function_count;
    if (index >= data_.body_count || !data_.bodies)
        return Status::TypeMismatch;
    FunctionBody &body = data_.bodies[index];
    if (body.instructions)
        return Status::Ok;
    if (!body.code || !body.code_size)
        return Status::Malformed;
    /* decode_body needs nothing from the module but an arena: type
     * checking already happened in validate_all_bodies, and the br_table
     * targets are recorded, so this only re-reads the body. */
    Builder builder(&arena_);
    builder.emit_branch_targets = false;
    Reader reader{body.code, body.code_size, 0};
    FunctionBody fresh{};
    fresh.type_index = body.type_index;
    fresh.local_count = body.local_count;
    fresh.local_types = body.local_types;
    fresh.local_types_base = body.local_types_base;
    fresh.branch_base = body.branch_base;
    fresh.branch_count = body.branch_count;
    Status status = decode_body(builder, reader, &fresh);
    if (status != Status::Ok)
        return status;
    /* Must be the same body that was validated at instantiate, or the
     * interpreter would run something no check ever saw. max_stack is
     * deliberately absent: the validator computes that, not the decoder,
     * and it already ran on this body at instantiate. */
    if (fresh.instruction_count != body.instruction_count ||
        fresh.local_count != body.local_count || fresh.branch_count != body.branch_count ||
        !fresh.instructions)
        return Status::Internal;
    body.instructions = fresh.instructions;
    body.instruction_count = fresh.instruction_count;
    return Status::Ok;
}

Module::Module() : data_{} {}

Module::~Module() = default;

const ModuleData &Module::data() const {
    return data_;
}

Status Module::decode(const uint8_t *blob, uint64_t length, Allocator allocator,
                     Deallocator deallocator, void *context) {
    return decode_impl(blob, length, allocator, deallocator, context, true);
}

Status Module::decode_view(const uint8_t *blob, uint64_t length, Allocator allocator,
                          Deallocator deallocator, void *context) {
    return decode_impl(blob, length, allocator, deallocator, context, false);
}

Status Module::decode_impl(const uint8_t *blob, uint64_t length, Allocator allocator,
                          Deallocator deallocator, void *context, bool copy_blob) {
    arena_.destroy();
    data_ = ModuleData{};
    if (!blob || length < 8 || !allocator || !deallocator)
        return Status::Malformed;
    static const uint8_t wasm_magic[4] = {0x00, 0x61, 0x73, 0x6d};
    static const uint8_t wasm_version[4] = {0x01, 0x00, 0x00, 0x00};
    if (__builtin_memcmp(blob, wasm_magic, sizeof(wasm_magic)) != 0 ||
        __builtin_memcmp(blob + 4, wasm_version, sizeof(wasm_version)) != 0)
        return Status::Malformed;
    arena_.init(allocator, deallocator, context);
    const uint8_t *owned_blob = blob;
    if (copy_blob) {
        void *owned = arena_.allocate(length, 1);
        if (!owned)
            return Status::OutOfMemory;
        __builtin_memcpy(owned, blob, length);
        owned_blob = static_cast<const uint8_t *>(owned);
    }

    Reader reader{owned_blob, length, 8};
    Builder builder(&arena_);
    uint32_t last_order = 0;
    uint32_t section_count = 0;

    while (reader.position < reader.size) {
        if (section_count >= kMaxSections)
            return Status::Unsupported;
        ++section_count;
        uint8_t section_id = 0;
        uint32_t section_size = 0;
        if (!reader.u8(&section_id) || !reader.uleb32(&section_size))
            return Status::Malformed;
        const uint8_t *payload = nullptr;
        if (!reader.bytes(section_size, &payload))
            return Status::Malformed;
        Reader section{payload, section_size, 0};
        if (section_id == 0) {
            Slice name{};
            if (!section.name(&name))
                return Status::Malformed;
            if (slice_equal_cstr(name, "abi_ver") && section.remaining(1)) {
                const uint8_t *version = nullptr;
                if (!section.bytes(1, &version))
                    return Status::Malformed;
                data_.has_abi_version = true;
                data_.abi_version = version[0];
            }
            section.position = section.size;
        } else {
            uint32_t order = 0;
            switch (section_id) {
            case 1: order = 1; break;
            case 2: order = 2; break;
            case 3: order = 3; break;
            case 4: order = 4; break;
            case 5: order = 5; break;
            case 6: order = 6; break;
            case 7: order = 7; break;
            case 8: order = 8; break;
            case 9: order = 9; break;
            case 12: order = 10; break;
            case 10: order = 11; break;
            case 11: order = 12; break;
            default: return Status::Unsupported;
            }
            if (order <= last_order)
                return Status::Malformed;
            last_order = order;

            Status result = Status::Malformed;
            switch (section_id) {
            case 1: result = decode_type_section(builder, section); break;
            case 2: result = decode_import_section(builder, section); break;
            case 3: result = decode_function_section(builder, section); break;
            case 4: result = decode_table_section(builder, section, data_); break;
            case 5: result = decode_memory_section(builder, section, data_); break;
            case 6: result = decode_global_section(builder, section); break;
            case 7: result = decode_export_section(builder, section); break;
            case 9: result = decode_element_section(builder, section); break;
            case 8: result = decode_start_section(builder, section, data_); break;
            case 10: {
                uint32_t count = 0;
                /* The walk's lowered form goes to this chunked vector and
                 * is dropped per body: validation happens in
                 * validate_all_bodies, and a function that is never
                 * called must not keep IR alive. */
                TempVector<Instruction> walk_scratch(builder.arena);
                if (!section.uleb32(&count) || count != builder.function_count)
                    return Status::Malformed;
                for (uint32_t index = 0; index < count; ++index) {
                    uint32_t body_size = 0;
                    const uint8_t *body_data = nullptr;
                    if (!section.uleb32(&body_size) || !section.bytes(body_size, &body_data))
                        return Status::Malformed;
                    Reader body{body_data, body_size, 0};
                    FunctionBody function{};
                    /* This body's br_table targets start wherever the
                     * module-wide array has got to. */
                    function.branch_base = builder.branch_targets.count();
                    function.code = body_data;
                    function.code_size = body_size;
                    result = decode_body(builder, body, &function, &walk_scratch);
                    if (result != Status::Ok)
                        return result;
                    walk_scratch.release();
                    if (!builder.bodies.append(function))
                        return Status::OutOfMemory;
                }
                {
                    uint8_t *shared = builder.local_types_all.finish();
                    for (uint32_t index = 0; index < builder.bodies.count(); ++index) {
                        FunctionBody &body = *builder.bodies.at(index);
                        if (body.local_count != 0) {
                            if (!shared)
                                return Status::OutOfMemory;
                            body.local_types = shared + body.local_types_base;
                        }
                    }
                }
                break;
            }
            case 11: result = decode_data_section(builder, section, data_); break;
            case 12: result = decode_data_count_section(builder, section); break;
            default: result = Status::Unsupported; break;
            }
            if (result != Status::Ok)
                return result;
        }
        if (section.position != section.size)
            return Status::Malformed;
    }

    if (builder.function_count != builder.bodies.count())
        return Status::Malformed;
    if (builder.has_data_count && builder.data_count_declared != builder.data.count())
        return Status::Malformed;
    data_.type_count = builder.type_count;
    data_.types = builder.types.finish();
    data_.import_count = builder.import_count;
    data_.imports = builder.imports.finish();
    data_.imported_function_count = builder.imported_function_count;
    data_.defined_function_count = builder.function_count;
    data_.function_types = builder.function_types.finish();
    data_.global_count = builder.globals.count();
    data_.globals = builder.globals.finish();
    data_.element_count = builder.elements.count();
    data_.elements = builder.elements.finish();
    data_.export_count = builder.export_count;
    data_.exports = builder.exports.finish();
    data_.data_count = builder.data.count();
    data_.data = builder.data.finish();
    data_.body_count = builder.bodies.count();
    data_.bodies = builder.bodies.finish();
    for (uint32_t index = 0; index < data_.body_count; ++index)
        data_.bodies[index].type_index = data_.function_types[index];
    if (data_.defined_function_count && !data_.bodies)
        return Status::OutOfMemory;
    for (uint32_t index = 0; index < data_.defined_function_count; ++index) {
        if (data_.function_types[index] >= data_.type_count)
            return Status::TypeMismatch;
    }
    for (uint32_t index = 0; index < data_.import_count; ++index) {
        if (data_.imports[index].type_index >= data_.type_count)
            return Status::TypeMismatch;
    }
    /* Per-body validation happens before the br_table array is
     * consolidated, because the validator reads each body's targets
     * through the builder's chunk list. */
    Status body_status = validate_all_bodies(data_, builder);
    if (body_status != Status::Ok)
        return body_status;
    return validate_module(data_, &arena_);
}

namespace {

enum class ValidationType : uint8_t {
    Bottom,
    I32,
    I64,
    F32,
    F64,
};

constexpr uint8_t kImplicitFunctionControl = 0xff;

struct ValidationControl {
    uint8_t opcode;
    uint32_t height;
    uint32_t label_count;
    ValidationType label_types[4];
    uint32_t end_count;
    ValidationType end_types[4];
    bool unreachable;
    bool has_else;
};

ValidationType validation_type(ValueType type) {
    switch (type) {
    case ValueType::I32: return ValidationType::I32;
    case ValueType::I64: return ValidationType::I64;
    case ValueType::F32: return ValidationType::F32;
    case ValueType::F64: return ValidationType::F64;
    default: return ValidationType::Bottom;
    }
}

bool compatible_types(ValidationType actual, ValidationType expected) {
    return actual == ValidationType::Bottom || expected == ValidationType::Bottom ||
           actual == expected;
}

struct ValidationContext {
    ValidationContext(ModuleData &data, const FuncType &type, FunctionBody &body,
                      ValidationType *stack, ValidationControl *controls,
                      const TempVector<Instruction> *live_instructions = nullptr)
        : data(data), type(type), body(body), stack(stack), controls(controls),
          live_instructions(live_instructions),
          instruction_cursor(live_instructions ? live_instructions->cursor()
                                               : TempVector<Instruction>::Cursor{nullptr, 0}),
          stack_count(0), control_count(0), max_stack(0),
          function_ended(false), status(Status::Ok) {}

    /* Instruction lookup: while a body is being validated out of a
     * chunked vector its array is null. The cursor matters -- validation
     * walks pc forward, and a fresh walk of the chunk list per
     * instruction is quadratic in the chunk count. */
    const Instruction *instruction_at(uint32_t pc) {
        if (live_instructions) {
            if (!live_instructions) {
                instruction_cursor = live_instructions->cursor();
            }
            return live_instructions->nth_from(pc, instruction_cursor);
        }
        return pc < body.instruction_count ? &body.instructions[pc] : nullptr;
    }

    bool fail(Status value) {
        if (status == Status::Ok)
            status = value;
        return false;
    }

    ValidationControl &current_control() {
        return controls[control_count - 1];
    }

    bool push_type(ValidationType value) {
        if (control_count == 0)
            return fail(Status::Internal);
        ValidationControl &control = current_control();
        if (stack_count < control.height)
            return fail(Status::Internal);
        if (stack_count >= kMaxTypeStack)
            return fail(Status::Unsupported);
        stack[stack_count++] = value;
        if (stack_count > max_stack)
            max_stack = stack_count;
        return true;
    }

    bool pop_type(ValidationType expected) {
        if (control_count == 0)
            return fail(Status::Internal);
        ValidationControl &control = current_control();
        if (stack_count < control.height)
            return fail(Status::TypeMismatch);
        if (stack_count == control.height) {
            if (!control.unreachable)
                return fail(Status::TypeMismatch);
            return true;
        }
        ValidationType actual = stack[--stack_count];
        if (!compatible_types(actual, expected))
            return fail(Status::TypeMismatch);
        return true;
    }

    bool pop_types(const ValidationType *types, uint32_t count) {
        for (uint32_t index = count; index != 0; --index) {
            if (!pop_type(types[index - 1]))
                return false;
        }
        return true;
    }

    bool push_types(const ValidationType *types, uint32_t count) {
        for (uint32_t index = 0; index < count; ++index) {
            if (!push_type(types[index]))
                return false;
        }
        return true;
    }

    bool pop_value_types(const ValueType *types, uint32_t count) {
        ValidationType converted[16]{};
        for (uint32_t index = 0; index < count; ++index) {
            converted[index] = validation_type(types[index]);
            if (converted[index] == ValidationType::Bottom)
                return fail(Status::Malformed);
        }
        return pop_types(converted, count);
    }

    bool push_value_types(const ValueType *types, uint32_t count) {
        ValidationType converted[4]{};
        for (uint32_t index = 0; index < count; ++index) {
            converted[index] = validation_type(types[index]);
            if (converted[index] == ValidationType::Bottom)
                return fail(Status::Malformed);
        }
        return push_types(converted, count);
    }

    bool initialize_control() {
        if (type.param_count > 16 || type.result_count > 4)
            return fail(Status::Malformed);
        ValidationControl control{};
        control.opcode = kImplicitFunctionControl;
        control.height = 0;
        control.label_count = type.result_count;
        control.end_count = type.result_count;
        for (uint32_t index = 0; index < type.result_count; ++index) {
            ValidationType result = validation_type(type.results[index]);
            if (result == ValidationType::Bottom)
                return fail(Status::Malformed);
            control.label_types[index] = result;
            control.end_types[index] = result;
        }
        controls[control_count++] = control;
        return true;
    }

    bool push_control(Op op, uint8_t block_type) {
        if (control_count >= kMaxControlDepth)
            return fail(Status::Unsupported);
        ValidationType result = validation_type(static_cast<ValueType>(block_type));
        if (block_type != 0 && result == ValidationType::Bottom)
            return fail(Status::Malformed);
        ValidationControl control{};
        control.opcode = static_cast<uint8_t>(op);
        control.height = stack_count;
        if (block_type != 0) {
            control.end_count = 1;
            control.end_types[0] = result;
        }
        if (op != Op::Loop)
            control.label_count = control.end_count;
        for (uint32_t index = 0; index < control.end_count; ++index)
            control.label_types[index] = control.end_types[index];
        control.unreachable = control_count != 0 && controls[control_count - 1].unreachable;
        controls[control_count++] = control;
        return true;
    }

    bool finish_control(ValidationControl &control) {
        if (!pop_types(control.end_types, control.end_count))
            return false;
        if (stack_count != control.height)
            return fail(Status::TypeMismatch);
        return true;
    }

    void mark_unreachable() {
        if (control_count == 0) {
            status = Status::Internal;
            return;
        }
        ValidationControl &control = current_control();
        control.unreachable = true;
        stack_count = control.height;
    }

    bool end_control() {
        if (control_count == 0)
            return fail(Status::Malformed);
        ValidationControl &control = current_control();
        if (control.opcode == kImplicitFunctionControl) {
            if (control_count != 1)
                return fail(Status::Malformed);
            if (!finish_control(control))
                return false;
            --control_count;
            function_ended = true;
            return true;
        }
        if (control.opcode == static_cast<uint8_t>(Op::If) && !control.has_else &&
            control.end_count != 0)
            return fail(Status::TypeMismatch);
        if (!finish_control(control))
            return false;
        uint32_t result_count = control.end_count;
        ValidationType result_types[4]{};
        for (uint32_t index = 0; index < result_count; ++index)
            result_types[index] = control.end_types[index];
        --control_count;
        return push_types(result_types, result_count);
    }

    bool enter_else() {
        if (control_count == 0)
            return fail(Status::Malformed);
        ValidationControl &control = current_control();
        if (control.opcode != static_cast<uint8_t>(Op::If) || control.has_else)
            return fail(Status::Malformed);
        if (!finish_control(control))
            return false;
        control.has_else = true;
        control.unreachable = control_count > 1 && controls[control_count - 2].unreachable;
        stack_count = control.height;
        return true;
    }

    bool local_type(uint32_t index, ValidationType *value) const {
        if (index < type.param_count) {
            *value = validation_type(type.params[index]);
            return *value != ValidationType::Bottom;
        }
        uint32_t local_index = index - type.param_count;
        if (local_index >= body.local_count || !body.local_types)
            return false;
        *value = validation_type(static_cast<ValueType>(body.local_types[local_index]));
        return *value != ValidationType::Bottom;
    }

    bool require_memory() {
        return data.has_memory || fail(Status::TypeMismatch);
    }

    bool validate_alignment(const Instruction &instruction) {
        uint32_t natural = 0;
        if (instruction.memory_width == 2)
            natural = 1;
        else if (instruction.memory_width == 4)
            natural = 2;
        else if (instruction.memory_width == 8)
            natural = 3;
        return instruction.operand <= natural || fail(Status::TypeMismatch);
    }

    bool validate_load(const Instruction &instruction) {
        if (!require_memory() || !validate_alignment(instruction))
            return false;
        ValidationType result = ValidationType::Bottom;
        if (instruction.op == Op::I32Load) {
            if (instruction.memory_width != 1 && instruction.memory_width != 2 &&
                instruction.memory_width != 4)
                return fail(Status::Malformed);
            result = ValidationType::I32;
        } else if (instruction.op == Op::I64Load) {
            if (instruction.memory_width != 1 && instruction.memory_width != 2 &&
                instruction.memory_width != 4 && instruction.memory_width != 8)
                return fail(Status::Malformed);
            result = ValidationType::I64;
        } else if (instruction.op == Op::F32Load) {
            if (instruction.memory_width != 4 || instruction.memory_signed)
                return fail(Status::Malformed);
            result = ValidationType::F32;
        } else if (instruction.op == Op::F64Load) {
            if (instruction.memory_width != 8 || instruction.memory_signed)
                return fail(Status::Malformed);
            result = ValidationType::F64;
        } else {
            return fail(Status::Malformed);
        }
        return pop_type(ValidationType::I32) && push_type(result);
    }

    bool validate_store(const Instruction &instruction) {
        if (!require_memory() || !validate_alignment(instruction))
            return false;
        ValidationType value_type = ValidationType::Bottom;
        if (instruction.op == Op::I32Store) {
            if (instruction.memory_width != 1 && instruction.memory_width != 2 &&
                instruction.memory_width != 4)
                return fail(Status::Malformed);
            value_type = ValidationType::I32;
        } else if (instruction.op == Op::I64Store) {
            if (instruction.memory_width != 1 && instruction.memory_width != 2 &&
                instruction.memory_width != 4 && instruction.memory_width != 8)
                return fail(Status::Malformed);
            value_type = ValidationType::I64;
        } else if (instruction.op == Op::F32Store) {
            if (instruction.memory_width != 4 || instruction.memory_signed)
                return fail(Status::Malformed);
            value_type = ValidationType::F32;
        } else if (instruction.op == Op::F64Store) {
            if (instruction.memory_width != 8 || instruction.memory_signed)
                return fail(Status::Malformed);
            value_type = ValidationType::F64;
        } else {
            return fail(Status::Malformed);
        }
        return pop_type(value_type) && pop_type(ValidationType::I32);
    }

    bool pop_select_type(ValidationType *value) {
        if (control_count == 0)
            return fail(Status::Internal);
        ValidationControl &control = current_control();
        if (stack_count < control.height)
            return fail(Status::TypeMismatch);
        if (stack_count == control.height) {
            if (!control.unreachable)
                return fail(Status::TypeMismatch);
            *value = ValidationType::Bottom;
            return true;
        }
        *value = stack[--stack_count];
        return true;
    }

    bool validate_select() {
        ValidationType right = ValidationType::Bottom;
        ValidationType left = ValidationType::Bottom;
        if (!pop_type(ValidationType::I32) || !pop_select_type(&right) ||
            !pop_select_type(&left))
            return false;
        if (left != ValidationType::Bottom && right != ValidationType::Bottom &&
            left != right)
            return fail(Status::TypeMismatch);
        ValidationType result = left != ValidationType::Bottom ? left : right;
        return push_type(result);
    }

    bool validate_call(const Instruction &instruction) {
        const FuncType *callee = function_type(data, instruction.operand);
        if (!callee)
            return fail(Status::TypeMismatch);
        if (!pop_value_types(callee->params, callee->param_count))
            return false;
        return push_value_types(callee->results, callee->result_count);
    }

    bool validate_call_indirect(const Instruction &instruction) {
        if (instruction.operand2 != 0 || !data.has_table ||
            instruction.operand >= data.type_count)
            return fail(Status::TypeMismatch);
        const FuncType *callee = &data.types[instruction.operand];
        if (!pop_type(ValidationType::I32) ||
            !pop_value_types(callee->params, callee->param_count))
            return false;
        return push_value_types(callee->results, callee->result_count);
    }

    bool validate_binary(ValidationType operand, ValidationType result) {
        return pop_type(operand) && pop_type(operand) && push_type(result);
    }

    bool validate_unary(ValidationType operand, ValidationType result) {
        return pop_type(operand) && push_type(result);
    }

    bool validate_float_convert(const Instruction &instruction) {
        ValidationType operand = ValidationType::Bottom;
        ValidationType result = ValidationType::Bottom;
        switch (instruction.immediate) {
        case 0xa8:
        case 0xa9:
            operand = ValidationType::F32;
            result = ValidationType::I32;
            break;
        case 0xaa:
        case 0xab:
            operand = ValidationType::F64;
            result = ValidationType::I32;
            break;
        case 0xae:
        case 0xaf:
            operand = ValidationType::F32;
            result = ValidationType::I64;
            break;
        case 0xb0:
        case 0xb1:
            operand = ValidationType::F64;
            result = ValidationType::I64;
            break;
        case 0xb2:
        case 0xb3:
            operand = ValidationType::I32;
            result = ValidationType::F32;
            break;
        case 0xb4:
        case 0xb5:
            operand = ValidationType::I64;
            result = ValidationType::F32;
            break;
        case 0xb7:
        case 0xb8:
            operand = ValidationType::I32;
            result = ValidationType::F64;
            break;
        case 0xb9:
        case 0xba:
            operand = ValidationType::I64;
            result = ValidationType::F64;
            break;
        default:
            return fail(Status::Malformed);
        }
        return validate_unary(operand, result);
    }

    bool validate_reinterpret(const Instruction &instruction) {
        ValidationType operand = ValidationType::Bottom;
        ValidationType result = ValidationType::Bottom;
        switch (instruction.immediate) {
        case 0xbc:
            operand = ValidationType::F32;
            result = ValidationType::I32;
            break;
        case 0xbd:
            operand = ValidationType::F64;
            result = ValidationType::I64;
            break;
        case 0xbe:
            operand = ValidationType::I32;
            result = ValidationType::F32;
            break;
        case 0xbf:
            operand = ValidationType::I64;
            result = ValidationType::F64;
            break;
        default:
            return fail(Status::Malformed);
        }
        return validate_unary(operand, result);
    }

    bool validate_sign_extend(const Instruction &instruction) {
        switch (instruction.immediate) {
        case 0xc0:
        case 0xc1:
            return validate_unary(ValidationType::I32, ValidationType::I32);
        case 0xc2:
        case 0xc3:
        case 0xc4:
            return validate_unary(ValidationType::I64, ValidationType::I64);
        default:
            return fail(Status::Malformed);
        }
    }

    bool validate_trunc_sat(const Instruction &instruction) {
        ValidationType operand = ValidationType::Bottom;
        ValidationType result = ValidationType::Bottom;
        switch (instruction.operand) {
        case 0:
        case 1:
            operand = ValidationType::F32;
            result = ValidationType::I32;
            break;
        case 2:
        case 3:
            operand = ValidationType::F64;
            result = ValidationType::I32;
            break;
        case 4:
        case 5:
            operand = ValidationType::F32;
            result = ValidationType::I64;
            break;
        case 6:
        case 7:
            operand = ValidationType::F64;
            result = ValidationType::I64;
            break;
        default:
            return fail(Status::Malformed);
        }
        return validate_unary(operand, result);
    }

    bool validate_local(const Instruction &instruction) {
        ValidationType value_type = ValidationType::Bottom;
        if (!local_type(instruction.operand, &value_type))
            return fail(Status::TypeMismatch);
        if (instruction.op == Op::LocalGet)
            return push_type(value_type);
        if (!pop_type(value_type))
            return false;
        if (instruction.op == Op::LocalSet)
            return true;
        return push_type(value_type);
    }

    bool validate_global(const Instruction &instruction) {
        if (instruction.operand >= data.global_count || !data.globals)
            return fail(Status::TypeMismatch);
        const GlobalDesc &global = data.globals[instruction.operand];
        if (!valid_value_type(global.value_type))
            return fail(Status::Malformed);
        if (instruction.op == Op::GlobalSet && !global.mutable_value)
            return fail(Status::TypeMismatch);
        ValidationType value_type = validation_type(static_cast<ValueType>(global.value_type));
        if (instruction.op == Op::GlobalGet)
            return push_type(value_type);
        return pop_type(value_type);
    }

    bool validate_memory(const Instruction &instruction) {
        if (!require_memory())
            return false;
        switch (instruction.op) {
        case Op::MemorySize:
            return push_type(ValidationType::I32);
        case Op::MemoryGrow:
            return validate_unary(ValidationType::I32, ValidationType::I32);
        case Op::MemoryCopy:
            return pop_type(ValidationType::I32) && pop_type(ValidationType::I32) &&
                   pop_type(ValidationType::I32);
        case Op::MemoryFill:
            return pop_type(ValidationType::I32) && pop_type(ValidationType::I32) &&
                   pop_type(ValidationType::I32);
        case Op::I32Load:
        case Op::I64Load:
        case Op::F32Load:
        case Op::F64Load:
            return validate_load(instruction);
        case Op::I32Store:
        case Op::I64Store:
        case Op::F32Store:
        case Op::F64Store:
            return validate_store(instruction);
        default:
            return fail(Status::Malformed);
        }
    }

    bool validate_branch(const Instruction &instruction) {
        if (instruction.op == Op::BrTable) {
            const uint32_t base = body.branch_base;
            if (instruction.operand2 == 0 || !data.branch_targets ||
                base > data.branch_target_count ||
                instruction.operand > data.branch_target_count - base ||
                instruction.operand2 > data.branch_target_count - base - instruction.operand)
                return fail(Status::Malformed);
            const uint32_t *targets = data.branch_targets + base + instruction.operand;
            if (!pop_type(ValidationType::I32))
                return false;
            if (control_count == 0)
                return fail(Status::Internal);
            uint32_t default_depth = targets[instruction.operand2 - 1];
            if (default_depth >= control_count)
                return fail(Status::TypeMismatch);
            const ValidationControl &default_control = controls[control_count - 1 - default_depth];
            for (uint32_t index = 0; index < instruction.operand2; ++index) {
                uint32_t depth = targets[index];
                if (depth >= control_count)
                    return fail(Status::TypeMismatch);
                const ValidationControl &target = controls[control_count - 1 - depth];
                if (target.label_count != default_control.label_count)
                    return fail(Status::TypeMismatch);
                for (uint32_t type_index = 0; type_index < target.label_count; ++type_index) {
                    if (target.label_types[type_index] != default_control.label_types[type_index])
                        return fail(Status::TypeMismatch);
                }
            }
            if (!pop_types(default_control.label_types, default_control.label_count))
                return false;
            mark_unreachable();
            return true;
        }
        uint32_t depth = instruction.operand;
        if (instruction.op != Op::Br && instruction.op != Op::BrIf)
            return fail(Status::Malformed);
        if (depth >= control_count)
            return fail(Status::TypeMismatch);
        const ValidationControl &target = controls[control_count - 1 - depth];
        if (instruction.op == Op::BrIf) {
            if (!pop_type(ValidationType::I32) ||
                !pop_types(target.label_types, target.label_count))
                return false;
            return push_types(target.label_types, target.label_count);
        }
        if (!pop_types(target.label_types, target.label_count))
            return false;
        mark_unreachable();
        return true;
    }

    bool validate_instruction(const Instruction &instruction) {
        if (function_ended)
            return fail(Status::Malformed);
        switch (instruction.op) {
        case Op::Unreachable:
            mark_unreachable();
            return true;
        case Op::Nop:
            return true;
        case Op::Block:
        case Op::Loop:
        case Op::If:
            if (instruction.op == Op::If && !pop_type(ValidationType::I32))
                return false;
            return push_control(instruction.op, instruction.block_type);
        case Op::Else:
            return enter_else();
        case Op::End:
            return end_control();
        case Op::Br:
        case Op::BrIf:
        case Op::BrTable:
            return validate_branch(instruction);
        case Op::Return: {
            if (control_count == 0)
                return fail(Status::Internal);
            if (!pop_types(controls[0].end_types, controls[0].end_count))
                return false;
            mark_unreachable();
            return true;
        }
        case Op::Drop:
            return pop_type(ValidationType::Bottom);
        case Op::Select:
            return validate_select();
        case Op::I32Const:
            return push_type(ValidationType::I32);
        case Op::I64Const:
            return push_type(ValidationType::I64);
        case Op::F32Const:
            return push_type(ValidationType::F32);
        case Op::F64Const:
            return push_type(ValidationType::F64);
        case Op::I32Eqz:
            return validate_unary(ValidationType::I32, ValidationType::I32);
        case Op::I32Cmp:
            if (instruction.immediate < 0x46 || instruction.immediate > 0x4f)
                return fail(Status::Malformed);
            return validate_binary(ValidationType::I32, ValidationType::I32);
        case Op::I32Clz:
        case Op::I32Ctz:
        case Op::I32Popcnt:
            return validate_unary(ValidationType::I32, ValidationType::I32);
        case Op::I32Rotl:
        case Op::I32Rotr:
        case Op::I32DivS:
        case Op::I32DivU:
        case Op::I32RemS:
        case Op::I32RemU:
        case Op::I32And:
        case Op::I32Or:
        case Op::I32Xor:
        case Op::I32Shl:
        case Op::I32ShrS:
        case Op::I32ShrU:
        case Op::I32Add:
        case Op::I32Sub:
        case Op::I32Mul:
            return validate_binary(ValidationType::I32, ValidationType::I32);
        case Op::I64Eqz:
            return validate_unary(ValidationType::I64, ValidationType::I32);
        case Op::I64Cmp:
            if (instruction.immediate < 0x51 || instruction.immediate > 0x5a)
                return fail(Status::Malformed);
            return validate_binary(ValidationType::I64, ValidationType::I32);
        case Op::I64Clz:
        case Op::I64Ctz:
        case Op::I64Popcnt:
            return validate_unary(ValidationType::I64, ValidationType::I64);
        case Op::I64Rotl:
        case Op::I64Rotr:
        case Op::I64Sub:
        case Op::I64Mul:
        case Op::I64DivS:
        case Op::I64DivU:
        case Op::I64RemS:
        case Op::I64RemU:
        case Op::I64And:
        case Op::I64Or:
        case Op::I64Xor:
        case Op::I64Shl:
        case Op::I64ShrS:
        case Op::I64ShrU:
        case Op::I64Add:
            return validate_binary(ValidationType::I64, ValidationType::I64);
        case Op::F32Cmp:
            if (instruction.immediate < 0x5b || instruction.immediate > 0x60)
                return fail(Status::Malformed);
            return validate_binary(ValidationType::F32, ValidationType::I32);
        case Op::F64Cmp:
            if (instruction.immediate < 0x61 || instruction.immediate > 0x66)
                return fail(Status::Malformed);
            return validate_binary(ValidationType::F64, ValidationType::I32);
        case Op::F32Unary:
            return validate_unary(ValidationType::F32, ValidationType::F32);
        case Op::F32Binary:
            return validate_binary(ValidationType::F32, ValidationType::F32);
        case Op::F64Unary:
            return validate_unary(ValidationType::F64, ValidationType::F64);
        case Op::F64Binary:
            return validate_binary(ValidationType::F64, ValidationType::F64);
        case Op::I32WrapI64:
            return validate_unary(ValidationType::I64, ValidationType::I32);
        case Op::I64ExtendI32S:
        case Op::I64ExtendI32U:
            return validate_unary(ValidationType::I32, ValidationType::I64);
        case Op::FloatConvert:
            return validate_float_convert(instruction);
        case Op::F32DemoteF64:
            return validate_unary(ValidationType::F64, ValidationType::F32);
        case Op::F64PromoteF32:
            return validate_unary(ValidationType::F32, ValidationType::F64);
        case Op::Reinterpret:
            return validate_reinterpret(instruction);
        case Op::SignExtend:
            return validate_sign_extend(instruction);
        case Op::TruncSat:
            return validate_trunc_sat(instruction);
        case Op::LocalGet:
        case Op::LocalSet:
        case Op::LocalTee:
            return validate_local(instruction);
        case Op::GlobalGet:
        case Op::GlobalSet:
            return validate_global(instruction);
        case Op::Call:
            return validate_call(instruction);
        case Op::CallIndirect:
            return validate_call_indirect(instruction);
        case Op::MemorySize:
        case Op::MemoryGrow:
        case Op::I32Load:
        case Op::I32Store:
        case Op::I64Load:
        case Op::I64Store:
        case Op::F32Load:
        case Op::F32Store:
        case Op::F64Load:
        case Op::F64Store:
        case Op::MemoryCopy:
        case Op::MemoryFill:
            return validate_memory(instruction);
        default:
            return fail(Status::Malformed);
        }
    }

    bool validate() {
        if (body.instruction_count > kMaxInstructions)
            return fail(Status::Unsupported);
        if (body.instruction_count == 0 || (!live_instructions && !body.instructions))
            return fail(Status::Malformed);
        if (body.local_count != 0 && !body.local_types)
            return fail(Status::Malformed);
        if (!initialize_control())
            return false;
        for (uint32_t pc = 0; pc < body.instruction_count; ++pc) {
            const Instruction *instruction = instruction_at(pc);
            if (!instruction || !validate_instruction(*instruction))
                return fail(Status::Malformed);
        }
        if (!function_ended || control_count != 0)
            return fail(Status::Malformed);
        body.max_stack = max_stack;
        return status == Status::Ok;
    }

    Status status_value() const {
        return status;
    }

    ModuleData &data;
    const FuncType &type;
    FunctionBody &body;
    ValidationType *stack;
    ValidationControl *controls;
    const TempVector<Instruction> *live_instructions;
    TempVector<Instruction>::Cursor instruction_cursor;
    uint32_t stack_count;
    uint32_t control_count;
    uint32_t max_stack;
    bool function_ended;
    Status status;
};

Status validate_module_impl(ModuleData &data, Arena *arena) {
    if (!arena)
        return Status::Internal;
    if (data.type_count != 0 && !data.types)
        return Status::Malformed;
    for (uint32_t index = 0; index < data.type_count; ++index) {
        const FuncType &type = data.types[index];
        if (type.param_count > 16 || type.result_count > 4)
            return Status::Unsupported;
        for (uint32_t param = 0; param < type.param_count; ++param) {
            if (!valid_value_type(static_cast<uint8_t>(type.params[param])))
                return Status::Malformed;
        }
        for (uint32_t result = 0; result < type.result_count; ++result) {
            if (!valid_value_type(static_cast<uint8_t>(type.results[result])))
                return Status::Malformed;
        }
    }
    if (data.import_count != 0 && !data.imports)
        return Status::Malformed;
    uint32_t imported_function_count = 0;
    for (uint32_t index = 0; index < data.import_count; ++index) {
        if (data.imports[index].kind != 0)
            return Status::Unsupported;
        if (data.imports[index].type_index >= data.type_count)
            return Status::TypeMismatch;
        ++imported_function_count;
    }
    if (imported_function_count != data.imported_function_count)
        return Status::Malformed;
    if (data.defined_function_count != data.body_count)
        return Status::Malformed;
    if (data.defined_function_count != 0 &&
        (!data.function_types || !data.bodies))
        return Status::Malformed;
    for (uint32_t index = 0; index < data.defined_function_count; ++index) {
        if (data.function_types[index] >= data.type_count)
            return Status::TypeMismatch;
    }
    if (data.global_count != 0 && !data.globals)
        return Status::Malformed;
    for (uint32_t index = 0; index < data.global_count; ++index) {
        if (!valid_value_type(data.globals[index].value_type) ||
            !data.globals[index].constant_init)
            return Status::Malformed;
    }
    if (data.export_count != 0 && !data.exports)
        return Status::Malformed;
    uint32_t function_count = data.imported_function_count + data.defined_function_count;
    for (uint32_t index = 0; index < data.export_count; ++index) {
        const Export &export_value = data.exports[index];
        if (export_value.kind == 0) {
            if (export_value.index >= function_count)
                return Status::TypeMismatch;
        } else if (export_value.kind == 2) {
            if (export_value.index != 0 || !data.has_memory)
                return Status::TypeMismatch;
        } else {
            return Status::Unsupported;
        }
    }
    if (data.has_start) {
        if (data.start_function >= function_count)
            return Status::TypeMismatch;
        const FuncType *start_type = function_type(data, data.start_function);
        if (!start_type || start_type->param_count != 0 || start_type->result_count != 0)
            return Status::TypeMismatch;
    }
    if (data.element_count != 0 && !data.elements)
        return Status::Malformed;
    if (data.element_count != 0 && !data.has_table)
        return Status::TypeMismatch;
    for (uint32_t index = 0; index < data.element_count; ++index) {
        const ElementSegment &segment = data.elements[index];
        if (segment.table_index != 0 || !segment.has_offset || segment.offset < 0 ||
            (segment.function_count != 0 && !segment.functions))
            return Status::Malformed;
        uint32_t offset = static_cast<uint32_t>(segment.offset);
        if (segment.function_count > data.table.min_size ||
            offset > data.table.min_size - segment.function_count)
            return Status::TypeMismatch;
        for (uint32_t function_index = 0; function_index < segment.function_count; ++function_index) {
            if (segment.functions[function_index] >= function_count)
                return Status::TypeMismatch;
        }
    }
    if (data.data_count != 0 && !data.data)
        return Status::Malformed;
    if (data.data_count != 0 && !data.has_memory)
        return Status::TypeMismatch;

    return Status::Ok;
}

/* Per-body validation, run as its own pass over the code section.
 *
 * Every body is validated here, at instantiate, exactly as before -- but
 * one body at a time, out of a chunked vector that is dropped
 * immediately afterwards, so the module never holds the lowered form of
 * a function nobody calls. Measured on the real corpus only 8-9% of a
 * Go service's functions are ever entered, and the lowered IR was 52-63%
 * of every module's arena. Validation is not deferred to first call: an
 * ill-typed or unsupported body still fails instantiation.
 *
 * The bodies were already walked once by the code section to record
 * their structure (locals, instruction count, br_table targets), so this
 * pass re-lowers each body to validate it. */
Status validate_all_bodies(ModuleData &data, Builder &builder) {
    if (!builder.arena)
        return Status::Internal;
    uint64_t stack_bytes = static_cast<uint64_t>(kMaxTypeStack) * sizeof(ValidationType);
    uint64_t control_bytes = static_cast<uint64_t>(kMaxControlDepth) * sizeof(ValidationControl);
    ValidationType *stack = static_cast<ValidationType *>(
        builder.arena->allocate(stack_bytes, alignof(ValidationType)));
    ValidationControl *controls = static_cast<ValidationControl *>(
        builder.arena->allocate(control_bytes, alignof(ValidationControl)));
    if (!stack || !controls)
        return Status::OutOfMemory;
    /* The code section has already recorded every body's br_table
     * targets, so consolidate them before validating: reading them out
     * of the builder's 64-entry chunk list instead cost 100 s on go.wasm
     * (1.6 billion chunk hops). */
    data.branch_target_count = builder.branch_targets.count();
    data.branch_targets = builder.branch_targets.finish();
    /* One reused vector: a body's lowered form lives in its chunks only
     * for as long as that body takes to validate. */
    TempVector<Instruction> scratch(builder.arena);
    for (uint32_t index = 0; index < data.defined_function_count; ++index) {
        uint32_t function_index = data.imported_function_count + index;
        const FuncType *type = function_type(data, function_index);
        if (!type)
            return Status::TypeMismatch;
        FunctionBody &body = data.bodies[index];
        if (body.type_index != data.function_types[index])
            return Status::Malformed;
        if (!body.code || !body.code_size)
            return Status::Malformed;
        if (body.instruction_count > kMaxInstructions)
            return Status::Unsupported;
        Reader reader{body.code, body.code_size, 0};
        FunctionBody replay{};
        /* The br_table targets are already recorded from the code
         * section's walk; re-recording them here would append a second
         * copy and shift every later body's branch_base. */
        const bool emit_targets = builder.emit_branch_targets;
        builder.emit_branch_targets = false;
        replay.type_index = body.type_index;
        replay.local_count = body.local_count;
        replay.local_types = body.local_types;
        replay.local_types_base = body.local_types_base;
        replay.branch_base = body.branch_base;
        replay.branch_count = body.branch_count;
        Status status = decode_body(builder, reader, &replay, &scratch);
        builder.emit_branch_targets = emit_targets;
        if (status != Status::Ok)
            return status;
        /* The replay must agree with the code section's walk, or the
         * interpreter would later run something that was never checked. */
        if (replay.instruction_count != body.instruction_count ||
            replay.local_count != body.local_count || replay.branch_count != body.branch_count)
            return Status::Internal;
        ValidationContext context(data, *type, body, stack, controls, &scratch);
        if (!context.validate())
            return context.status_value();
        scratch.release();
    }
    return Status::Ok;
}

}

Status validate_module(ModuleData &data, Arena *arena) {
    return validate_module_impl(data, arena);
}

Runtime::Runtime()
    : module_(nullptr), allocator_(nullptr), deallocator_(nullptr), allocator_context_(nullptr),
      config_{}, values_(nullptr), value_count_(0), frames_(nullptr), frame_count_(0),
      labels_(nullptr), label_count_(0), globals_(nullptr), global_count_(0),
      table_(nullptr), table_size_(0),
      memory_(nullptr), memory_size_(0), memory_capacity_(0),
      instantiated_(false), running_(false), preempt_pending_(false), exit_requested_(false),
      pending_function_(0), exit_code_(0), error_(Status::Ok),
      trap_function_(0), trap_pc_(0), error_import_(kNoImport),
      pending_results_(nullptr),
      pending_result_count_(0), pending_result_capacity_(0), imports_(nullptr), import_count_(0) {}

Runtime::~Runtime() {
    if (memory_ && deallocator_)
        deallocator_(memory_, allocator_context_);
    memory_ = nullptr;
    memory_size_ = 0;
    memory_capacity_ = 0;
    arena_.destroy();
}

Status Runtime::instantiate(Module &module, Allocator allocator,
                             Deallocator deallocator, void *context,
                             const RuntimeConfig &config) {
    if (memory_ && deallocator_)
        deallocator_(memory_, allocator_context_);
    memory_ = nullptr;
    memory_size_ = 0;
    memory_capacity_ = 0;
    arena_.destroy();
    module_ = &module;
    allocator_ = allocator;
    deallocator_ = deallocator;
    allocator_context_ = context;
    config_ = config;
    if (config_.max_values == 0)
        config_.max_values = 4096;
    if (config_.max_frames == 0)
        config_.max_frames = 64;
    if (config_.max_labels == 0)
        config_.max_labels = 4096;
    if (config_.max_table_entries == 0)
        config_.max_table_entries = 1 << 20;
    if (config_.max_memory_bytes == 0)
        config_.max_memory_bytes = 16 * 1024 * 1024;
    if (!allocator || !deallocator)
        return Status::Internal;
    arena_.init(allocator, deallocator, context);
    values_ = static_cast<Value *>(arena_.allocate(
        static_cast<uint64_t>(config_.max_values) * sizeof(Value), alignof(Value)));
    frames_ = static_cast<Frame *>(arena_.allocate(
        static_cast<uint64_t>(config_.max_frames) * sizeof(Frame), alignof(Frame)));
    labels_ = static_cast<Label *>(arena_.allocate(
        static_cast<uint64_t>(config_.max_labels) * sizeof(Label), alignof(Label)));
    if (!values_ || !frames_ || !labels_)
        return Status::OutOfMemory;
    __builtin_memset(values_, 0, static_cast<size_t>(config_.max_values) * sizeof(Value));
    __builtin_memset(frames_, 0, static_cast<size_t>(config_.max_frames) * sizeof(Frame));
    __builtin_memset(labels_, 0, static_cast<size_t>(config_.max_labels) * sizeof(Label));
    value_count_ = 0;
    frame_count_ = 0;
    label_count_ = 0;
    error_ = Status::Ok;
    running_ = false;
    preempt_pending_ = false;
    exit_requested_ = false;
    exit_code_ = 0;
    pending_results_ = nullptr;
    pending_result_count_ = 0;
    pending_result_capacity_ = 0;

    const ModuleData &module_data = module.data();
    import_count_ = module_data.import_count;
    if (import_count_ != 0) {
        imports_ = static_cast<HostImport *>(arena_.allocate(
            static_cast<uint64_t>(import_count_) * sizeof(HostImport), alignof(HostImport)));
        if (!imports_)
            return Status::OutOfMemory;
        __builtin_memset(imports_, 0, static_cast<size_t>(import_count_) * sizeof(HostImport));
    }
    globals_ = nullptr;
    global_count_ = module_data.global_count;
    if (global_count_ != 0) {
        globals_ = static_cast<Value *>(arena_.allocate(
            static_cast<uint64_t>(global_count_) * sizeof(Value), alignof(Value)));
        if (!globals_)
            return Status::OutOfMemory;
        for (uint32_t index = 0; index < global_count_; ++index) {
            const GlobalDesc &global = module_data.globals[index];
            if (!global.constant_init)
                return Status::Unsupported;
            uint64_t bits = static_cast<uint64_t>(global.initial_value);
            if (global.value_type == static_cast<uint8_t>(ValueType::I32) ||
                global.value_type == static_cast<uint8_t>(ValueType::F32))
                bits &= 0xffffffffu;
            globals_[index] = Value{bits};
        }
    }
    table_ = nullptr;
    table_size_ = 0;
    if (module_data.has_table) {
        if (module_data.table.min_size > config_.max_table_entries)
            return Status::OutOfMemory;
        uint64_t table_bytes = 0;
        if (mul_overflow_u64(sizeof(uint32_t), module_data.table.min_size, &table_bytes))
            return Status::OutOfMemory;
        if (table_bytes != 0) {
            table_ = static_cast<uint32_t *>(arena_.allocate(table_bytes, alignof(uint32_t)));
            if (!table_)
                return Status::OutOfMemory;
            for (uint32_t index = 0; index < module_data.table.min_size; ++index)
                table_[index] = UINT32_MAX;
        }
        table_size_ = module_data.table.min_size;
        for (uint32_t segment_index = 0; segment_index < module_data.element_count; ++segment_index) {
            const ElementSegment &segment = module_data.elements[segment_index];
            if (segment.table_index != 0 || !segment.has_offset || segment.offset < 0)
                return Status::Malformed;
            uint32_t offset = static_cast<uint32_t>(segment.offset);
            if (segment.function_count > table_size_ ||
                offset > table_size_ - segment.function_count)
                return Status::Malformed;
            for (uint32_t function_index = 0; function_index < segment.function_count; ++function_index) {
                uint32_t target = segment.functions[function_index];
                if (target >= module_data.imported_function_count + module_data.defined_function_count)
                    return Status::TypeMismatch;
                table_[offset + function_index] = target;
            }
        }
    }
    memory_size_ = 0;
    memory_capacity_ = 0;
    memory_ = nullptr;
    if (module_data.has_memory) {
        uint64_t minimum = static_cast<uint64_t>(module_data.memory.min_pages) * kPageSize;
        if (minimum > config_.max_memory_bytes)
            return Status::OutOfMemory;
        uint64_t initial_capacity = minimum == 0 ? kPageSize : minimum;
        if (minimum > 1u * 1024u * 1024u) {
            /* Headroom so memory.grow does not copy on every step, sized
             * PROPORTIONALLY. The old policy had a cliff: anything
             * declaring more than 16 MiB reserved 64 MiB outright, so
             * shell.wasm (34.75 MiB) took nearly twice what it needed
             * and a sixth service could not fit the pool. Go's heap
             * doubles, so 1.5x covers the next doubling; the floor keeps
             * small modules from growing one page at a time, and the
             * configured cap still applies. */
            uint64_t reserve = minimum + minimum / 2;
            if (reserve < 4u * 1024u * 1024u)
                reserve = 4u * 1024u * 1024u;
            if (reserve > config_.max_memory_bytes)
                reserve = config_.max_memory_bytes;
            if (initial_capacity < reserve)
                initial_capacity = reserve;
        }
        memory_capacity_ = initial_capacity;
        memory_ = static_cast<uint8_t *>(allocator_(memory_capacity_, allocator_context_));
        if (!memory_)
            return Status::OutOfMemory;
        __builtin_memset(memory_, 0, static_cast<size_t>(memory_capacity_));
        memory_size_ = minimum;
        for (uint32_t index = 0; index < module_data.data_count; ++index) {
            const DataSegment &segment = module_data.data[index];
            if (!check_memory(segment.offset, segment.size))
                return Status::Malformed;
            __builtin_memcpy(memory_ + segment.offset, segment.bytes, segment.size);
        }
    }
    instantiated_ = true;
    return Status::Ok;
}

Status Runtime::bind_import(const char *module_name, const char *field_name,
                            ImportInvoke invoke, void *context) {
    return bind_import_typed(module_name, field_name, nullptr, invoke, context);
}

Status Runtime::bind_import_typed(const char *module_name, const char *field_name,
                                  const FuncType *expected, ImportInvoke invoke,
                                  void *context) {
    if (!instantiated_ || !module_name || !field_name || !invoke)
        return Status::Internal;
    const ModuleData &module_data = module_->data();
    uint32_t matches = 0;
    for (uint32_t index = 0; index < module_data.import_count; ++index) {
        const Import &import = module_data.imports[index];
        if (import.kind != 0 ||
            !slice_equal_cstr(import.module_name, module_name) ||
            !slice_equal_cstr(import.field_name, field_name))
            continue;
        if (expected) {
            const FuncType *actual = &module_data.types[import.type_index];
            if (!same_function_type(*expected, *actual))
                return Status::TypeMismatch;
        }
        ++matches;
    }
    if (matches == 0)
        return Status::MissingImport;
    for (uint32_t index = 0; index < module_data.import_count; ++index) {
        const Import &import = module_data.imports[index];
        if (import.kind != 0 ||
            !slice_equal_cstr(import.module_name, module_name) ||
            !slice_equal_cstr(import.field_name, field_name))
            continue;
        imports_[index].module_name = module_name;
        imports_[index].field_name = field_name;
        imports_[index].invoke = invoke;
        imports_[index].context = context;
    }
    return Status::Ok;
}

bool Runtime::imports_bound() const {
    if (!instantiated_ || !module_)
        return false;
    for (uint32_t index = 0; index < module_->data().import_count; ++index) {
        if (!imports_ || !imports_[index].invoke)
            return false;
    }
    return true;
}

RunStatus Runtime::invoke(const Slice &name, const Value *args, uint32_t arg_count,
                           Value *results, uint32_t result_capacity) {
    if (!instantiated_)
        return RunStatus::Error;
    uint32_t function_index = 0;
    if (!find_function(name, &function_index))
        return RunStatus::Error;
    const FuncType *type = function_type(module_->data(), function_index);
    if (!type || arg_count != type->param_count || result_capacity < type->result_count)
        return RunStatus::Error;
    value_count_ = 0;
    frame_count_ = 0;
    label_count_ = 0;
    for (uint32_t index = 0; index < arg_count; ++index) {
        values_[value_count_++] = args[index];
    }
    pending_results_ = results;
    pending_result_count_ = type->result_count;
    pending_result_capacity_ = result_capacity;
    error_ = Status::Ok;
    running_ = true;
    exit_requested_ = false;
    Status enter_status = enter_frame(function_index);
    if (enter_status != Status::Ok) {
        error_ = enter_status;
        running_ = false;
        return enter_status == Status::TrapStackOverflow ? RunStatus::Trapped : RunStatus::Error;
    }
    return step(results, result_capacity);
}

RunStatus Runtime::resume(Value *results, uint32_t result_capacity) {
    if (!running_) {
        error_ = Status::Internal;
        return RunStatus::Error;
    }
    pending_results_ = results;
    pending_result_capacity_ = result_capacity;
    return step(results, result_capacity);
}

void Runtime::request_preempt() {
    if (running_)
        preempt_pending_ = true;
}

uint8_t *Runtime::memory() const {
    return memory_;
}

uint64_t Runtime::memory_size() const {
    return memory_size_;
}

uint32_t Runtime::exit_code() const {
    return exit_code_;
}

Status Runtime::error() const {
    return error_;
}

uint32_t Runtime::trap_function() const {
    return trap_function_;
}

uint32_t Runtime::trap_pc() const {
    return trap_pc_;
}

uint32_t Runtime::error_import() const {
    return error_import_;
}

/* Record a trap together with WHERE it happened.  Assigning error_
 * directly (as the memory traps in the instruction loop used to) leaves
 * the location at zero, which makes a guest trap untriageable. */
void Runtime::raise(Status status) {
    set_error(status);
}

void Runtime::set_error(Status status) {
    error_ = status;
    /* frame.pc has already been advanced past the instruction that
     * faulted, so report the instruction itself. */
    trap_function_ = 0;
    trap_pc_ = 0;
    if (frame_count_ > 0 && frames_) {
        const Frame &frame = frames_[frame_count_ - 1];
        trap_function_ = frame.function_index;
        trap_pc_ = frame.pc > 0 ? frame.pc - 1 : 0;
    }
}

uint32_t Runtime::function_type_index(uint32_t function_index) const {
    const FuncType *type = function_type(module_->data(), function_index);
    if (!type)
        return UINT32_MAX;
    for (uint32_t index = 0; index < module_->data().type_count; ++index) {
        if (&module_->data().types[index] == type)
            return index;
    }
    return UINT32_MAX;
}

const FunctionBody *Runtime::function_body(uint32_t function_index) const {
    return body_for(module_->data(), function_index);
}

bool Runtime::find_function(const Slice &name, uint32_t *function_index) const {
    const ModuleData &module_data = module_->data();
    for (uint32_t index = 0; index < module_data.export_count; ++index) {
        const Export &export_value = module_data.exports[index];
        if (export_value.kind == 0 && export_value.name.size == name.size &&
            __builtin_memcmp(export_value.name.data, name.data, name.size) == 0) {
            *function_index = export_value.index;
            return true;
        }
    }
    return false;
}

bool Runtime::local_type(uint32_t function_index, uint32_t local_index,
                         ValueType *type) const {
    const FuncType *function = function_type(module_->data(), function_index);
    const FunctionBody *body = function_body(function_index);
    if (!function || !body)
        return false;
    if (local_index < function->param_count) {
        *type = function->params[local_index];
        return true;
    }
    uint32_t declared = local_index - function->param_count;
    if (declared >= body->local_count)
        return false;
    *type = static_cast<ValueType>(body->local_types[declared]);
    return true;
}

bool Runtime::pop_value(Value *value) {
    if (!value || value_count_ == 0)
        return false;
    *value = values_[--value_count_];
    return true;
}

bool Runtime::push_value(const Value &value) {
    if (value_count_ >= config_.max_values)
        return false;
    values_[value_count_++] = value;
    return true;
}

bool Runtime::check_memory(uint64_t offset, uint64_t size) const {
    return offset <= memory_size_ && size <= memory_size_ - offset;
}

/* Capacity kept above the requested size so the guest's next
 * memory.grow is a memset inside the existing buffer instead of a
 * reallocation that copies the whole memory. Without it, a session grown
 * to the net window placement (68 MiB) would need a transient second copy
 * of that 68 MiB on its first heap doubling. */
static constexpr uint64_t kEnsureSlackBytes = 8u * 1024u * 1024u;

bool Runtime::ensure_memory_bytes(uint64_t min_bytes) {
    uint64_t want = align_up_u64(min_bytes, kPageSize);
    if (want == 0)
        return false;
    if (want <= memory_size_)
        return true;
    const ModuleData &module_data = module_->data();
    if (module_data.memory.has_max &&
        want > static_cast<uint64_t>(module_data.memory.max_pages) * kPageSize)
        return false;
    uint64_t capacity = want;
    if (add_overflow_u64(capacity, kEnsureSlackBytes, &capacity) ||
        capacity > UINT64_MAX - kPageSize)
        return false;
    capacity = align_up_u64(capacity, kPageSize);
    if (capacity == 0)
        return false;
    if (capacity > config_.max_memory_bytes)
        config_.max_memory_bytes = capacity;
    if (want > memory_capacity_) {
        uint8_t *new_memory =
            static_cast<uint8_t *>(allocator_(capacity, allocator_context_));
        if (!new_memory)
            return false;
        __builtin_memcpy(new_memory, memory_, static_cast<size_t>(memory_size_));
        __builtin_memset(new_memory + memory_size_, 0,
                         static_cast<size_t>(capacity - memory_size_));
        deallocator_(memory_, allocator_context_);
        memory_ = new_memory;
        memory_capacity_ = capacity;
    }
    __builtin_memset(memory_ + memory_size_, 0,
                     static_cast<size_t>(want - memory_size_));
    memory_size_ = want;
    return true;
}

bool Runtime::grow_memory(uint32_t pages, uint32_t *old_pages) {
    *old_pages = static_cast<uint32_t>(memory_size_ / kPageSize);
    uint64_t requested = static_cast<uint64_t>(pages) * kPageSize;
    uint64_t current = memory_size_;
    uint64_t grown = 0;
    if (add_overflow_u64(current, requested, &grown) || grown > config_.max_memory_bytes)
        return false;
    const ModuleData &module_data = module_->data();
    if (module_data.memory.has_max && grown >
            static_cast<uint64_t>(module_data.memory.max_pages) * kPageSize)
        return false;
    if (grown == current)
        return true;
    if (grown <= memory_capacity_) {
        __builtin_memset(memory_ + current, 0, static_cast<size_t>(grown - current));
        memory_size_ = grown;
        return true;
    }
    uint8_t *new_memory = static_cast<uint8_t *>(allocator_(grown, allocator_context_));
    if (!new_memory)
        return false;
    __builtin_memcpy(new_memory, memory_, static_cast<size_t>(current));
    __builtin_memset(new_memory + current, 0, static_cast<size_t>(grown - current));
    deallocator_(memory_, allocator_context_);
    memory_ = new_memory;
    memory_capacity_ = grown;
    memory_size_ = grown;
    return true;
}

Status Runtime::enter_frame(uint32_t function_index) {
    const FuncType *type = function_type(module_->data(), function_index);
    const FunctionBody *body = function_body(function_index);
    if (!type || !body)
        return Status::TypeMismatch;
    if (!body->instructions) {
        Status lowered = module_->ensure_lowered(function_index);
        if (lowered != Status::Ok)
            return lowered;
        body = function_body(function_index);
        if (!body || !body->instructions)
            return Status::Internal;
    }
    if (value_count_ < type->param_count)
        return Status::TypeMismatch;
    uint32_t locals_base = value_count_ - type->param_count;
    uint64_t total_locals = static_cast<uint64_t>(type->param_count) + body->local_count;
    if (total_locals > config_.max_values - locals_base)
        return Status::TrapStackOverflow;
    uint64_t values_base = locals_base + total_locals;
    if (body->max_stack > config_.max_values - values_base)
        return Status::TrapStackOverflow;
    for (uint32_t index = 0; index < body->local_count; ++index)
        values_[locals_base + type->param_count + index] = Value{0};
    if (frame_count_ >= config_.max_frames)
        return Status::TrapStackOverflow;
    if (label_count_ > config_.max_labels)
        return Status::TrapStackOverflow;
    Frame frame{function_index, 0, locals_base,
                static_cast<uint32_t>(locals_base + total_locals), label_count_};
    frames_[frame_count_++] = frame;
    value_count_ = frame.values_base;
    return Status::Ok;
}

RunStatus Runtime::execute_import(uint32_t function_index) {
    error_import_ = kNoImport;
    const ModuleData &module_data = module_->data();
    if (function_index >= module_data.import_count) {
        error_ = Status::TypeMismatch;
        running_ = false;
        return RunStatus::Error;
    }
    const Import &import = module_data.imports[function_index];
    const HostImport &binding = imports_[function_index];
    if (!binding.invoke) {
        error_ = Status::MissingImport;
        running_ = false;
        return RunStatus::Error;
    }
    const FuncType *type = &module_data.types[import.type_index];
    if (value_count_ < type->param_count) {
        error_ = Status::TypeMismatch;
        running_ = false;
        return RunStatus::Error;
    }
    Value arguments[16]{};
    uint32_t argument_base = value_count_ - type->param_count;
    for (uint32_t index = 0; index < type->param_count; ++index)
        arguments[index] = values_[argument_base + index];
    value_count_ = argument_base;
    RunStatus status = binding.invoke(binding.context, *this, arguments,
                                       values_ + value_count_, type->result_count,
                                       &exit_code_);
    if (status == RunStatus::Completed) {
        if (value_count_ > config_.max_values - type->result_count) {
            error_ = Status::Internal;
            running_ = false;
            return RunStatus::Error;
        }
        value_count_ += type->result_count;
    } else if (status == RunStatus::Exited) {
        exit_requested_ = true;
        running_ = false;
    } else if (status == RunStatus::Trapped || status == RunStatus::Error) {
        /* A host import reporting failure is the most common cause of a
         * guest trap, and the guest may well have handled it. Record
         * WHICH import so the kernel -- which owns the console and can
         * resolve the name -- can say so. The engine stays arch-blind
         * and allocation-free here. */
        error_import_ = function_index;
        running_ = false;
    }
    return status;
}

bool Runtime::branch_to(uint32_t depth) {
    if (frame_count_ == 0)
        return false;
    Frame &frame = frames_[frame_count_ - 1];
    uint32_t explicit_labels = label_count_ - frame.labels_base;
    if (depth == explicit_labels) {
        const FunctionBody *body = function_body(frame.function_index);
        if (!body)
            return false;
        frame.pc = body->instruction_count;
        label_count_ = frame.labels_base;
        return true;
    }
    if (depth > explicit_labels)
        return false;
    uint32_t label_index = label_count_ - 1 - depth;
    const Label &target = labels_[label_index];
    uint32_t keep = target.kind == 0x03 ? 0 : target.result_count;
    if (value_count_ < target.value_height + keep)
        return false;
    for (uint32_t index = 0; index < keep; ++index)
        values_[target.value_height + index] = values_[value_count_ - keep + index];
    value_count_ = target.value_height + keep;
    if (target.kind == 0x03) {
        frame.pc = target.start_pc;
        label_count_ = label_index + 1;
    } else {
        frame.pc = target.end_pc + 1;
        label_count_ = label_index;
    }
    return true;
}

RunStatus Runtime::return_from_function(Value *results, uint32_t result_capacity) {
    if (frame_count_ == 0)
        return RunStatus::Error;
    Frame frame = frames_[--frame_count_];
    const FuncType *type = function_type(module_->data(), frame.function_index);
    if (!type || value_count_ < type->result_count)
        return RunStatus::Error;
    uint32_t source = value_count_ - type->result_count;
    if (source < frame.locals_base)
        return RunStatus::Error;
    for (uint32_t index = 0; index < type->result_count; ++index)
        values_[frame.locals_base + index] = values_[source + index];
    value_count_ = frame.locals_base + type->result_count;
    label_count_ = frame.labels_base;
    if (frame_count_ != 0)
        return RunStatus::Completed;
    if (result_capacity < type->result_count)
        return RunStatus::Error;
    for (uint32_t index = 0; index < type->result_count; ++index)
        results[index] = values_[frame.locals_base + index];
    running_ = false;
    return exit_requested_ ? RunStatus::Exited : RunStatus::Completed;
}

RunStatus Runtime::finish_function(Value *results, uint32_t result_capacity) {
    return return_from_function(results, result_capacity);
}

RunStatus Runtime::step(Value *, uint32_t result_capacity) {
    while (running_) {
        if (preempt_pending_) {
            preempt_pending_ = false;
            return RunStatus::Yield;
        }
        if (frame_count_ == 0)
            return finish_function(pending_results_, result_capacity);
        Frame &frame = frames_[frame_count_ - 1];
        const FunctionBody *body = function_body(frame.function_index);
        if (!body || frame.pc >= body->instruction_count) {
            RunStatus status = finish_function(pending_results_, result_capacity);
            if (status != RunStatus::Completed || frame_count_ == 0)
                return status;
            continue;
        }
        const Instruction &instruction = body->instructions[frame.pc++];
        Value left{};
        Value right{};
        switch (instruction.op) {
        case Op::Unreachable:
            raise(Status::TrapUnreachable);
            running_ = false;
            return RunStatus::Trapped;
        case Op::Nop:
            break;
        case Op::Block:
        case Op::Loop:
        case Op::If: {
            if (instruction.op == Op::If) {
                if (!pop_value(&left))
                    return RunStatus::Error;
                if ((left.bits & 0xffffffffu) == 0) {
                    Label label{static_cast<uint32_t>(instruction.op == Op::Block ? 0x02 : (instruction.op == Op::Loop ? 0x03 : 0x04)),
                                frame.pc, instruction.control.else_pc, instruction.control.end_pc,
                                value_count_, static_cast<uint32_t>(instruction.block_type == 0 ? 0 : 1),
                                instruction.block_type};
                    if (label_count_ >= config_.max_labels)
                        return RunStatus::Error;
                    labels_[label_count_++] = label;
                    frame.pc = instruction.control.else_pc == kNoPc ? instruction.control.end_pc : instruction.control.else_pc + 1;
                    break;
                }
            }
            Label label{static_cast<uint32_t>(instruction.op == Op::Block ? 0x02 : (instruction.op == Op::Loop ? 0x03 : 0x04)),
                        frame.pc, instruction.control.else_pc, instruction.control.end_pc, value_count_,
                        static_cast<uint32_t>(instruction.block_type == 0 ? 0 : 1), instruction.block_type};
            if (label_count_ >= config_.max_labels)
                return RunStatus::Error;
            labels_[label_count_++] = label;
            break;
        }
        case Op::Else:
            if (label_count_ <= frame.labels_base)
                return RunStatus::Error;
            frame.pc = labels_[label_count_ - 1].end_pc;
            break;
        case Op::End:
            if (label_count_ > frame.labels_base) {
                --label_count_;
            } else {
                RunStatus status = finish_function(pending_results_, result_capacity);
                if (status != RunStatus::Completed || frame_count_ == 0)
                    return status;
            }
            break;
        case Op::Br:
            if (!branch_to(instruction.operand))
                return RunStatus::Error;
            break;
        case Op::BrIf:
            if (!pop_value(&left) || (left.bits & 0xffffffffu) == 0)
                break;
            if (!branch_to(instruction.operand))
                return RunStatus::Error;
            break;
        case Op::BrTable: {
            const ModuleData &data = module_->data();
            const uint32_t base = body->branch_base;
            if (!pop_value(&left) || instruction.operand2 == 0 || !data.branch_targets ||
                base > data.branch_target_count ||
                instruction.operand > data.branch_target_count - base ||
                instruction.operand2 > data.branch_target_count - base - instruction.operand)
                return RunStatus::Error;
            const uint32_t *targets = data.branch_targets + base + instruction.operand;
            uint32_t selector = static_cast<uint32_t>(left.bits);
            uint32_t target = selector < instruction.operand2 - 1
                                  ? targets[selector]
                                  : targets[instruction.operand2 - 1];
            if (!branch_to(target))
                return RunStatus::Error;
            break;
        }
        case Op::Return: {
            RunStatus status = finish_function(pending_results_, result_capacity);
            if (status != RunStatus::Completed || frame_count_ == 0)
                return status;
            break;
        }
        case Op::Drop:
            if (!pop_value(&left))
                return RunStatus::Error;
            break;
        case Op::Select:
            if (!pop_value(&left))
                return RunStatus::Error;
            {
                Value false_value{};
                Value true_value{};
                if (!pop_value(&false_value) || !pop_value(&true_value) ||
                    !push_value((left.bits & 0xffffffffu) != 0 ? true_value : false_value))
                    return RunStatus::Error;
            }
            break;
        case Op::I32Const:
            if (!push_value(Value{static_cast<uint64_t>(static_cast<uint32_t>(instruction.immediate))}))
                return RunStatus::Error;
            break;
        case Op::F32Const:
            if (!push_value(Value{static_cast<uint32_t>(instruction.immediate)}))
                return RunStatus::Error;
            break;
        case Op::F64Const:
            if (!push_value(Value{instruction.immediate}))
                return RunStatus::Error;
            break;
        case Op::I64Const:
        case Op::I64ExtendI32U:
            if (instruction.op == Op::I64Const) {
                if (!push_value(Value{instruction.immediate}))
                    return RunStatus::Error;
            } else {
                if (!pop_value(&left) || !push_value(Value{static_cast<uint32_t>(left.bits)}))
                    return RunStatus::Error;
            }
            break;
        case Op::I64ExtendI32S:
            if (!pop_value(&left) ||
                !push_value(Value{static_cast<uint64_t>(
                    static_cast<int64_t>(static_cast<int32_t>(left.bits)))}))
                return RunStatus::Error;
            break;
        case Op::I32WrapI64:
            if (!pop_value(&left) || !push_value(Value{static_cast<uint32_t>(left.bits)}))
                return RunStatus::Error;
            break;
        case Op::I64Add:
            if (!pop_value(&right) || !pop_value(&left) ||
                !push_value(Value{left.bits + right.bits}))
                return RunStatus::Error;
            break;
        case Op::I64Sub:
        case Op::I64Mul:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            if (!push_value(Value{instruction.op == Op::I64Sub
                                      ? left.bits - right.bits
                                      : left.bits * right.bits}))
                return RunStatus::Error;
            break;
        case Op::I32DivS:
        case Op::I32DivU:
        case Op::I32RemS:
        case Op::I32RemU: {
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            uint32_t divisor = static_cast<uint32_t>(right.bits);
            if (divisor == 0) {
                raise(Status::TrapDivideByZero);
                running_ = false;
                return RunStatus::Trapped;
            }
            uint32_t dividend = static_cast<uint32_t>(left.bits);
            uint32_t result = 0;
            if (instruction.op == Op::I32DivU) {
                result = dividend / divisor;
            } else if (instruction.op == Op::I32RemU) {
                result = dividend % divisor;
            } else {
                int32_t signed_dividend = static_cast<int32_t>(dividend);
                int32_t signed_divisor = static_cast<int32_t>(divisor);
                if (signed_dividend == INT32_MIN && signed_divisor == -1) {
                    if (instruction.op == Op::I32DivS) {
                        raise(Status::TrapIntegerOverflow);
                        running_ = false;
                        return RunStatus::Trapped;
                    }
                    result = 0;
                } else if (instruction.op == Op::I32DivS) {
                    result = static_cast<uint32_t>(signed_dividend / signed_divisor);
                } else {
                    result = static_cast<uint32_t>(signed_dividend % signed_divisor);
                }
            }
            if (!push_value(Value{result}))
                return RunStatus::Error;
            break;
        }
        case Op::I64DivS:
        case Op::I64DivU:
        case Op::I64RemS:
        case Op::I64RemU: {
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            uint64_t divisor = right.bits;
            if (divisor == 0) {
                raise(Status::TrapDivideByZero);
                running_ = false;
                return RunStatus::Trapped;
            }
            uint64_t result = 0;
            if (instruction.op == Op::I64DivU) {
                result = left.bits / divisor;
            } else if (instruction.op == Op::I64RemU) {
                result = left.bits % divisor;
            } else {
                int64_t signed_dividend = static_cast<int64_t>(left.bits);
                int64_t signed_divisor = static_cast<int64_t>(divisor);
                if (signed_dividend == INT64_MIN && signed_divisor == -1) {
                    if (instruction.op == Op::I64DivS) {
                        raise(Status::TrapIntegerOverflow);
                        running_ = false;
                        return RunStatus::Trapped;
                    }
                    result = 0;
                } else if (instruction.op == Op::I64DivS) {
                    result = static_cast<uint64_t>(signed_dividend / signed_divisor);
                } else {
                    result = static_cast<uint64_t>(signed_dividend % signed_divisor);
                }
            }
            if (!push_value(Value{result}))
                return RunStatus::Error;
            break;
        }
        case Op::I32Eqz:
            if (!pop_value(&left) || !push_value(Value{static_cast<uint32_t>(left.bits) == 0}))
                return RunStatus::Error;
            break;
        case Op::I32Cmp:
            if (!pop_value(&right) || !pop_value(&left) ||
                !push_value(Value{compare_i32(static_cast<uint8_t>(instruction.immediate),
                                              static_cast<uint32_t>(left.bits),
                                              static_cast<uint32_t>(right.bits))}))
                return RunStatus::Error;
            break;
        case Op::I64Eqz:
            if (!pop_value(&left) || !push_value(Value{left.bits == 0}))
                return RunStatus::Error;
            break;
        case Op::I64Cmp:
            if (!pop_value(&right) || !pop_value(&left) ||
                !push_value(Value{compare_i64(static_cast<uint8_t>(instruction.immediate),
                                              left.bits, right.bits)}))
                return RunStatus::Error;
            break;
        case Op::F32Cmp:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            else {
                float a = f32_from_bits(static_cast<uint32_t>(left.bits));
                float b = f32_from_bits(static_cast<uint32_t>(right.bits));
                bool result = false;
                switch (instruction.immediate) {
                case 0x5b: result = a == b; break;
                case 0x5c: result = a != b; break;
                case 0x5d: result = a < b; break;
                case 0x5e: result = a > b; break;
                case 0x5f: result = a <= b; break;
                case 0x60: result = a >= b; break;
                default: return RunStatus::Error;
                }
                if (!push_value(Value{result}))
                    return RunStatus::Error;
            }
            break;
        case Op::F64Cmp:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            else {
                double a = f64_from_bits(left.bits);
                double b = f64_from_bits(right.bits);
                bool result = false;
                switch (instruction.immediate) {
                case 0x61: result = a == b; break;
                case 0x62: result = a != b; break;
                case 0x63: result = a < b; break;
                case 0x64: result = a > b; break;
                case 0x65: result = a <= b; break;
                case 0x66: result = a >= b; break;
                default: return RunStatus::Error;
                }
                if (!push_value(Value{result}))
                    return RunStatus::Error;
            }
            break;
        case Op::F32Unary: {
            if (!pop_value(&left))
                return RunStatus::Error;
            uint32_t bits = static_cast<uint32_t>(left.bits);
            float value = f32_from_bits(bits);
            float result = 0.0f;
            switch (instruction.immediate) {
            case 0x8b: result = f32_from_bits(bits & 0x7fffffffu); break;
            case 0x8c: result = f32_from_bits(bits ^ 0x80000000u); break;
            case 0x8d: result = __builtin_ceilf(value); break;
            case 0x8e: result = __builtin_floorf(value); break;
            case 0x8f: result = __builtin_truncf(value); break;
            case 0x90: result = f32_nearest(value); break;
            case 0x91: result = __builtin_sqrtf(value); break;
            default: return RunStatus::Error;
            }
            if (!push_value(Value{f32_to_bits(result)}))
                return RunStatus::Error;
            break;
        }
        case Op::F32Binary: {
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            float a = f32_from_bits(static_cast<uint32_t>(left.bits));
            float b = f32_from_bits(static_cast<uint32_t>(right.bits));
            float result = 0.0f;
            switch (instruction.immediate) {
            case 0x92: result = a + b; break;
            case 0x93: result = a - b; break;
            case 0x94: result = a * b; break;
            case 0x95: result = a / b; break;
            case 0x96: result = f32_min(a, b); break;
            case 0x97: result = f32_max(a, b); break;
            case 0x98: {
                uint32_t sign = f32_to_bits(b) & 0x80000000u;
                result = f32_from_bits((f32_to_bits(a) & 0x7fffffffu) | sign);
                break;
            }
            default: return RunStatus::Error;
            }
            if (!push_value(Value{f32_to_bits(result)}))
                return RunStatus::Error;
            break;
        }
        case Op::F64Unary: {
            if (!pop_value(&left))
                return RunStatus::Error;
            uint64_t bits = left.bits;
            double value = f64_from_bits(bits);
            double result = 0.0;
            switch (instruction.immediate) {
            case 0x99: result = f64_from_bits(bits & 0x7fffffffffffffffull); break;
            case 0x9a: result = f64_from_bits(bits ^ 0x8000000000000000ull); break;
            case 0x9b: result = __builtin_ceil(value); break;
            case 0x9c: result = __builtin_floor(value); break;
            case 0x9d: result = __builtin_trunc(value); break;
            case 0x9e: result = f64_nearest(value); break;
            case 0x9f: result = __builtin_sqrt(value); break;
            default: return RunStatus::Error;
            }
            if (!push_value(Value{f64_to_bits(result)}))
                return RunStatus::Error;
            break;
        }
        case Op::F64Binary: {
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            double a = f64_from_bits(left.bits);
            double b = f64_from_bits(right.bits);
            double result = 0.0;
            switch (instruction.immediate) {
            case 0xa0: result = a + b; break;
            case 0xa1: result = a - b; break;
            case 0xa2: result = a * b; break;
            case 0xa3: result = a / b; break;
            case 0xa4: result = f64_min(a, b); break;
            case 0xa5: result = f64_max(a, b); break;
            case 0xa6: {
                uint64_t sign = f64_to_bits(b) & 0x8000000000000000ull;
                result = f64_from_bits((f64_to_bits(a) & 0x7fffffffffffffffull) | sign);
                break;
            }
            default: return RunStatus::Error;
            }
            if (!push_value(Value{f64_to_bits(result)}))
                return RunStatus::Error;
            break;
        }
        case Op::FloatConvert: {
            if (!pop_value(&left))
                return RunStatus::Error;
            uint8_t opcode = static_cast<uint8_t>(instruction.immediate);
            uint64_t result = 0;
            bool valid = true;
            switch (opcode) {
            case 0xa8:
            case 0xa9:
            case 0xaa:
            case 0xab: {
                uint32_t converted = 0;
                if (opcode == 0xa8 || opcode == 0xa9) {
                    uint32_t bits = static_cast<uint32_t>(left.bits);
                    valid = !f32_is_nan(bits);
                    double value = f32_from_bits(bits);
                    valid = valid && (opcode == 0xa8 ? trunc_i32_s(value, &converted)
                                                    : trunc_i32_u(value, &converted));
                } else {
                    valid = !f64_is_nan(left.bits);
                    double value = f64_from_bits(left.bits);
                    valid = valid && (opcode == 0xaa ? trunc_i32_s(value, &converted)
                                                    : trunc_i32_u(value, &converted));
                }
                result = converted;
                break;
            }
            case 0xae:
            case 0xaf:
            case 0xb0:
            case 0xb1: {
                uint64_t converted = 0;
                if (opcode == 0xae || opcode == 0xaf) {
                    uint32_t bits = static_cast<uint32_t>(left.bits);
                    valid = !f32_is_nan(bits);
                    double value = f32_from_bits(bits);
                    valid = valid && (opcode == 0xae ? trunc_i64_s(value, &converted)
                                                    : trunc_i64_u(value, &converted));
                } else {
                    valid = !f64_is_nan(left.bits);
                    double value = f64_from_bits(left.bits);
                    valid = valid && (opcode == 0xb0 ? trunc_i64_s(value, &converted)
                                                    : trunc_i64_u(value, &converted));
                }
                result = converted;
                break;
            }
            case 0xb2:
                result = f32_to_bits(static_cast<float>(static_cast<int32_t>(left.bits)));
                break;
            case 0xb3:
                result = f32_to_bits(static_cast<float>(static_cast<uint32_t>(left.bits)));
                break;
            case 0xb4:
                result = f32_to_bits(static_cast<float>(static_cast<int64_t>(left.bits)));
                break;
            case 0xb5:
                result = f32_to_bits(static_cast<float>(left.bits));
                break;
            case 0xb7:
                result = f64_to_bits(static_cast<double>(static_cast<int32_t>(left.bits)));
                break;
            case 0xb8:
                result = f64_to_bits(static_cast<double>(static_cast<uint32_t>(left.bits)));
                break;
            case 0xb9:
                result = f64_to_bits(static_cast<double>(static_cast<int64_t>(left.bits)));
                break;
            case 0xba:
                result = f64_to_bits(static_cast<double>(left.bits));
                break;
            default:
                return RunStatus::Error;
            }
            if (!valid) {
                raise(Status::TrapInvalidConversion);
                running_ = false;
                return RunStatus::Trapped;
            }
            if (!push_value(Value{result}))
                return RunStatus::Error;
            break;
        }
        case Op::F32DemoteF64:
            if (!pop_value(&left) ||
                !push_value(Value{f32_to_bits(static_cast<float>(f64_from_bits(left.bits)))}))
                return RunStatus::Error;
            break;
        case Op::F64PromoteF32:
            if (!pop_value(&left) ||
                !push_value(Value{f64_to_bits(static_cast<double>(f32_from_bits(
                    static_cast<uint32_t>(left.bits))))}))
                return RunStatus::Error;
            break;
        case Op::Reinterpret:
            if (!pop_value(&left))
                return RunStatus::Error;
            if (instruction.immediate == 0xbc || instruction.immediate == 0xbe)
                left.bits &= 0xffffffffu;
            if (!push_value(left))
                return RunStatus::Error;
            break;
        case Op::SignExtend:
            if (!pop_value(&left))
                return RunStatus::Error;
            switch (instruction.immediate) {
            case 0xc0:
                left.bits = static_cast<uint32_t>(static_cast<int32_t>(static_cast<int8_t>(left.bits)));
                break;
            case 0xc1:
                left.bits = static_cast<uint32_t>(static_cast<int32_t>(static_cast<int16_t>(left.bits)));
                break;
            case 0xc2:
                left.bits = static_cast<uint64_t>(static_cast<int64_t>(static_cast<int8_t>(left.bits)));
                break;
            case 0xc3:
                left.bits = static_cast<uint64_t>(static_cast<int64_t>(static_cast<int16_t>(left.bits)));
                break;
            case 0xc4:
                left.bits = static_cast<uint64_t>(static_cast<int64_t>(static_cast<int32_t>(left.bits)));
                break;
            default:
                return RunStatus::Error;
            }
            if (!push_value(left))
                return RunStatus::Error;
            break;
        case Op::I32Clz:
        case Op::I32Ctz:
        case Op::I32Popcnt:
            if (!pop_value(&left))
                return RunStatus::Error;
            if (!push_value(Value{instruction.op == Op::I32Clz
                                      ? count_leading_zeroes32(static_cast<uint32_t>(left.bits))
                                      : (instruction.op == Op::I32Ctz
                                             ? count_trailing_zeroes32(static_cast<uint32_t>(left.bits))
                                             : population_count32(static_cast<uint32_t>(left.bits)))}))
                return RunStatus::Error;
            break;
        case Op::I64Clz:
        case Op::I64Ctz:
        case Op::I64Popcnt:
            if (!pop_value(&left))
                return RunStatus::Error;
            if (!push_value(Value{instruction.op == Op::I64Clz
                                      ? count_leading_zeroes64(left.bits)
                                      : (instruction.op == Op::I64Ctz
                                             ? count_trailing_zeroes64(left.bits)
                                             : population_count64(left.bits))}))
                return RunStatus::Error;
            break;
        case Op::I32Rotl:
        case Op::I32Rotr:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            if (!push_value(Value{instruction.op == Op::I32Rotl
                                      ? rotate_left32(static_cast<uint32_t>(left.bits),
                                                      static_cast<uint32_t>(right.bits))
                                      : rotate_right32(static_cast<uint32_t>(left.bits),
                                                       static_cast<uint32_t>(right.bits))}))
                return RunStatus::Error;
            break;
        case Op::I64Rotl:
        case Op::I64Rotr:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            if (!push_value(Value{instruction.op == Op::I64Rotl
                                      ? rotate_left64(left.bits, static_cast<uint32_t>(right.bits))
                                      : rotate_right64(left.bits, static_cast<uint32_t>(right.bits))}))
                return RunStatus::Error;
            break;
        case Op::I32And:
        case Op::I32Or:
        case Op::I32Xor:
        case Op::I32Shl:
        case Op::I32ShrS:
        case Op::I32ShrU:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            {
                uint32_t a = static_cast<uint32_t>(left.bits);
                uint32_t b = static_cast<uint32_t>(right.bits);
                uint32_t result = 0;
                if (instruction.op == Op::I32And)
                    result = a & b;
                else if (instruction.op == Op::I32Or)
                    result = a | b;
                else if (instruction.op == Op::I32Xor)
                    result = a ^ b;
                else if (instruction.op == Op::I32Shl)
                    result = a << (b & 31);
                else if (instruction.op == Op::I32ShrS)
                    result = shift_right_i32(a, b, true);
                else
                    result = shift_right_i32(a, b, false);
                if (!push_value(Value{result}))
                    return RunStatus::Error;
            }
            break;
        case Op::I64And:
        case Op::I64Or:
        case Op::I64Xor:
        case Op::I64Shl:
        case Op::I64ShrS:
        case Op::I64ShrU:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            {
                uint64_t a = left.bits;
                uint64_t b = right.bits;
                uint64_t result = 0;
                if (instruction.op == Op::I64And)
                    result = a & b;
                else if (instruction.op == Op::I64Or)
                    result = a | b;
                else if (instruction.op == Op::I64Xor)
                    result = a ^ b;
                else if (instruction.op == Op::I64Shl)
                    result = a << (static_cast<uint32_t>(b) & 63);
                else if (instruction.op == Op::I64ShrS)
                    result = shift_right_i64(a, static_cast<uint32_t>(b), true);
                else
                    result = shift_right_i64(a, static_cast<uint32_t>(b), false);
                if (!push_value(Value{result}))
                    return RunStatus::Error;
            }
            break;
        case Op::I32Add:
        case Op::I32Sub:
        case Op::I32Mul:
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            {
                uint32_t a = static_cast<uint32_t>(left.bits);
                uint32_t b = static_cast<uint32_t>(right.bits);
                uint32_t result = instruction.op == Op::I32Add ? a + b : (instruction.op == Op::I32Sub ? a - b : a * b);
                if (!push_value(Value{static_cast<uint64_t>(result)}))
                    return RunStatus::Error;
            }
            break;
        case Op::LocalGet:
            if (instruction.operand >= body->local_count + function_type(module_->data(), frame.function_index)->param_count ||
                !push_value(values_[frame.locals_base + instruction.operand]))
                return RunStatus::Error;
            break;
        case Op::LocalSet:
            if (!pop_value(&left))
                return RunStatus::Error;
            values_[frame.locals_base + instruction.operand] = left;
            break;
        case Op::LocalTee:
            if (value_count_ == 0)
                return RunStatus::Error;
            values_[frame.locals_base + instruction.operand] = values_[value_count_ - 1];
            break;
        case Op::GlobalGet:
            if (instruction.operand >= global_count_ || !globals_)
                return RunStatus::Error;
            if (!push_value(globals_[instruction.operand]))
                return RunStatus::Error;
            break;
        case Op::GlobalSet:
            if (instruction.operand >= global_count_ || !globals_ ||
                !module_->data().globals[instruction.operand].mutable_value ||
                !pop_value(&left))
                return RunStatus::Error;
            if (module_->data().globals[instruction.operand].value_type ==
                    static_cast<uint8_t>(ValueType::I32) ||
                module_->data().globals[instruction.operand].value_type ==
                    static_cast<uint8_t>(ValueType::F32))
                left.bits &= 0xffffffffu;
            globals_[instruction.operand] = left;
            break;
        case Op::Call: {
            if (instruction.operand < module_->data().imported_function_count) {
                RunStatus status = execute_import(instruction.operand);
                if (status != RunStatus::Completed)
                    return status;
            } else if (enter_frame(instruction.operand) != Status::Ok) {
                return RunStatus::Error;
            }
            break;
        }
        case Op::CallIndirect: {
            if (!pop_value(&left))
                return RunStatus::Error;
            if (instruction.operand2 != 0 || !table_) {
                raise(Status::TrapIndirectCall);
                running_ = false;
                return RunStatus::Trapped;
            }
            uint32_t table_index = static_cast<uint32_t>(left.bits);
            if (table_index >= table_size_ || table_[table_index] == UINT32_MAX) {
                raise(Status::TrapIndirectCall);
                running_ = false;
                return RunStatus::Trapped;
            }
            uint32_t target = table_[table_index];
            const FuncType *expected = &module_->data().types[instruction.operand];
            const FuncType *actual = function_type(module_->data(), target);
            if (!actual || !same_function_type(*expected, *actual)) {
                raise(Status::TrapIndirectCall);
                running_ = false;
                return RunStatus::Trapped;
            }
            if (target < module_->data().imported_function_count) {
                RunStatus status = execute_import(target);
                if (status != RunStatus::Completed)
                    return status;
            } else if (enter_frame(target) != Status::Ok) {
                return RunStatus::Error;
            }
            break;
        }
        case Op::MemorySize:
            if (!push_value(Value{static_cast<uint64_t>(memory_size_ / kPageSize)}))
                return RunStatus::Error;
            break;
        case Op::MemoryGrow:
            if (!pop_value(&left))
                return RunStatus::Error;
             {
                 uint32_t old_pages = 0;
                 uint32_t requested = static_cast<uint32_t>(left.bits);
                 if (!grow_memory(requested, &old_pages))
                     old_pages = UINT32_MAX;
                 if (!push_value(Value{static_cast<uint64_t>(old_pages)}))
                     return RunStatus::Error;
             }
            break;
        case Op::I32Load:
        case Op::I64Load:
        case Op::F32Load:
        case Op::F64Load: {
            if (!pop_value(&left))
                return RunStatus::Error;
            uint64_t address = static_cast<uint32_t>(left.bits);
            uint64_t size = instruction.memory_width;
            if (size == 0 || add_overflow_u64(address, instruction.operand2, &address) ||
                !check_memory(address, size)) {
                raise(Status::TrapOutOfBounds);
                running_ = false;
                return RunStatus::Trapped;
            }
            uint64_t value = 0;
            for (uint64_t byte_index = 0; byte_index < size; ++byte_index)
                value |= static_cast<uint64_t>(memory_[address + byte_index]) << (byte_index * 8);
            if (instruction.memory_signed && size < 8) {
                uint64_t sign_bit = static_cast<uint64_t>(1) << (size * 8 - 1);
                if (value & sign_bit)
                    value |= UINT64_MAX << (size * 8);
            }
            if (instruction.op == Op::I32Load || instruction.op == Op::F32Load)
                value &= 0xffffffffu;
            if (!push_value(Value{value}))
                return RunStatus::Error;
            break;
        }
        case Op::I32Store:
        case Op::I64Store:
        case Op::F32Store:
        case Op::F64Store: {
            if (!pop_value(&right) || !pop_value(&left))
                return RunStatus::Error;
            uint64_t address = static_cast<uint32_t>(left.bits);
            uint64_t size = instruction.memory_width;
            if (size == 0 || add_overflow_u64(address, instruction.operand2, &address) ||
                !check_memory(address, size)) {
                raise(Status::TrapOutOfBounds);
                running_ = false;
                return RunStatus::Trapped;
            }
            for (uint64_t byte_index = 0; byte_index < size; ++byte_index)
                memory_[address + byte_index] = static_cast<uint8_t>(right.bits >> (byte_index * 8));
            break;
        }
        case Op::TruncSat: {
            if (!pop_value(&left))
                return RunStatus::Error;
            uint32_t subopcode = instruction.operand;
            double value = (subopcode & 2) != 0
                              ? f64_from_bits(left.bits)
                              : static_cast<double>(f32_from_bits(static_cast<uint32_t>(left.bits)));
            uint64_t result = 0;
            if ((subopcode & 4) != 0)
                result = (subopcode & 1) != 0 ? saturate_i64_u(value) : saturate_i64_s(value);
            else
                result = (subopcode & 1) != 0 ? saturate_i32_u(value) : saturate_i32_s(value);
            if (!push_value(Value{result}))
                return RunStatus::Error;
            break;
        }
        case Op::MemoryCopy: {
            Value length_value{};
            Value source_value{};
            Value destination_value{};
            if (!pop_value(&length_value) || !pop_value(&source_value) ||
                !pop_value(&destination_value))
                return RunStatus::Error;
            uint64_t destination = static_cast<uint32_t>(destination_value.bits);
            uint64_t source = static_cast<uint32_t>(source_value.bits);
            uint64_t length = static_cast<uint32_t>(length_value.bits);
            uint64_t destination_end = destination;
            uint64_t source_end = source;
            if (add_overflow_u64(destination, length, &destination_end) ||
                add_overflow_u64(source, length, &source_end) ||
                !check_memory(destination, length) || !check_memory(source, length)) {
                raise(Status::TrapOutOfBounds);
                running_ = false;
                return RunStatus::Trapped;
            }
            __builtin_memmove(memory_ + destination, memory_ + source,
                              static_cast<size_t>(length));
            break;
        }
        case Op::MemoryFill: {
            Value length_value{};
            Value fill_value{};
            Value destination_value{};
            if (!pop_value(&length_value) || !pop_value(&fill_value) ||
                !pop_value(&destination_value))
                return RunStatus::Error;
            uint64_t destination = static_cast<uint32_t>(destination_value.bits);
            uint64_t length = static_cast<uint32_t>(length_value.bits);
            uint64_t destination_end = destination;
            if (add_overflow_u64(destination, length, &destination_end) ||
                !check_memory(destination, length)) {
                raise(Status::TrapOutOfBounds);
                running_ = false;
                return RunStatus::Trapped;
            }
            __builtin_memset(memory_ + destination, static_cast<int>(fill_value.bits & 0xff),
                             static_cast<size_t>(length));
            break;
        }
        }
    }
    return RunStatus::Completed;
}

}
