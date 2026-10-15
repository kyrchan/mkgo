#include "core/wasm/wasm.h"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <vector>

namespace {

void *host_alloc(uint64_t size, void *) {
    return std::malloc(static_cast<size_t>(size));
}

void host_free(void *pointer, void *) {
    std::free(pointer);
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

void append_sleb64(std::vector<uint8_t> &output, uint64_t value) {
    bool more = true;
    while (more) {
        uint8_t byte = static_cast<uint8_t>(value & 0x7f);
        value >>= 7;
        if ((value == 0 && (byte & 0x40) == 0) ||
            (value == UINT64_MAX && (byte & 0x40) != 0))
            more = false;
        else
            byte |= 0x80;
        output.push_back(byte);
    }
}

void append_name(std::vector<uint8_t> &output, const char *text) {
    uint32_t length = static_cast<uint32_t>(std::strlen(text));
    append_uleb(output, length);
    output.insert(output.end(), text, text + length);
}

void append_section(std::vector<uint8_t> &output, uint8_t id, const std::vector<uint8_t> &payload) {
    output.push_back(id);
    append_uleb(output, static_cast<uint32_t>(payload.size()));
    output.insert(output.end(), payload.begin(), payload.end());
}

std::vector<uint8_t> make_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 2);
    types.push_back(0x60);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_section(output, 1, types);

    std::vector<uint8_t> imports;
    append_uleb(imports, 1);
    append_name(imports, "test");
    append_name(imports, "add");
    imports.push_back(0);
    append_uleb(imports, 0);
    append_section(output, 2, imports);

    std::vector<uint8_t> functions;
    append_uleb(functions, 2);
    append_uleb(functions, 1);
    append_uleb(functions, 1);
    append_section(output, 3, functions);

    std::vector<uint8_t> table;
    append_uleb(table, 1);
    table.push_back(0x70);
    table.push_back(0);
    append_uleb(table, 2);
    append_section(output, 4, table);

    std::vector<uint8_t> memory;
    append_uleb(memory, 1);
    memory.push_back(0);
    append_uleb(memory, 1);
    append_section(output, 5, memory);

    std::vector<uint8_t> globals;
    append_uleb(globals, 1);
    globals.push_back(0x7f);
    globals.push_back(0);
    globals.push_back(0x41);
    globals.push_back(0x07);
    globals.push_back(0x0b);
    append_section(output, 6, globals);

    std::vector<uint8_t> exports;
    append_uleb(exports, 2);
    append_name(exports, "run");
    exports.push_back(0);
    append_uleb(exports, 1);
    append_name(exports, "mem");
    exports.push_back(0);
    append_uleb(exports, 2);
    append_section(output, 7, exports);

    std::vector<uint8_t> elements;
    append_uleb(elements, 1);
    append_uleb(elements, 0);
    elements.push_back(0x41);
    elements.push_back(0x00);
    elements.push_back(0x0b);
    append_uleb(elements, 1);
    append_uleb(elements, 1);
    append_section(output, 9, elements);

    std::vector<uint8_t> code;
    append_uleb(code, 2);
    std::vector<uint8_t> run_body{0x00, 0x41, 0x29, 0x10, 0x00, 0x0b};
    append_uleb(code, static_cast<uint32_t>(run_body.size()));
    code.insert(code.end(), run_body.begin(), run_body.end());
    std::vector<uint8_t> memory_body{0x01, 0x01, 0x7f,
                                     0x41, 0x10, 0x21, 0x00,
                                     0x20, 0x00, 0x41, 0x2a, 0x36, 0x02, 0x00,
                                     0x20, 0x00, 0x28, 0x02, 0x00, 0x0b};
    append_uleb(code, static_cast<uint32_t>(memory_body.size()));
    code.insert(code.end(), memory_body.begin(), memory_body.end());
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_feature_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_section(output, 1, types);

    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 0);
    append_section(output, 3, functions);

    std::vector<uint8_t> memory;
    append_uleb(memory, 1);
    memory.push_back(0);
    append_uleb(memory, 1);
    append_section(output, 5, memory);

    std::vector<uint8_t> globals;
    append_uleb(globals, 1);
    globals.push_back(0x7e);
    globals.push_back(1);
    globals.push_back(0x42);
    globals.push_back(0x0a);
    globals.push_back(0x0b);
    append_section(output, 6, globals);

    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "run");
    exports.push_back(0);
    append_uleb(exports, 0);
    append_section(output, 7, exports);

    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{
        0x00,
        0x02, 0x7f,
        0x41, 0x07,
        0x41, 0x00,
        0x0e, 0x00, 0x00,
        0x41, 0x07,
        0x0b,
        0x1a,
        0x41, 0x07,
        0xad,
        0x1a,
        0x41, 0x00,
        0x42, 0x3c,
        0x37, 0x03, 0x04,
        0x41, 0x00,
        0x29, 0x03, 0x04,
        0x23, 0x00,
        0x42, 0x05,
        0x7c,
        0x7c,
        0xa7,
        0x42, 0x05,
        0x42, 0x05,
        0x51,
        0x1a,
        0x42, 0x00,
        0x50,
        0x1a,
        0x41, 0x03,
        0x41, 0x05,
        0x4d,
        0x1a,
        0x41, 0x00,
        0x42, 0x7f,
        0x3c, 0x00, 0x08,
        0x41, 0x00,
        0x31, 0x00, 0x08,
        0x42, 0xff, 0x01,
        0x51,
        0x1a,
        0x41, 0x00,
        0x30, 0x00, 0x08,
        0x42, 0x00,
        0x52,
        0x1a,
        0x42, 0x0f,
        0x42, 0x03,
        0x83,
        0x42, 0x03,
        0x51,
        0x1a,
        0x42, 0x10,
        0x42, 0x02,
        0x88,
        0x42, 0x04,
        0x51,
        0x1a,
        0x41, 0x03,
        0x41, 0x02,
        0x74,
        0x41, 0x0c,
        0x46,
        0x1a,
        0x42, 0x10,
        0x42, 0x03,
        0x7e,
        0x42, 0x0c,
        0x51,
        0x1a,
        0x42, 0x14,
        0x42, 0x03,
        0x7f,
        0x42, 0x06,
        0x51,
        0x1a,
        0x42, 0x14,
        0x42, 0x03,
        0x81,
        0x42, 0x02,
        0x51,
        0x1a,
        0x41, 0x14,
        0x41, 0x03,
        0x6d,
        0x41, 0x06,
        0x46,
        0x1a,
        0x41, 0x0a,
        0x41, 0x14,
        0x41, 0x01,
        0x1b,
        0x41, 0x0a,
        0x46,
        0x1a,
        0x41, 0x7f,
        0xac,
        0x42, 0x7f,
        0x51,
        0x1a,
        0x42, 0x01,
        0x79,
        0x42, 0x3f,
        0x51,
        0x1a,
        0x42, 0x08,
        0x7a,
        0x42, 0x03,
        0x51,
        0x1a,
        0x42, 0x07,
        0x7b,
        0x42, 0x03,
        0x51,
        0x1a,
        0x42, 0x01,
        0x42, 0x01,
        0x89,
        0x42, 0x02,
        0x51,
        0x1a,
        0x42, 0x02,
        0x42, 0x01,
        0x8a,
        0x42, 0x01,
        0x51,
        0x1a,
        0x41, 0x10,
        0x41, 0xda, 0x00,
        0x41, 0x04,
        0xfc, 0x0b, 0x00,
        0x41, 0x14,
        0x41, 0x10,
        0x41, 0x04,
        0xfc, 0x0a, 0x00, 0x00,
        0x41, 0x18,
        0x43, 0x00, 0x00, 0x80, 0x3f,
        0x38, 0x02, 0x00,
        0x41, 0x18,
        0x2a, 0x02, 0x00,
        0x1a,
        0x41, 0x20,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
        0x39, 0x03, 0x00,
        0x41, 0x20,
        0x2b, 0x03, 0x00,
        0x1a,
        0x43, 0x00, 0x00, 0x80, 0x3f,
        0x43, 0x00, 0x00, 0x80, 0x3f,
        0x5b,
        0x1a,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
        0x63,
        0x1a,
        0x43, 0x00, 0x00, 0x80, 0x3f,
        0xbb,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
        0x61,
        0x1a,
        0x42, 0x01,
        0xb9,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
        0x61,
        0x1a,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
        0xa0,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x40,
        0x61,
        0x1a,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x40,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x40,
        0xa3,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
        0x61,
        0x1a,
        0x43, 0x00, 0x00, 0x80, 0x3f,
        0x43, 0x00, 0x00, 0x00, 0x40,
        0x92,
        0x43, 0x00, 0x00, 0x40, 0x40,
        0x5b,
        0x1a,
        0x41, 0x7f,
        0xc0,
        0x41, 0x7f,
        0x46,
        0x1a,
        0x41, 0x7f,
        0xc1,
        0x41, 0x7f,
        0x46,
        0x1a,
        0x42, 0x7f,
        0xc2,
        0x42, 0x7f,
        0x51,
        0x1a,
        0x42, 0x7f,
        0xc3,
        0x42, 0x7f,
        0x51,
        0x1a,
        0x42, 0x7f,
        0xc4,
        0x42, 0x7f,
        0x51,
        0x1a,
        0x41, 0x80, 0x81, 0x82, 0x84, 0x78,
        0x41, 0x80, 0x81, 0x82, 0x84, 0x78,
        0x46,
        0x1a,
        0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf8, 0x3f,
        0xfc, 0x06,
        0x42, 0x01,
        0x51,
        0x1a,
        0xad,
        0x24, 0x00,
        0x23, 0x00,
        0xa7,
        0x0b,
    };
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_indirect_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_uleb(types, 1);
    types.push_back(0x7f);
    append_section(output, 1, types);

    std::vector<uint8_t> functions;
    append_uleb(functions, 3);
    append_uleb(functions, 0);
    append_uleb(functions, 0);
    append_uleb(functions, 0);
    append_section(output, 3, functions);

    std::vector<uint8_t> table;
    append_uleb(table, 1);
    table.push_back(0x70);
    table.push_back(0);
    append_uleb(table, 2);
    append_section(output, 4, table);

    std::vector<uint8_t> exports;
    append_uleb(exports, 2);
    append_name(exports, "run");
    exports.push_back(0);
    append_uleb(exports, 1);
    append_name(exports, "bad");
    exports.push_back(0);
    append_uleb(exports, 2);
    append_section(output, 7, exports);

    std::vector<uint8_t> elements;
    append_uleb(elements, 1);
    append_uleb(elements, 0);
    elements.push_back(0x41);
    elements.push_back(0x00);
    elements.push_back(0x0b);
    append_uleb(elements, 1);
    append_uleb(elements, 0);
    append_section(output, 9, elements);

    std::vector<uint8_t> code;
    append_uleb(code, 3);
    std::vector<uint8_t> target_body{0x00, 0x41, 0x07, 0x0b};
    append_uleb(code, static_cast<uint32_t>(target_body.size()));
    code.insert(code.end(), target_body.begin(), target_body.end());
    std::vector<uint8_t> run_body{0x00, 0x41, 0x29, 0x41, 0x00, 0x11, 0x00, 0x00, 0x0b};
    append_uleb(code, static_cast<uint32_t>(run_body.size()));
    code.insert(code.end(), run_body.begin(), run_body.end());
    std::vector<uint8_t> bad_body{0x00, 0x20, 0x00, 0x41, 0x01, 0x11, 0x00, 0x00, 0x0b};
    append_uleb(code, static_cast<uint32_t>(bad_body.size()));
    code.insert(code.end(), bad_body.begin(), bad_body.end());
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_i64_bitwise_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 1);
    types.push_back(0x7e);
    append_section(output, 1, types);

    std::vector<uint8_t> functions;
    append_uleb(functions, 3);
    for (uint32_t index = 0; index < 3; ++index)
        append_uleb(functions, 0);
    append_section(output, 3, functions);

    std::vector<uint8_t> exports;
    append_uleb(exports, 3);
    append_name(exports, "run_and");
    exports.push_back(0);
    append_uleb(exports, 0);
    append_name(exports, "run_or");
    exports.push_back(0);
    append_uleb(exports, 1);
    append_name(exports, "run_xor");
    exports.push_back(0);
    append_uleb(exports, 2);
    append_section(output, 7, exports);

    const uint64_t left = 0x8080808080808080ull;
    const uint64_t right = 0x123456789abcdef0ull;
    const uint8_t operations[] = {0x83, 0x84, 0x85};
    std::vector<uint8_t> code;
    append_uleb(code, 3);
    for (uint8_t operation : operations) {
        std::vector<uint8_t> body{0x00, 0x42};
        append_sleb64(body, left);
        body.push_back(0x42);
        append_sleb64(body, right);
        body.push_back(operation);
        body.push_back(0x0b);
        append_uleb(code, static_cast<uint32_t>(body.size()));
        code.insert(code.end(), body.begin(), body.end());
    }
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_deep_control_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 0);
    append_section(output, 1, types);
    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 0);
    append_section(output, 3, functions);
    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{0x00};
    for (uint32_t index = 0; index < 1025; ++index) {
        body.push_back(0x02);
        body.push_back(0x40);
    }
    for (uint32_t index = 0; index < 1025; ++index)
        body.push_back(0x0b);
    body.push_back(0x0b);
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_validation_module(const std::vector<uint8_t> &body,
                                            uint8_t result_type = 0x40,
                                            bool has_memory = false,
                                            bool has_global = false,
                                            bool mutable_global = false,
                                            uint8_t global_type = 0x7f) {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, result_type == 0x40 ? 0 : 1);
    if (result_type != 0x40)
        types.push_back(result_type);
    append_section(output, 1, types);

    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 0);
    append_section(output, 3, functions);

    if (has_memory) {
        std::vector<uint8_t> memory;
        append_uleb(memory, 1);
        memory.push_back(0);
        append_uleb(memory, 1);
        append_section(output, 5, memory);
    }

    if (has_global) {
        std::vector<uint8_t> globals;
        append_uleb(globals, 1);
        globals.push_back(global_type);
        globals.push_back(mutable_global ? 1 : 0);
        if (global_type == 0x7e) {
            globals.push_back(0x42);
            globals.push_back(0x00);
        } else if (global_type == 0x7d) {
            globals.push_back(0x43);
            globals.push_back(0x00);
            globals.push_back(0x00);
            globals.push_back(0x00);
            globals.push_back(0x00);
        } else if (global_type == 0x7c) {
            globals.push_back(0x44);
            for (uint32_t index = 0; index < 8; ++index)
                globals.push_back(0x00);
        } else {
            globals.push_back(0x41);
            globals.push_back(0x00);
        }
        globals.push_back(0x0b);
        append_section(output, 6, globals);
    }

    std::vector<uint8_t> code;
    std::vector<uint8_t> body_bytes{0x00};
    body_bytes.insert(body_bytes.end(), body.begin(), body.end());
    append_uleb(code, 1);
    append_uleb(code, static_cast<uint32_t>(body_bytes.size()));
    code.insert(code.end(), body_bytes.begin(), body_bytes.end());
    append_section(output, 10, code);
    return output;
}

std::vector<uint8_t> make_data_count_module() {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> types;
    append_uleb(types, 1);
    types.push_back(0x60);
    append_uleb(types, 0);
    append_uleb(types, 0);
    append_section(output, 1, types);
    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 0);
    append_section(output, 3, functions);
    std::vector<uint8_t> memory;
    append_uleb(memory, 1);
    memory.push_back(0);
    append_uleb(memory, 1);
    append_section(output, 5, memory);
    std::vector<uint8_t> data_count;
    append_uleb(data_count, 1);
    append_section(output, 12, data_count);
    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{0x00, 0x0b};
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    std::vector<uint8_t> data;
    append_uleb(data, 1);
    append_uleb(data, 0);
    data.push_back(0x41);
    data.push_back(0x00);
    data.push_back(0x0b);
    append_uleb(data, 1);
    data.push_back(0x2a);
    append_section(output, 11, data);
    return output;
}

wasm::RunStatus add_one(void *, wasm::Runtime &, const wasm::Value *args,
                       wasm::Value *results, uint32_t result_count,
                       uint32_t *exit_code) {
    if (result_count != 1)
        return wasm::RunStatus::Error;
    results[0].bits = (args[0].bits + 1) & 0xffffffffu;
    *exit_code = 0;
    return wasm::RunStatus::Completed;
}

wasm::RunStatus preempt_add(void *context, wasm::Runtime &runtime, const wasm::Value *args,
                            wasm::Value *results, uint32_t result_count,
                            uint32_t *exit_code) {
    wasm::RunStatus status = add_one(context, runtime, args, results, result_count, exit_code);
    if (status == wasm::RunStatus::Completed)
        runtime.request_preempt();
    return status;
}

bool expect(bool condition, const char *message) {
    if (condition)
        return true;
    std::fprintf(stderr, "wasm interpreter test failed: %s\n", message);
    return false;
}

}

int main() {
    std::vector<uint8_t> bytes = make_module();
    wasm::Module module;
    if (!expect(module.decode(bytes.data(), bytes.size(), host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "module decode"))
        return 1;

    wasm::Runtime runtime;
    wasm::RuntimeConfig config{};
    config.max_values = 128;
    config.max_frames = 16;
    config.max_labels = 32;
    config.max_memory_bytes = 1024 * 1024;
    if (!expect(runtime.instantiate(module, host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "runtime instantiate"))
        return 1;
    if (!expect(runtime.bind_import("test", "add", add_one, nullptr) == wasm::Status::Ok,
                "import binding"))
        return 1;

    const wasm::Slice run_name{reinterpret_cast<const uint8_t *>("run"), 3};
    wasm::Value results[2]{};
    wasm::RunStatus run_status = runtime.invoke(run_name, nullptr, 0, results, 2);
    if (!expect(run_status == wasm::RunStatus::Completed, "import invocation")) {
        std::fprintf(stderr, "run status=%u error=%u\n", static_cast<unsigned>(run_status),
                     static_cast<unsigned>(runtime.error()));
        return 1;
    }
    if (!expect(results[0].bits == 42, "import result"))
        return 1;

    if (!expect(runtime.bind_import("test", "add", preempt_add, nullptr) == wasm::Status::Ok,
                "preempt import rebind"))
        return 1;
    wasm::RunStatus preempt_status = runtime.invoke(run_name, nullptr, 0, results, 2);
    if (!expect(preempt_status == wasm::RunStatus::Yield, "preempt yield"))
        return 1;
    if (!expect(runtime.resume(results, 2) == wasm::RunStatus::Completed, "preempt resume"))
        return 1;
    if (!expect(results[0].bits == 42, "preempt result"))
        return 1;

    if (!expect(runtime.bind_import("test", "add", add_one, nullptr) == wasm::Status::Ok,
                "import rebind"))
        return 1;
    const wasm::Slice memory_name{reinterpret_cast<const uint8_t *>("mem"), 3};
    if (!expect(runtime.invoke(memory_name, nullptr, 0, results, 2) == wasm::RunStatus::Completed,
                "memory invocation"))
        return 1;
    if (!expect(results[0].bits == 42, "memory result"))
        return 1;

    std::vector<uint8_t> feature_bytes = make_feature_module();
    wasm::Module feature_module;
    wasm::Status feature_status = feature_module.decode(feature_bytes.data(), feature_bytes.size(), host_alloc, host_free, nullptr);
    if (!expect(feature_status == wasm::Status::Ok,
                "feature module decode"))
        return 1;
    wasm::Runtime feature_runtime;
    if (!expect(feature_runtime.instantiate(feature_module, host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "feature runtime instantiate"))
        return 1;
    const wasm::Slice feature_name{reinterpret_cast<const uint8_t *>("run"), 3};
    wasm::RunStatus feature_run_status = feature_runtime.invoke(feature_name, nullptr, 0, results, 2);
    if (!expect(feature_run_status == wasm::RunStatus::Completed,
                "feature invocation"))
        return 1;
    if (!expect(results[0].bits == 75, "global i64 comparison and branch table result"))
        return 1;
    if (!expect(feature_runtime.memory_size() == 65536, "feature memory size"))
        return 1;
    if (!expect(feature_runtime.memory() != nullptr &&
                    feature_runtime.memory()[4] == 60 &&
                    feature_runtime.memory()[5] == 0 &&
                    feature_runtime.memory()[8] == 0xff &&
                    feature_runtime.memory()[16] == 0x5a &&
                    feature_runtime.memory()[20] == 0x5a &&
                    feature_runtime.memory()[27] == 0x3f &&
                    feature_runtime.memory()[39] == 0x3f,
                "feature memory operations"))
        return 1;

    std::vector<uint8_t> bitwise_bytes = make_i64_bitwise_module();
    wasm::Module bitwise_module;
    if (!expect(bitwise_module.decode(bitwise_bytes.data(), bitwise_bytes.size(),
                                     host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "i64 bitwise module decode"))
        return 1;
    wasm::Runtime bitwise_runtime;
    if (!expect(bitwise_runtime.instantiate(bitwise_module, host_alloc, host_free,
                                           nullptr, config) == wasm::Status::Ok,
                "i64 bitwise runtime instantiate"))
        return 1;
    const uint64_t bitwise_left = 0x8080808080808080ull;
    const uint64_t bitwise_right = 0x123456789abcdef0ull;
    const wasm::Slice bitwise_and_name{reinterpret_cast<const uint8_t *>("run_and"), 7};
    const wasm::Slice bitwise_or_name{reinterpret_cast<const uint8_t *>("run_or"), 6};
    const wasm::Slice bitwise_xor_name{reinterpret_cast<const uint8_t *>("run_xor"), 7};
    if (!expect(bitwise_runtime.invoke(bitwise_and_name, nullptr, 0, results, 2) == wasm::RunStatus::Completed &&
                    results[0].bits == (bitwise_left & bitwise_right),
                "i64 bitwise and high bits") ||
        !expect(bitwise_runtime.invoke(bitwise_or_name, nullptr, 0, results, 2) == wasm::RunStatus::Completed &&
                    results[0].bits == (bitwise_left | bitwise_right),
                "i64 bitwise or high bits") ||
        !expect(bitwise_runtime.invoke(bitwise_xor_name, nullptr, 0, results, 2) == wasm::RunStatus::Completed &&
                    results[0].bits == (bitwise_left ^ bitwise_right),
                "i64 bitwise xor high bits"))
        return 1;

    std::vector<uint8_t> indirect_bytes = make_indirect_module();
    wasm::Module indirect_module;
    if (!expect(indirect_module.decode(indirect_bytes.data(), indirect_bytes.size(), host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "indirect module decode"))
        return 1;
    wasm::Runtime indirect_runtime;
    if (!expect(indirect_runtime.instantiate(indirect_module, host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "indirect runtime instantiate"))
        return 1;
    wasm::Value indirect_arg{41};
    if (!expect(indirect_runtime.invoke(run_name, &indirect_arg, 1, results, 2) == wasm::RunStatus::Completed,
                "indirect invocation"))
        return 1;
    if (!expect(results[0].bits == 7, "indirect result"))
        return 1;
    const wasm::Slice bad_name{reinterpret_cast<const uint8_t *>("bad"), 3};
    if (!expect(indirect_runtime.invoke(bad_name, &indirect_arg, 1, results, 2) == wasm::RunStatus::Trapped,
                "indirect null trap"))
        return 1;
    if (!expect(indirect_runtime.error() == wasm::Status::TrapIndirectCall,
                "indirect null trap status"))
        return 1;

    std::vector<uint8_t> deep_bytes = make_deep_control_module();
    wasm::Module deep_module;
    if (!expect(deep_module.decode(deep_bytes.data(), deep_bytes.size(), host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "deep control decode"))
        return 1;

    std::vector<uint8_t> data_count_bytes = make_data_count_module();
    wasm::Module data_count_module;
    if (!expect(data_count_module.decode(data_count_bytes.data(), data_count_bytes.size(), host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "data count ordering decode"))
        return 1;

    std::vector<uint8_t> unreachable_bytes = make_validation_module({0x00, 0x45, 0x0b}, 0x7f);
    wasm::Module unreachable_module;
    if (!expect(unreachable_module.decode(unreachable_bytes.data(), unreachable_bytes.size(),
                                          host_alloc, host_free, nullptr) == wasm::Status::Ok,
                "unreachable polymorphic validation"))
        return 1;

    auto reject_module = [](const std::vector<uint8_t> &invalid_bytes, const char *message) {
        wasm::Module invalid_module;
        wasm::Status status = invalid_module.decode(invalid_bytes.data(), invalid_bytes.size(),
                                                     host_alloc, host_free, nullptr);
        return expect(status == wasm::Status::TypeMismatch, message);
    };

    if (!reject_module(make_validation_module({0x42, 0x00, 0x0b}, 0x7f),
                       "wrong result type rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x42, 0x01, 0x42, 0x02, 0x6a, 0x0b}),
                       "wrong operand type rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x41, 0x00, 0x04, 0x7f,
                                               0x41, 0x01, 0x0b, 0x0b}, 0x7f),
                       "result-producing if without else rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x42, 0x00, 0x0e, 0x00, 0x00, 0x0b}),
                       "invalid br_table selector rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x02, 0x7f, 0x41, 0x00,
                                               0x0e, 0x01, 0x00, 0x01,
                                               0x0b, 0x1a, 0x0b}),
                       "invalid br_table arity rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x41, 0x01, 0x24, 0x00, 0x0b},
                                              0x40, false, true, false),
                       "immutable global set rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x42, 0x01, 0x24, 0x00, 0x0b},
                                              0x40, false, true, true),
                       "wrong global set type rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x42, 0x00, 0x28, 0x02, 0x00,
                                               0x1a, 0x0b}, 0x40, true),
                       "bad memory operand type rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x41, 0x00, 0x28, 0x03, 0x00,
                                               0x1a, 0x0b}, 0x40, true),
                       "bad memory alignment rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x41, 0x00, 0x28, 0x02, 0x00,
                                               0x1a, 0x0b}),
                       "memory operation without memory rejection"))
        return 1;
    if (!reject_module(make_validation_module({0x00, 0x42, 0x00, 0x45, 0x0b}, 0x7f),
                       "unreachable concrete type mismatch rejection"))
        return 1;

    for (size_t size = 0; size < 8; ++size) {
        wasm::Module malformed;
        if (!expect(malformed.decode(bytes.data(), size, host_alloc, host_free, nullptr) != wasm::Status::Ok,
                    "short rejection"))
            return 1;
    }
    wasm::Module malformed;
    if (!expect(malformed.decode(bytes.data(), bytes.size() - 1, host_alloc, host_free, nullptr) != wasm::Status::Ok,
                "truncated rejection"))
        return 1;

    std::puts("wasm interpreter test: PASS");
    return 0;
}
