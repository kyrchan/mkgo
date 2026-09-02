#include "engine.h"
#include "wasi_glue.h"
#include "plat.h"

extern "C" {
#include "wasm3.h"
}

static constexpr uint64_t STACK_SLOTS = 8192;

/* abi/ABI.md v2.0: every service module carries a custom section
 * "abi_ver" whose first payload byte is the ABI version. Walk top-level
 * sections of the raw blob and verify it before handing to wasm3. */
static int check_abiver(const uint8_t *b, uint64_t len) {
    uint64_t i = 8; /* skip magic+version */
    while (i + 1 <= len) {
        uint8_t id = b[i++];
        uint64_t sz = 0;
        int sh = 0;
        /* F21: LEB hardening -- cap shift width and byte count so hostile
         * blobs cannot drive sh past 64 (UB) or loop wild. */
        while (i < len && sh < 64) {
            uint8_t byte = b[i++];
            sz |= (uint64_t)(byte & 0x7F) << sh;
            sh += 7;
            if (!(byte & 0x80))
                break;
        }
        if (sh >= 64)
            return -1; /* unterminated / oversized LEB */
        uint64_t end;
        if (__builtin_add_overflow(i, sz, &end) || end > len)
            return -1;
        if (id == 0) { /* custom */
            uint64_t nl = 0;
            if (i < len && !(b[i] & 0x80))
                nl = b[i];
            else if (i + 1 < len) /* F21: bounds-check continuation byte */
                nl = ((b[i] & 0x7F) | ((uint64_t)b[i + 1] << 7)) & 0xFFFFFFFF;
            if (nl == 7 && i + 1 + nl <= len &&
                b[i + 1] == 'a' && b[i + 2] == 'b' && b[i + 3] == 'i' &&
                b[i + 4] == '_' && b[i + 5] == 'v' && b[i + 6] == 'e' &&
                b[i + 7] == 'r') {
                const uint8_t *pl = b + i + 1 + nl;
                return pl < b + len ? (int)pl[0] : -1;
            }
        }
        i += sz;
    }
    return -1; /* missing section */
}

int engine_init(struct engine *e, const uint8_t *blob, uint64_t len) {
    e->env = e->rt = e->mod = 0;
    int av = check_abiver(blob, len);
    if (av != 2) {
        console_puts("[engine] refusing module: abi_ver ");
        console_hex64((uint64_t)(uint32_t)(av < 0 ? 0xFFFF : av));
        console_puts(" != 2\n");
        return 1;
    }
    IM3Environment env = m3_NewEnvironment();
    if (!env)
        return -1;
    e->env = env;
    console_puts("[eng] env ok\n");

    console_puts("[eng] parsing...\n");
    M3Result r = m3_ParseModule(env, (IM3Module *)&e->mod, blob,
                                (uint32_t)len);
    console_puts("[eng] parsed\n");
    if (r) {
        console_puts("[engine] parse: ");
        console_puts(r);
        console_puts("\n");
        return 1;
    }

    IM3Runtime rt = m3_NewRuntime(env, STACK_SLOTS * sizeof(uint64_t), 0);
    if (!rt)
        return -1;
    e->rt = rt;
    console_puts("[eng] runtime ok\n");

    console_puts("[eng] loading...\n");
    r = m3_LoadModule(rt, (IM3Module)e->mod);
    console_puts("[eng] loaded\n");
    if (r) {
        console_puts("[engine] load: ");
        console_puts(r);
        console_puts("\n");
        e->mod = 0; /* runtime owns/failed; do not double-free */
        return 1;
    }
    /* Eagerly compile the module's exports up-front so the first guest
     * syscall doesn't pay metacode-generation latency. Trade-off: longer
     * load time (now shown as [eng] compile), steady-state perf benefit.
     * Keep WASI link after compile so the import stubs are present. */
    console_puts("[eng] compile...\n");
    r = m3_CompileModule((IM3Module)e->mod);
    if (r) {
        console_puts("[engine] compile: ");
        console_puts(r);
        console_puts("\n");
        return 1;
    }
    console_puts("[eng] compiled\n");
    r = wasi_link_module((IM3Module)e->mod);
    if (r) {
        console_puts("[engine] link: ");
        console_puts(r);
        console_puts("\n");
        return 1;
    }
    return 0;
}

int engine_start(struct engine *e) {
    IM3Runtime rt = (IM3Runtime)e->rt;
    IM3Function f = 0;
    M3Result r = m3_FindFunction(&f, rt, "_start");
    if (r == m3Err_none && f)
        r = m3_CallV(f);

    if (r == m3Err_trapExit)
        return 0; /* proc_exit is a clean session end */
    if (r != m3Err_none) {
        console_puts("[engine] error: ");
        console_puts(r ? r : "(null)");
        console_puts("\n");
        return 2;
    }
    return 0;
}

const char *engine_errstr(struct engine *e) {
    if (!e || !e->rt)
        return "no runtime";
    M3ErrorInfo info;
    for (unsigned i = 0; i < sizeof(info); i++)
        ((uint8_t *)&info)[i] = 0;
    m3_GetErrorInfo((IM3Runtime)e->rt, &info);
    return info.result ? info.result : "unknown";
}

void engine_shutdown(struct engine *e) {
    if (e->rt)
        m3_FreeRuntime((IM3Runtime)e->rt);
    if (e->mod && !e->rt)
        m3_FreeModule((IM3Module)e->mod);
    if (e->env)
        m3_FreeEnvironment((IM3Environment)e->env);
    e->env = e->rt = e->mod = 0;
}
