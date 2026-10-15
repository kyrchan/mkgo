#include "core/wasm/owned_session.h"

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

void append_name(std::vector<uint8_t> &output, const char *text) {
    uint32_t length = static_cast<uint32_t>(std::strlen(text));
    append_uleb(output, length);
    output.insert(output.end(), text, text + length);
}

/* i32.const operands are decoded as s32 by this engine (see the
 * characterization block in main), so test programs must ENCODE them the
 * way real toolchains do: signed LEB, which is why Go never emits the
 * 5-byte unsigned form. Encoding 64 as the single byte 0x40 would be read
 * back as -64. */
void append_sleb32(std::vector<uint8_t> &output, int32_t value) {
    for (;;) {
        uint8_t byte = static_cast<uint8_t>(value & 0x7f);
        value >>= 7; /* arithmetic shift: sign-extends */
        if ((value == 0 && !(byte & 0x40)) || (value == -1 && (byte & 0x40))) {
            output.push_back(byte);
            return;
        }
        output.push_back(static_cast<uint8_t>(byte | 0x80));
    }
}

void append_section(std::vector<uint8_t> &output, uint8_t id,
                    const std::vector<uint8_t> &payload) {
    output.push_back(id);
    append_uleb(output, static_cast<uint32_t>(payload.size()));
    output.insert(output.end(), payload.begin(), payload.end());
}

std::vector<uint8_t> make_module(uint8_t abi_version = 2) {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> custom;
    append_name(custom, "abi_ver");
    custom.push_back(abi_version);
    append_section(output, 0, custom);
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
    append_uleb(imports, 2);
    for (uint32_t index = 0; index < 2; ++index) {
        append_name(imports, "test");
        append_name(imports, "add");
        imports.push_back(0);
        append_uleb(imports, 0);
    }
    append_section(output, 2, imports);
    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 1);
    append_section(output, 3, functions);
    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "_start");
    exports.push_back(0);
    append_uleb(exports, 2);
    append_section(output, 7, exports);
    std::vector<uint8_t> code;
    append_uleb(code, 1);
    std::vector<uint8_t> body{0x00, 0x41, 0x28, 0x10, 0x00, 0x10, 0x01, 0x0b};
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}


/* ---- resume-equivalence programs (Phase 20 preemption gate) ----
 *
 * AGENTS requires that "interrupted execution resumes with identical
 * final state and trap/exit status". The interpreter test only proved a
 * single yield/resume round trip; nothing proved that a session which
 * yields at EVERY commit point ends in the same state, or that an import
 * with a side effect runs exactly once per call across those yields.
 * These programs make both observable:
 *
 *   Loop  : acc=0; repeat N times { i++; acc = add(acc) }; return acc
 *           -> result must be N, and the host import counter must be N,
 *              which can only hold if no call is replayed or dropped.
 *   Trap  : 1 / 0 (integer divide by zero)
 *   Exit  : call an import that returns Exited with code 7
 */
enum class ProgramKind { Loop, Trap, Exit };

std::vector<uint8_t> make_program(ProgramKind kind, uint32_t iterations) {
    std::vector<uint8_t> output{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
    std::vector<uint8_t> custom;
    append_name(custom, "abi_ver");
    custom.push_back(2);
    append_section(output, 0, custom);
    /* type 0: (i32)->i32 (the import), type 1: ()->i32 (the program) */
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
    /* one import: test.add -> func 0 */
    std::vector<uint8_t> imports;
    append_uleb(imports, 1);
    append_name(imports, "test");
    append_name(imports, "add");
    imports.push_back(0);
    append_uleb(imports, 0);
    append_section(output, 2, imports);
    std::vector<uint8_t> functions;
    append_uleb(functions, 1);
    append_uleb(functions, 1);
    append_section(output, 3, functions);
    std::vector<uint8_t> exports;
    append_uleb(exports, 1);
    append_name(exports, "_start");
    exports.push_back(0);
    append_uleb(exports, 1);
    append_section(output, 7, exports);

    std::vector<uint8_t> body;
    if (kind == ProgramKind::Loop) {
        body.push_back(0x02);            /* 2 local groups            */
        body.push_back(0x01);
        body.push_back(0x7f);
        body.push_back(0x01);
        body.push_back(0x7f);
        body.push_back(0x41);
        body.push_back(0x00);
        body.push_back(0x21);
        body.push_back(0x00);            /* acc = 0                   */
        body.push_back(0x41);
        body.push_back(0x00);
        body.push_back(0x21);
        body.push_back(0x01);            /* i = 0                     */
        body.push_back(0x02);
        body.push_back(0x40);            /* block                     */
        body.push_back(0x03);
        body.push_back(0x40);            /*   loop                   */
        body.push_back(0x20);
        body.push_back(0x01);
        body.push_back(0x41);
        body.push_back(0x01);
        body.push_back(0x6a);            /*     i + 1                */
        body.push_back(0x21);
        body.push_back(0x01);
        body.push_back(0x20);
        body.push_back(0x00);
        body.push_back(0x10);
        body.push_back(0x00);            /*     acc = add(acc)       */
        body.push_back(0x21);
        body.push_back(0x00);
        body.push_back(0x20);
        body.push_back(0x01);
        body.push_back(0x41);
        append_sleb32(body, static_cast<int32_t>(iterations));
        body.push_back(0x49);            /*     i < iterations       */
        body.push_back(0x0d);
        body.push_back(0x00);            /*     br_if loop           */
        body.push_back(0x0b);            /*   end loop               */
        body.push_back(0x0b);            /* end block                */
        body.push_back(0x20);
        body.push_back(0x00);            /* return acc               */
    } else if (kind == ProgramKind::Trap) {
        body.push_back(0x00);            /* no local groups           */
        body.push_back(0x41);
        body.push_back(0x01);
        body.push_back(0x41);
        body.push_back(0x00);
        body.push_back(0x6d);            /* i32.div_s -> trap         */
    } else {
        body.push_back(0x00);            /* no local groups           */
        body.push_back(0x41);
        body.push_back(0x00);
        body.push_back(0x10);
        body.push_back(0x00);            /* call add -> Exited       */
    }
    body.push_back(0x0b);
    std::vector<uint8_t> code;
    append_uleb(code, 1);
    append_uleb(code, static_cast<uint32_t>(body.size()));
    code.insert(code.end(), body.begin(), body.end());
    append_section(output, 10, code);
    return output;
}

struct ImportProbe {
    uint32_t calls = 0;
    uint32_t exit_with = 0;   /* non-zero: return Exited with this code */
    bool preempt_each_call = false;
    uint32_t yields = 0;
};

wasm::RunStatus probe_add(void *context, wasm::Runtime &runtime,
                          const wasm::Value *args, wasm::Value *results,
                          uint32_t result_count, uint32_t *exit_code) {
    ImportProbe *probe = static_cast<ImportProbe *>(context);
    probe->calls++;
    if (result_count != 1)
        return wasm::RunStatus::Error;
    if (probe->exit_with) {
        *exit_code = probe->exit_with;
        return wasm::RunStatus::Exited;
    }
    results[0].bits = (args[0].bits + 1) & 0xffffffffu;
    *exit_code = 0;
    if (probe->preempt_each_call) {
        /* Ask to be resumed at the very next commit point. */
        runtime.request_preempt();
        probe->yields++;
    }
    return wasm::RunStatus::Completed;
}

struct Outcome {
    wasm::RunStatus status = wasm::RunStatus::Error;
    uint32_t result = 0;
    uint32_t calls = 0;
    uint32_t requests = 0;  /* preempt requests the probe issued  */
    uint32_t yields = 0;    /* times the engine actually returned Yield */
    uint32_t exit_code = 0;
    wasm::Status init_status = wasm::Status::Internal;
};

/* Drive a session to a terminal status.
 *
 * `preempt_every_opportunity` makes the import request a yield after each
 * call, so the session is interrupted again at the next commit point --
 * once per iteration.
 *
 * Do NOT also request a preempt before every resume(): Runtime::step()
 * checks the flag before executing anything, so a request made while a
 * yield is pending livelocks the session (it yields forever without
 * making progress). A scheduler must let a continuation run until the
 * next commit point, not re-arm the yield it just served. */

Outcome drive(const std::vector<uint8_t> &bytes, bool preempt_every_opportunity,
              uint32_t exit_with) {
    Outcome outcome;
    ImportProbe probe;
    probe.exit_with = exit_with;
    probe.preempt_each_call = preempt_every_opportunity;
    wasm::OwnedSession session;
    wasm::RuntimeConfig config{};
    config.max_values = 256;
    config.max_frames = 16;
    config.max_labels = 32;
    outcome.init_status = session.init(bytes.data(), bytes.size(), host_alloc,
                                       host_free, nullptr, config);
    if (outcome.init_status != wasm::Status::Ok)
        return outcome;
    if (session.bind_import("test", "add", probe_add, &probe) != wasm::Status::Ok)
        return outcome;
    wasm::Value results[2]{};
    wasm::RunStatus status = session.start(results, 2);
    uint32_t guard = 0;
    while (status == wasm::RunStatus::Yield) {
        if (++guard > 100000u)
            return outcome; /* no forward progress: livelock */
        outcome.yields++;
        status = session.resume(results, 2);
    }
    outcome.status = status;
    outcome.result = results[0].bits;
    outcome.calls = probe.calls;
    outcome.requests = probe.yields;
    outcome.exit_code = session.runtime().exit_code();
    return outcome;
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

bool expect(bool condition, const char *message) {
    if (condition)
        return true;
    std::fprintf(stderr, "owned session test failed: %s\n", message);
    return false;
}

}

int main() {
    std::vector<uint8_t> bytes = make_module();
    wasm::OwnedSession session;
    wasm::RuntimeConfig config{};
    config.max_values = 64;
    config.max_frames = 8;
    config.max_labels = 16;
    if (!expect(session.init(bytes.data(), bytes.size(), host_alloc, host_free, nullptr, config) == wasm::Status::Ok,
                "owned session init"))
        return 1;
    if (!expect(!session.runtime().imports_bound(), "unbound imports rejected"))
        return 1;
    if (!expect(session.bind_import("test", "add", add_one, nullptr) == wasm::Status::Ok,
                "duplicate import binding"))
        return 1;
    if (!expect(session.runtime().imports_bound(), "all imports bound"))
        return 1;
    wasm::Value results[1]{};
    if (!expect(session.start(results, 1) == wasm::RunStatus::Completed,
                "owned session start"))
        return 1;
    if (!expect(results[0].bits == 42, "owned session result"))
        return 1;
    wasm::FuncType wrong{};
    wrong.param_count = 1;
    wrong.params[0] = wasm::ValueType::I32;
    wrong.result_count = 1;
    wrong.results[0] = wasm::ValueType::I32;
    if (!expect(session.bind_import_typed("test", "add", &wrong, add_one, nullptr) == wasm::Status::Ok,
                "typed duplicate import binding"))
        return 1;
    wrong.params[0] = wasm::ValueType::I64;
    if (!expect(session.bind_import_typed("test", "add", &wrong, add_one, nullptr) == wasm::Status::TypeMismatch,
                "typed import mismatch"))
        return 1;
    if (!expect(session.bind_import("test", "missing", add_one, nullptr) == wasm::Status::MissingImport,
                "missing import"))
        return 1;

    std::vector<uint8_t> bad_abi = make_module(3);
    wasm::OwnedSession abi_session;
    if (!expect(abi_session.init(bad_abi.data(), bad_abi.size(), host_alloc, host_free, nullptr, config) == wasm::Status::Unsupported,
                "ABI version rejection"))
        return 1;

    std::vector<uint8_t> bad_magic = bytes;
    bad_magic[0] = 0xff;
    wasm::Module malformed;
    if (!expect(malformed.decode(bad_magic.data(), bad_magic.size(), host_alloc, host_free, nullptr) == wasm::Status::Malformed,
                "Wasm header rejection"))
        return 1;

    /* ---- i32.const operand encoding: an AMBIGUOUS byte, pinned ----
     *
     * The spec says i32.const takes a u32, so (i32.const 64) is the
     * single byte 0x40 and a signed reader turns it into -64. The
     * obvious "fix" breaks every Go guest: Go writes i32.const as a
     * *signed* LEB and never emits the 5-byte unsigned form (no shipped
     * artifact contains 41 ff ff ff ff 0f, all of them contain 41 7f and
     * 41 40), so with unsigned decoding fs.wasm traps out-of-bounds --
     * every -1 becomes 127.
     *
     * Both readings are therefore pinned here, with the evidence, so the
     * next person to "fix" this finds out why it was reverted:
     *   - signed 1-byte forms 0x40..0x7F  -> -64..-1   (what Go emits)
     *   - the 2-byte form of 64 (c0 00)   -> 64        (what clang emits)
     *   - the 5-byte form of -1 (ff ff ff ff 0f) -> -1 (spec canonical)
     * A spec-canonical *unsigned* module and a Go module cannot both be
     * satisfied; we follow the ecosystem, because guest ABI stability
     * (AGENTS rule 5) is judged against the shipped artifacts.
     */
    {
        struct ConstCase {
            std::vector<uint8_t> encoding;
            uint32_t expected;
            const char *note;
        };
        const ConstCase cases[] = {
            {{0x40}, 0xffffffc0u, "i32.const -64 (Go's 1-byte signed form of 0x40)"},
            {{0x7f}, 0xffffffffu, "i32.const -1 (Go's 1-byte signed form of 0x7f)"},
            {{0xc0, 0x00}, 64u, "i32.const 64 via the 2-byte form (clang/wat2wasm)"},
            {{0xff, 0xff, 0xff, 0xff, 0x0f}, 0xffffffffu,
             "i32.const -1 via the spec-canonical 5-byte form"},
            {{0x00}, 0u, "i32.const 0"},
            {{0x3f}, 63u, "i32.const 63 (top unambiguous single byte)"},
            {{0xc1, 0x00}, 65u, "i32.const 65 via the 2-byte form"},
            {{0x80, 0x80, 0x80, 0x80, 0x78}, 0x80000000u,
             "i32.const INT32_MIN via the 5-byte signed form"},
        };
        for (const ConstCase &item : cases) {
            std::vector<uint8_t> body{0x00, 0x41};
            body.insert(body.end(), item.encoding.begin(), item.encoding.end());
            body.push_back(0x0b);
            std::vector<uint8_t> bytes{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00};
            std::vector<uint8_t> custom;
            append_name(custom, "abi_ver");
            custom.push_back(2);
            append_section(bytes, 0, custom);
            std::vector<uint8_t> types;
            append_uleb(types, 1);
            types.push_back(0x60);
            append_uleb(types, 0);
            append_uleb(types, 1);
            types.push_back(0x7f);
            append_section(bytes, 1, types);
            std::vector<uint8_t> functions;
            append_uleb(functions, 1);
            append_uleb(functions, 0);
            append_section(bytes, 3, functions);
            std::vector<uint8_t> exports;
            append_uleb(exports, 1);
            append_name(exports, "_start");
            exports.push_back(0);
            append_uleb(exports, 0);
            append_section(bytes, 7, exports);
            std::vector<uint8_t> code;
            append_uleb(code, 1);
            append_uleb(code, static_cast<uint32_t>(body.size()));
            code.insert(code.end(), body.begin(), body.end());
            append_section(bytes, 10, code);

            wasm::OwnedSession session;
            wasm::RuntimeConfig config{};
            config.max_values = 32;
            config.max_frames = 4;
            config.max_labels = 8;
            if (!expect(session.init(bytes.data(), bytes.size(), host_alloc,
                                    host_free, nullptr, config) == wasm::Status::Ok,
                        "i32.const module init"))
                return 1;
            wasm::Value results[1]{};
            if (!expect(session.start(results, 1) == wasm::RunStatus::Completed,
                        "i32.const module completes"))
                return 1;
            if (!expect(results[0].bits == item.expected, item.note))
                return 1;
        }
    }

    /* ---- resume equivalence: identical final state, trap, exit ---- */
    const uint32_t kIterations = 64;
    std::vector<uint8_t> loop_bytes = make_program(ProgramKind::Loop, kIterations);
    Outcome plain = drive(loop_bytes, false, 0);
    if (!expect(plain.init_status == wasm::Status::Ok, "resume-equiv: loop instantiates"))
        return 1;
    if (!expect(plain.status == wasm::RunStatus::Completed, "resume-equiv: loop completes"))
        return 1;
    if (!expect(plain.result == kIterations, "resume-equiv: loop result without preemption"))
        return 1;
    if (!expect(plain.calls == kIterations, "resume-equiv: import ran once per iteration (baseline)"))
        return 1;

    Outcome chopped = drive(loop_bytes, true, 0);
    if (!expect(chopped.status == plain.status,
                "resume-equiv: same terminal status when interrupted at every commit point"))
        return 1;
    if (!expect(chopped.result == plain.result,
                "resume-equiv: same final result when interrupted at every commit point"))
        return 1;
    if (!expect(chopped.calls == plain.calls,
                "resume-equiv: no import call replayed or dropped across yields"))
        return 1;
    /* Non-vacuity: the engine must ACTUALLY return Yield (not just accept
     * the request). Counting probe-side requests would pass even if
     * request_preempt() were a no-op. */
    if (!expect(chopped.requests > 0, "resume-equiv: preemption was requested"))
        return 1;
    if (!expect(chopped.yields > 0,
                "resume-equiv: engine really yielded (request_preempt is live)"))
        return 1;
    if (!expect(chopped.yields >= kIterations,
                "resume-equiv: yielded at least once per import call"))
        return 1;

    /* Trap status must be identical with and without preemption. */
    std::vector<uint8_t> trap_bytes = make_program(ProgramKind::Trap, 0);
    Outcome trap_plain = drive(trap_bytes, false, 0);
    Outcome trap_chopped = drive(trap_bytes, true, 0);
    if (!expect(trap_plain.status == wasm::RunStatus::Trapped, "resume-equiv: trap traps"))
        return 1;
    if (!expect(trap_chopped.status == trap_plain.status,
                "resume-equiv: same trap status when interrupted"))
        return 1;

    /* Exit status must be identical with and without preemption. */
    std::vector<uint8_t> exit_bytes = make_program(ProgramKind::Exit, 0);
    Outcome exit_plain = drive(exit_bytes, false, 7);
    Outcome exit_chopped = drive(exit_bytes, true, 7);
    if (!expect(exit_plain.status == wasm::RunStatus::Exited, "resume-equiv: exit reports Exited"))
        return 1;
    if (!expect(exit_plain.exit_code == 7, "resume-equiv: exit code 7 (baseline)"))
        return 1;
    if (!expect(exit_chopped.status == exit_plain.status,
                "resume-equiv: same Exited status when interrupted"))
        return 1;
    if (!expect(exit_chopped.exit_code == exit_plain.exit_code,
                "resume-equiv: same exit code when interrupted"))
        return 1;
    if (!expect(exit_chopped.calls == 1,
                "resume-equiv: the exiting import is not replayed on resume"))
        return 1;

    std::puts("owned session test: PASS");
    return 0;
}
