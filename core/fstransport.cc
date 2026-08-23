#include "fstransport.h"
#include "sched.h"
#include "plat.h"

extern "C" {
#include "wasm3.h"
}

static const char *FS_NAME = "fs";
static uint16_t fs_seq = 100;

int fs_call(const uint8_t *req, uint32_t req_len, uint8_t *resp,
            uint32_t resp_cap) {
    int sid = sched_session_by_name(FS_NAME);
    if (sid < 0)
        return -1; /* fs not running */
    void *rt = sched_runtime_of((uint32_t)sid);
    if (!rt)
        return -1;

    IM3Function fbuf = 0, fresp = 0, freq = 0;
    if (m3_FindFunction(&fbuf, (IM3Runtime)rt, "_fsbuf") != m3Err_none ||
        m3_FindFunction(&fresp, (IM3Runtime)rt, "_fsrespbuf") != m3Err_none ||
        m3_FindFunction(&freq, (IM3Runtime)rt, "_fsreq") != m3Err_none)
        return -1;

    uint32_t boff = 0, roff = 0;
    int64_t v;
    if (m3_CallV(fbuf) != m3Err_none)
        return -1;
    if (m3_GetResultsV(fbuf, &v) != m3Err_none)
        return -1;
    boff = (uint32_t)v;
    if (m3_CallV(fresp) != m3Err_none)
        return -1;
    if (m3_GetResultsV(fresp, &v) != m3Err_none)
        return -1;
    roff = (uint32_t)v;

    uint32_t memsz = 0;
    uint8_t *mem = m3_GetMemory((IM3Runtime)rt, &memsz, 0);
    if (!mem || !boff || !roff || (uint64_t)boff + req_len > memsz ||
        (uint64_t)roff + resp_cap > memsz)
        return -1;

    for (uint32_t i = 0; i < req_len; i++)
        mem[boff + i] = req[i];

    int64_t rlen = 0;
    if (m3_CallV(freq, (int32_t)req_len, (int32_t)resp_cap) != m3Err_none)
        return -1;
    if (m3_GetResultsV(freq, &v) != m3Err_none)
        return -1;
    rlen = v;
    if (rlen <= 0 || (uint64_t)rlen > resp_cap)
        return -1;

    for (int64_t i = 0; i < rlen; i++)
        resp[i] = mem[roff + i];
    return (int)rlen;
}

uint16_t fs_next_seq(void) { return ++fs_seq; }
