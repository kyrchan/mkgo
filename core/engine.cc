#include "engine.h"
#include "wasi_glue.h"
#include "plat.h"

extern "C" {
#include "wasm3.h"
}

static constexpr uint64_t STACK_SLOTS = 8192;

int engine_init(struct engine *e, const uint8_t *blob, uint64_t len) {
    e->env = e->rt = e->mod = 0;
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
