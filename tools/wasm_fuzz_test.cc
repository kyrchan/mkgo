#include "core/wasm/wasm.h"

#include <chrono>
#include <cstdio>
#include <cstdlib>
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

void append_name(std::vector<uint8_t> &output, const char *text) {
    uint32_t length = 0;
    while (text[length] != 0)
        ++length;
    append_uleb(output, length);
    for (uint32_t index = 0; index < length; ++index)
        output.push_back(static_cast<uint8_t>(text[index]));
}

void append_section(std::vector<uint8_t> &output, uint8_t id,
                    const std::vector<uint8_t> &payload) {
    output.push_back(id);
    append_uleb(output, static_cast<uint32_t>(payload.size()));
    output.insert(output.end(), payload.begin(), payload.end());
}

std::vector<uint8_t> make_seed() {
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
    globals.push_back(0x7f);
    globals.push_back(0);
    globals.push_back(0x41);
    globals.push_back(0x00);
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
    std::vector<uint8_t> body{0x00, 0x41, 0x2a, 0x0b};
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}

uint64_t next_random(uint64_t *state) {
    uint64_t value = *state;
    value ^= value << 13;
    value ^= value >> 7;
    value ^= value << 17;
    *state = value;
    return value;
}

}

int main(int argc, char **argv) {
    uint32_t seconds = 30;
    if (argc > 1)
        seconds = static_cast<uint32_t>(std::strtoul(argv[1], nullptr, 10));
    std::vector<uint8_t> seed = make_seed();
    uint64_t state = 0x9e3779b97f4a7c15ull;
    uint64_t iterations = 0;
    uint64_t accepted = 0;
    auto start = std::chrono::steady_clock::now();
    auto deadline = start + std::chrono::seconds(seconds);
    do {
        std::vector<uint8_t> candidate = seed;
        uint32_t mutations = 1 + static_cast<uint32_t>(next_random(&state) % 8);
        for (uint32_t index = 0; index < mutations; ++index) {
            size_t position = static_cast<size_t>(next_random(&state) % candidate.size());
            candidate[position] ^= static_cast<uint8_t>(1u << (next_random(&state) % 8));
        }
        if ((next_random(&state) & 7) == 0)
            candidate.resize(static_cast<size_t>(next_random(&state) % (seed.size() + 1)));
        wasm::Module module;
        wasm::Status status = module.decode(candidate.data(), candidate.size(),
                                           host_alloc, host_free, nullptr);
        if (status == wasm::Status::Ok)
            ++accepted;
        ++iterations;
    } while (std::chrono::steady_clock::now() < deadline);
    std::printf("wasm fuzz: PASS iterations=%llu accepted=%llu\n",
                static_cast<unsigned long long>(iterations),
                static_cast<unsigned long long>(accepted));
    return 0;
}
