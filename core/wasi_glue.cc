/* Mini-WASI preview1 profile (AGENTS.md rule 4, frozen):
 *   fd_write proc_exit clock_time_get random_get args_get args_sizes_get
 *   environ_get environ_sizes_get sched_yield
 * Everything else in the namespace gets a conservative ENOSYS stub so
 * stock toolchain runtimes (Go) still LINK; the behavioral surface stays
 * the frozen set. Decision recorded: stubs never touch HW/FS state.
 *
 * wasm3 raw-call convention: _sp[0..numRets) = return slots, then args;
 * memory pointers arrive as u32 offsets resolved through _mem. */
#include "wasi_glue.h"
#include "lib.h"
#include <stdio.h>
#include "fstransport.h"
#include "fsroute.h"
#include "sched.h"
#include "rt.h"

#define WASI_EBADF2 8
static void put16_(uint8_t *p, uint16_t v) {
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
}
static uint16_t fs_seq_ctr = 200;
#include "plat.h"
#include "rt.h"

extern "C" {
#include "wasm3.h"
#include "m3_env.h"
#include "m3_function.h"
}


/* ---- session context: lives in the owning session (see sched.h) ---- */
#include "sched.h"
static sched_wasi_state *wctx() { return sched_wasi_current(); }

bool wasi_exited(void) { sched_wasi_state *w = wctx(); return w ? w->exited : false; }
int wasi_exit_code(void) { sched_wasi_state *w = wctx(); return w ? w->exit_code : -1; }

/* errno (preview1) */
#define WASI_ESUCCESS 0
#define WASI_EINVAL 28
#define WASI_ENOSYS 52

/* TSC-based ns clock, calibrated once by arch timer (PIT reference). */
static uint64_t g_tsc_khz = 2000000; /* plausible default pre-calibration */
void wasi_calibrate_clock(uint64_t tsc_khz) {
    if (tsc_khz)
        g_tsc_khz = tsc_khz;
}
uint64_t wasi_now_ns(void) {
    return cpu_cycles() / (g_tsc_khz / 1000000ULL + 1);
}

/* ---- PRNG (xorshift64* over cycles entropy) ---- */
static uint64_t rng_next(void) {
    static uint64_t s = 0x9E3779B97F4A7C15ULL;
    uint64_t x = s;
    x ^= x << 13;
    x ^= x >> 7;
    x ^= x << 17;
    s = x ^ cpu_cycles();
    return x * 0x2545F4914F6CDD1DULL;
}

static int mem_ok(IM3Runtime rt, const void *p, uint64_t n) {
    uint32_t sz = 0;
    uint8_t *base = m3_GetMemory(rt, &sz, 0);
    return base && p && (const uint8_t *)p >= base &&
           ((const uint8_t *)p - base) + n <= sz;
}
#define MEMCHECK(ptr, len)                                                     \
    if (!mem_ok(runtime, ptr, (uint64_t)(len)))                                 \
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);

/* ---------------- frozen profile ---------------- */

int routed_rw(int32_t fd, bool write, uint8_t *buf, uint32_t len,
              uint32_t *done);

m3ApiRawFunction(wasi_fd_write) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    m3ApiGetArgMem(uint32_t *, iovs)
    m3ApiGetArg(int32_t, iovs_len)
    m3ApiGetArgMem(uint32_t *, nwritten)
    if (fd >= 3) { /* routed file write: single iovec fast path */
        if (!mem_ok(runtime, iovs, 8))
            m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
        uint32_t off = iovs[0], len = iovs[1];
        uint8_t *data = (uint8_t *)m3ApiOffsetToPtr(off);
        if (!mem_ok(runtime, data, len))
            m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
        uint32_t done = 0;
        int err = routed_rw(fd, true, data, len, &done);
        if (nwritten && mem_ok(runtime, nwritten, 4))
            *nwritten = done;
        m3ApiReturn(err);
    }
    if (fd != 1 && fd != 2) {
        m3ApiReturn(WASI_EINVAL);
    }
    if (!mem_ok(runtime, iovs, (uint64_t)(uint32_t)iovs_len * 8))
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
    uint32_t total = 0;
    for (int32_t i = 0; i < iovs_len; i++) {
        uint32_t off = iovs[(uint32_t)i * 2];
        uint32_t len = iovs[(uint32_t)i * 2 + 1];
        uint8_t *data = (uint8_t *)m3ApiOffsetToPtr(off);
        if (!mem_ok(runtime, data, len))
            m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
        for (uint32_t j = 0; j < len; j++)
            console_putc((char)data[j]);
        total += len;
    }
    if (nwritten) {
        if (!mem_ok(runtime, nwritten, 4))
            m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
        *nwritten = total;
    }
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_fd_read) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    m3ApiGetArgMem(uint32_t *, iovs)
    m3ApiGetArg(int32_t, iovs_len)
    m3ApiGetArgMem(uint32_t *, nread)
    if (fd < 3)
        m3ApiReturn(WASI_EBADF2); /* stdin not implemented in v1 */
    if (!mem_ok(runtime, iovs, (uint64_t)(uint32_t)iovs_len * 8))
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
    uint32_t off = iovs[0], len = iovs[1];
    uint8_t *data = (uint8_t *)m3ApiOffsetToPtr(off);
    if (!mem_ok(runtime, data, len))
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
    uint32_t done = 0;
    int err = routed_rw(fd, false, data, len, &done);
    if (nread && mem_ok(runtime, nread, 4))
        *nread = done;
    m3ApiReturn(err);
}

m3ApiRawFunction(wasi_proc_exit) {
    m3ApiGetArg(int32_t, code)
    sched_wasi_state *w = wctx();
    if (w) {
        w->exit_code = code;
        w->exited = true;
    }
    /* trap out of guest execution; engine treats this marker as clean end */
    return m3Err_trapExit;
}

m3ApiRawFunction(wasi_clock_time_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, clock_id)
    m3ApiGetArg(int64_t, precision);
    (void)precision;
    m3ApiGetArgMem(uint64_t *, time)
    if (clock_id != 0 && clock_id != 1 && clock_id != 2 && clock_id != 3)
        m3ApiReturn(WASI_EINVAL);
    if (time) {
        if (!mem_ok(runtime, time, 8))
            m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
        *time = wasi_now_ns();
    }
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_random_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(uint8_t *, buf)
    m3ApiGetArg(int32_t, len)
    if (!mem_ok(runtime, buf, (uint64_t)(uint32_t)len))
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
    for (int32_t i = 0; i < len; i++)
        buf[i] = (uint8_t)rng_next();
    m3ApiReturn(WASI_ESUCCESS);
}

static void args_sizes(uint32_t *argc, uint32_t *bufsize) {
    sched_wasi_state *w = wctx();
    *argc = 0;
    *bufsize = 0;
    if (!w)
        return;
    while (w->argv[*argc]) {
        const char *s = w->argv[*argc];
        uint64_t n = 0;
        while (s[n])
            n++;
        *bufsize += (uint32_t)(n + 1);
        (*argc)++;
    }
}

m3ApiRawFunction(wasi_args_sizes_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(uint32_t *, rpcnt)
    m3ApiGetArgMem(uint32_t *, rpsz)
    uint32_t argc, bufsz;
    args_sizes(&argc, &bufsz);
    if (rpcnt)
        *rpcnt = argc;
    if (rpsz)
        *rpsz = bufsz;
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_args_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(uint32_t *, argv)
    m3ApiGetArgMem(char *, argv_buf)
    uint32_t argc, bufsz;
    args_sizes(&argc, &bufsz);
    if (!mem_ok(runtime, argv, (uint64_t)argc * 4) ||
        !mem_ok(runtime, argv_buf, bufsz))
        m3ApiTrap(m3Err_trapOutOfBoundsMemoryAccess);
    sched_wasi_state *w = wctx();
    if (!w)
        return m3Err_none;
    uint32_t off = 0;
    for (uint32_t i = 0; i < argc; i++) {
        argv[i] = m3ApiPtrToOffset(argv_buf + off);
        const char *s = w->argv[i];
        uint64_t k = 0;
        for (; s[k]; k++)
            argv_buf[off + k] = s[k];
        argv_buf[off + k] = 0;
        off += (uint32_t)k + 1;
    }
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_environ_sizes_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(uint32_t *, rpcnt)
    m3ApiGetArgMem(uint32_t *, rpsz)
    if (rpcnt)
        *rpcnt = 0;
    if (rpsz)
        *rpsz = 0;
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_environ_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(uint32_t *, envp )
    m3ApiGetArgMem(char *, buf )
    m3ApiReturn(WASI_ESUCCESS);
}

uint8_t *g_last_mem[12]; // DEBUG: _mem seen by most recent raw call per sid

m3ApiRawFunction(wasi_sched_yield) {
    m3ApiReturnType(int32_t)
    {
        uint32_t sid = sched_current_sid();
        if (sid < 12)
            g_last_mem[sid] = (uint8_t *)_mem;
    }
    extern void sched_yield_current(void);
    sched_yield_current(); /* coroutine switch; resumes here next quantum */
    m3ApiReturn(WASI_ESUCCESS);
}

/* ---------------- routed path ops (Phase 5, ABI v1.1) ----------------
 * preview1 path_open/fd_read/fd_write(>=3)/fd_close/path_create_directory
 * forward to the fs.wasm session; fds live in the session's wctx. */

#define ROP_OPEN 1
#define ROP_CLOSE 2
#define ROP_READ 3
#define ROP_WRITE 4
#define ROP_STAT 5
#define ROP_MKDIR 6
#define ROP_DEL 7

static uint8_t fsreq[4096];
static uint8_t fsresp[8192];

/* build framed request: op seq uid pathlen path payload */
/* frame: {u16 op,u16 seq,u32 uid,char rname[16],u16 path_len,path,payload} */
static uint32_t mk_req(uint16_t op, const char *path, uint32_t plen,
                       const uint8_t *payload, uint32_t paylen) {
    put16_(fsreq, op);
    uint16_t sq = ++fs_seq_ctr;
    fsreq[2] = (uint8_t)sq;
    fsreq[3] = (uint8_t)(sq >> 8);
    uint32_t uid = sched_uid_of(sched_current_sid());
    fsreq[4] = (uint8_t)uid;
    fsreq[5] = (uint8_t)(uid >> 8);
    fsreq[6] = (uint8_t)(uid >> 16);
    fsreq[7] = (uint8_t)(uid >> 24);
    const char *rn = sched_name_of(sched_current_sid()); /* sync: unused */
    uint32_t ri = 0;
    for (; rn[ri] && ri < 15; ri++)
        fsreq[8 + ri] = (uint8_t)rn[ri];
    for (; ri < 16; ri++)
        fsreq[8 + ri] = 0;
    fsreq[24] = (uint8_t)plen;
    fsreq[25] = (uint8_t)(plen >> 8);
    uint32_t o = 26;
    for (uint32_t i = 0; i < plen && o < sizeof(fsreq); i++)
        fsreq[o++] = (uint8_t)path[i];
    for (uint32_t i = 0; i < paylen && o < sizeof(fsreq); i++)
        fsreq[o++] = payload[i];
    return o;
}

extern "C" bool ports_enqueue_by_name(const char *, const void *, uint32_t);

static uint16_t fs_rt_seq = 1000;

static int fs_roundtrip(uint32_t reqlen) {
    uint16_t sq = ++fs_rt_seq;
    fsreq[2] = (uint8_t)sq;
    fsreq[3] = (uint8_t)(sq >> 8);
#ifdef HOST_BUILD
    fprintf(stderr, "[rt] step=expect seq=%u\n", sq);
#endif
    if (fsroute_expect(sq, sched_name_of(sched_current_sid())) != 0) {
#ifdef HOST_BUILD
        fprintf(stderr, "[rt] expect FAIL\n");
#endif
        return -1;
    }
#ifdef HOST_BUILD
    fprintf(stderr, "[rt] step=enqueue\n");
#endif
    if (!ports_enqueue_by_name("fs", fsreq, reqlen)) {
#ifdef HOST_BUILD
        fprintf(stderr, "[rt] enqueue FAIL\n");
#endif
        return -1;
    }
#ifdef HOST_BUILD
    fprintf(stderr, "[rt] step=wait\n");
#endif
    int r = fsroute_wait(sq, fsresp, sizeof(fsresp));
#ifdef HOST_BUILD
    fprintf(stderr, "[rt] wait r=%d\n", r);
#endif
    return r;
}

static int alloc_fd(void) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w)
        return -1;
    for (int fd = 3; fd < SCHED_MAX_FDS; fd++)
        if (w->fds[fd] == SCHED_FD_EMPTY) {
            w->fds[fd] = 0x7FFFFFFF; /* reserve */
            return fd;
        }
    return -1;
}

m3ApiRawFunction(wasi_path_open) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, dirfd)
    m3ApiGetArg(int32_t, dirflags)
    m3ApiGetArgMem(char *, path)
    m3ApiGetArg(int32_t, path_len)
    m3ApiGetArg(int32_t, oflags)
    m3ApiGetArg(int64_t, rb)
    m3ApiGetArg(int64_t, ri)
    m3ApiGetArg(int32_t, fdflags)
    m3ApiGetArgMem(uint32_t *, opened_fd)
    (void)dirfd; (void)dirflags; (void)rb; (void)ri; (void)fdflags;

    int fd = alloc_fd();
    if (fd < 0)
        m3ApiReturn(WASI_EBADF2);
    /* payload: u32 fh_hint(0), u32 flags bit0=create */
    uint8_t pay[8] = {0};
    pay[4] = (oflags & 1) ? 1 : 0;
    uint32_t rl = mk_req(ROP_OPEN, path, path_len < 0 ? 0 : (uint32_t)path_len,
                         pay, 8);
    int r = fs_roundtrip(rl);
    if (r < 6) { /* OPEN reply = {u16 errno, u32 fh} */
        sched_wasi_state *w = sched_wasi_current();
        w->fds[fd] = SCHED_FD_EMPTY;
        m3ApiReturn(r < 0 ? WASI_ENOSYS : (int32_t)(fsresp[0] | fsresp[1] << 8));
    }
    int32_t errno_ = (int32_t)(fsresp[0] | (fsresp[1] << 8));
    if (errno_ != 0) {
        sched_wasi_state *w = sched_wasi_current();
        w->fds[fd] = SCHED_FD_EMPTY;
        m3ApiReturn(errno_);
    }
    uint32_t fh = (uint32_t)fsresp[2] | (uint32_t)fsresp[3] << 8 |
                  (uint32_t)fsresp[4] << 16 | (uint32_t)fsresp[5] << 24;
    sched_wasi_state *w = sched_wasi_current();
    w->fds[fd] = fh;
    if (opened_fd)
        *opened_fd = (uint32_t)fd;
    m3ApiReturn(0);
}

m3ApiRawFunction(wasi_path_create_directory) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, dirfd)
    m3ApiGetArgMem(char *, path)
    m3ApiGetArg(int32_t, path_len)
    (void)dirfd;
    uint32_t rl = mk_req(ROP_MKDIR, path,
                         path_len < 0 ? 0 : (uint32_t)path_len, 0, 0);
    int r = fs_roundtrip(rl);
    if (r < 2)
        m3ApiReturn(WASI_ENOSYS);
    m3ApiReturn((int32_t)(fsresp[0] | (fsresp[1] << 8)));
}

m3ApiRawFunction(wasi_fd_close_routed) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < 3 || fd >= SCHED_MAX_FDS || w->fds[fd] == SCHED_FD_EMPTY)
        m3ApiReturn(WASI_EBADF2);
    uint32_t fh = w->fds[fd];
    uint8_t pay[4] = {(uint8_t)fh, (uint8_t)(fh >> 8), (uint8_t)(fh >> 16),
                      (uint8_t)(fh >> 24)};
    uint32_t rl = mk_req(ROP_CLOSE, 0, 0, pay, 4);
    int r = fs_roundtrip(rl);
    w->fds[fd] = SCHED_FD_EMPTY;
    if (r < 2)
        m3ApiReturn(WASI_ENOSYS);
    m3ApiReturn((int32_t)(fsresp[0] | (fsresp[1] << 8)));
}

/* shared read/write for routed fds; returns wasi errno, sets done */
int routed_rw(int32_t fd, bool write, uint8_t *buf, uint32_t len,
              uint32_t *done) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < 3 || fd >= SCHED_MAX_FDS || w->fds[fd] == SCHED_FD_EMPTY)
        return WASI_EBADF2;
    uint32_t fh = w->fds[fd];
    uint8_t pay[8 + 8192];
    pay[0] = (uint8_t)fh;
    pay[1] = (uint8_t)(fh >> 8);
    pay[2] = (uint8_t)(fh >> 16);
    pay[3] = (uint8_t)(fh >> 24);
    pay[4] = (uint8_t)len;
    pay[5] = (uint8_t)(len >> 8);
    pay[6] = (uint8_t)(len >> 16);
    pay[7] = (uint8_t)(len >> 24);
    if (write) {
        if (len > 8192)
            return WASI_ENOSYS; /* single-shot cap for now */
        for (uint32_t i = 0; i < len; i++)
            pay[8 + i] = buf[i];
    }
    uint32_t rl = mk_req(write ? ROP_WRITE : ROP_READ, 0, 0, pay,
                         write ? 8 + len : 8);
    int r = fs_roundtrip(rl);
    if (r < 6)
        return WASI_ENOSYS;
    int32_t err = (int32_t)(fsresp[0] | (fsresp[1] << 8));
    if (err != 0)
        return err;
    uint32_t n = (uint32_t)fsresp[2] | (uint32_t)fsresp[3] << 8 |
                 (uint32_t)fsresp[4] << 16 | (uint32_t)fsresp[5] << 24;
    if (!write) {
        uint32_t cp = n < len ? n : len;
        for (uint32_t i = 0; i < cp; i++)
            buf[i] = fsresp[6 + i];
    }
    *done = n;
    return 0;
}

/* ---------------- kernel namespace (abi/ABI.md §1 ports) ---------------- */

#include "ports.h"

m3ApiRawFunction(kern_port_create) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(const char *, name)
    m3ApiGetArg(int32_t, name_len)
    m3ApiReturn(port_create(sched_current_sid(), name,
                            name_len < 0 ? 0 : (uint32_t)name_len));
}

m3ApiRawFunction(kern_port_bind) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(const char *, name)
    m3ApiGetArg(int32_t, name_len)
    m3ApiReturn(port_bind(sched_current_sid(), name,
                          name_len < 0 ? 0 : (uint32_t)name_len));
}

m3ApiRawFunction(kern_port_send) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, h)
    m3ApiGetArgMem(const void *, buf)
    m3ApiGetArg(int32_t, len)
    m3ApiReturn(port_send(sched_current_sid(), h, buf,
                          len < 0 ? 0 : (uint32_t)len));
}

m3ApiRawFunction(kern_port_recv) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, h)
    m3ApiGetArgMem(void *, buf)
    m3ApiGetArg(int32_t, cap)
    m3ApiReturn(port_recv(sched_current_sid(), h, buf,
                          cap < 0 ? 0 : (uint32_t)cap));
}

/* block class imports (ABI v1.1, managed-runtime transport) */
extern "C" int devblk_rw(uint32_t sid, int write, uint64_t lba, void *buf,
                         uint32_t count);

m3ApiRawFunction(kern_blk_read) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, lba)
    m3ApiGetArgMem(void *, buf)
    m3ApiGetArg(int32_t, cnt)
    m3ApiReturn(devblk_rw(sched_current_sid(), 0,
                          (uint64_t)(uint32_t)lba, buf,
                          cnt < 0 ? 0 : (uint32_t)cnt));
}

m3ApiRawFunction(kern_blk_write) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, lba)
    m3ApiGetArgMem(const void *, buf)
    m3ApiGetArg(int32_t, cnt)
    m3ApiReturn(devblk_rw(sched_current_sid(), 1,
                          (uint64_t)(uint32_t)lba, (void *)buf,
                          cnt < 0 ? 0 : (uint32_t)cnt));
}

struct link_entry2 {
    const char *name;
    M3RawCall fn;
};
static const link_entry2 kernlinks[] = {
    {"kern_port_create", kern_port_create},
    {"kern_port_bind", kern_port_bind},
    {"kern_port_send", kern_port_send},
    {"kern_port_recv", kern_port_recv},
    {"kern_blk_read", kern_blk_read},
    {"kern_blk_write", kern_blk_write},
};
#define NKERN (sizeof(kernlinks) / sizeof(kernlinks[0]))

/* ---------------- conservative linkage stubs ----------------
 * Per-name stubs with preview1-correct errno semantics so stock runtimes'
 * probes behave sanely (e.g. Go's preopen scan terminates on EBADF).
 * None of them implement behavior beyond that. */

#define WASI_EBADF 8

static const void *stub_ret_i32(IM3Runtime, IM3ImportContext, uint64_t *_sp,
                                void *) {
    *((int32_t *)_sp++) = WASI_ENOSYS;
    return m3Err_none;
}
static const void *stub_ret_void(IM3Runtime, IM3ImportContext, uint64_t *,
                                 void *) {
    return m3Err_none;
}
static const void *stub_ret_other(IM3Runtime, IM3ImportContext, uint64_t *_sp,
                                  void *) {
    *_sp++ = 0;
    return m3Err_none;
}
static const void *stub_ebadf(IM3Runtime, IM3ImportContext, uint64_t *_sp,
                              void *) {
    *((int32_t *)_sp++) = WASI_EBADF;
    return m3Err_none;
}
static const void *stub_ok(IM3Runtime, IM3ImportContext, uint64_t *_sp,
                           void *) {
    *((int32_t *)_sp++) = WASI_ESUCCESS;
    return m3Err_none;
}

struct link_entry {
    const char *name;
    M3RawCall fn;
};

static const struct link_entry stubs[] = {
    {"fd_prestat_get", stub_ebadf},       /* ends preopen scans */
    {"fd_prestat_dir_name", stub_ebadf},
    {"fd_fdstat_get", stub_ebadf},
    {"fd_fdstat_set_flags", stub_ok},
    {"fd_close", stub_ok},
    {"poll_oneoff", stub_ret_i32},        /* ENOSYS: no timers/events yet */
};
#define NSTUBS (sizeof(stubs) / sizeof(stubs[0]))

static const struct link_entry profile[] = {
    {"fd_write", wasi_fd_write},
    {"proc_exit", wasi_proc_exit},
    {"clock_time_get", wasi_clock_time_get},
    {"random_get", wasi_random_get},
    {"args_sizes_get", wasi_args_sizes_get},
    {"args_get", wasi_args_get},
    {"environ_sizes_get", wasi_environ_sizes_get},
    {"environ_get", wasi_environ_get},
    {"sched_yield", wasi_sched_yield},
    /* Phase 5 routed path ops (ABI v1.1) */
    {"path_open", wasi_path_open},
    {"fd_read", wasi_fd_read},
    {"path_create_directory", wasi_path_create_directory},
    {"fd_close", wasi_fd_close_routed},
};
#define NPROFILE (sizeof(profile) / sizeof(profile[0]))

static char sig_buf[128];

/* Build wasm3 signature string ("ret(args)", letters v/i/I/f/F) that is
 * guaranteed to match the import's actual type. */
static char type_char(u8 t) {
    switch (t) {
    case c_m3Type_i32: return 'i';
    case c_m3Type_i64: return 'I';
    case c_m3Type_f32: return 'f';
    default:           return 'F';
    }
}

static const char *sig_of(IM3FuncType ft) {
    char *p = sig_buf;
    if (GetFuncTypeNumResults(ft))
        *p++ = type_char(GetFuncTypeResultType(ft, 0));
    else
        *p++ = 'v';
    *p++ = '(';
    u16 n = GetFuncTypeNumParams(ft);
    for (u16 i = 0; i < n; i++)
        *p++ = type_char(GetFuncTypeParamType(ft, i));
    *p++ = ')';
    *p = 0;
    return sig_buf;
}

const char *wasi_link_module(IM3Module mod) {
    for (u32 i = 0; i < mod->numFunctions; i++) {
        M3Function *f = &mod->functions[i];
        if (!f->import.moduleUtf8)
            continue;
        if (!strcmp(f->import.moduleUtf8, "kernel")) {
            M3RawCall fn = 0;
            for (unsigned e = 0; e < NKERN; e++) {
                if (!strcmp(kernlinks[e].name, f->import.fieldUtf8)) {
                    fn = kernlinks[e].fn;
                    break;
                }
            }
            if (!fn) {
                console_puts("[link] unknown kernel import: ");
                console_puts(f->import.fieldUtf8);
                console_puts("\n");
                return "unknown kernel import";
            }
            (void)fn;
            M3Result r = m3_LinkRawFunction(mod, f->import.moduleUtf8,
                                            f->import.fieldUtf8,
                                            sig_of(f->funcType), fn);
            if (r)
                return r;
            continue;
        }
        if (!strcmp(f->import.moduleUtf8, "wasi_snapshot_preview1")) {
            M3RawCall fn = 0;
            for (unsigned e = 0; e < NPROFILE; e++) {
                if (!strcmp(profile[e].name, f->import.fieldUtf8)) {
                    fn = profile[e].fn;
                    break;
                }
            }
            if (!fn) {
                for (unsigned t = 0; t < NSTUBS; t++) {
                    if (!strcmp(stubs[t].name, f->import.fieldUtf8)) {
                        fn = stubs[t].fn;
                        break;
                    }
                }
            }
            if (!fn) {
                /* generic linkage stub by result shape */
                if (GetFuncTypeNumResults(f->funcType) == 0)
                    fn = stub_ret_void;
                else if (GetFuncTypeResultType(f->funcType, 0) == c_m3Type_i32)
                    fn = stub_ret_i32;
                else
                    fn = stub_ret_other;
                console_puts("[wasi] generic stub: ");
                console_puts(f->import.fieldUtf8);
                console_puts("\n");
            }
            M3Result r = m3_LinkRawFunction(mod, f->import.moduleUtf8,
                                            f->import.fieldUtf8,
                                            sig_of(f->funcType), fn);
            if (r)
                return r;
        }
    }
    return m3Err_none;
}
