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
#include "vfio.h"

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
    /* TSC ticks → ns. F59: divide first (with remainder correction) so
     * cycles*1e6 can never overflow u64 on large cycle accumulators. */
    uint64_t khz = g_tsc_khz;
    if (!khz)
        return 0;
    uint64_t c = cpu_cycles();
    return (c / khz) * 1000000ULL + ((c % khz) * 1000000ULL) / khz;
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

m3ApiRawFunction(wasi_sched_yield) {
    m3ApiReturnType(int32_t)
    extern void sched_yield_current(void);
    sched_yield_current(); /* coroutine switch; resumes here next quantum */
    m3ApiReturn(WASI_ESUCCESS);
}

/* void-declared shape (guests/lib): no result slot exists */
m3ApiRawFunction(wasi_sched_yield_v) {
    extern void sched_yield_current(void);
    sched_yield_current();
    m3ApiSuccess();
}

/* ---- root preopen (fd 3): lets stock runtimes path_open "/" files ---- */
#define WASI_PREOPEN_FD 3
#define FD_PREOPEN_ROOT 0x7FFFFFFEu

/* ---------------- routed path ops (Phase 5, ABI v1.1) ----------------
 * preview1 path_open/fd_read/fd_write(>=3)/fd_close/path_create_directory
 * forward to fs.wasm over its §1 port using the SAME op encoding as
 * direct-port clients (services/fs server.go): STAT=1 LIST=2 READ=3
 * WRITE=4 CREATE=5 MKDIR=6 DELETE=7, payload = {u16 len, path, ...}.
 * Replies are canonical 24-byte-header frames: status i32 @24, body @28.
 * The server is stateless/path-based; preview1 fds map to {path, cursor}
 * kept kernel-side below. */

#define FSOP_STAT 1
#define FSOP_READ 3
#define FSOP_WRITE 4
#define FSOP_CREATE 5
#define FSOP_MKDIR 6

/* services/fs wire status (guests/lib/fsclient.go) */
#define FS_ST_IO (-1)
#define FS_ST_NOENTRY (-2)
#define FS_ST_EXISTS (-3)
#define FS_ST_NOTDIR (-4)
#define FS_ST_ISDIR (-5)
#define FS_ST_NOSPACE (-6)
#define FS_ST_BADNAME (-7)
#define FS_ST_NOTEMPTY (-8)
#define FS_ST_RANGE (-9)
#define FS_ST_ACCESS (-10)

/* preview1 errno (wasi_preview1 numbering) */
#define WASI_EACCES 2
#define WASI_EEXIST 20
#define WASI_EIO 29
#define WASI_EISDIR 31
#define WASI_ENOSPC 51
#define WASI_ENOENT 44
#define WASI_ENOTDIR 54
#define WASI_ENOTEMPTY 55
#define WASI_ENAMETOOLONG_WASI 37

static int32_t fserr_to_wasi(int32_t st) {
    switch (st) {
    case 0:
        return WASI_ESUCCESS;
    case FS_ST_NOENTRY:
        return WASI_ENOENT;
    case FS_ST_EXISTS:
        return WASI_EEXIST;
    case FS_ST_NOTDIR:
        return WASI_ENOTDIR;
    case FS_ST_ISDIR:
        return WASI_EISDIR;
    case FS_ST_NOSPACE:
        return WASI_ENOSPC;
    case FS_ST_BADNAME:
    case FS_ST_RANGE:
        return WASI_EINVAL;
    case FS_ST_NOTEMPTY:
        return WASI_ENOTEMPTY;
    case FS_ST_ACCESS:
        return WASI_EACCES;
    default:
        return WASI_EIO;
    }
}

/* per-session fd -> routed path + offset cursor */
#define RFD_PATH_MAX 192
struct rfd {
    bool used;
    uint64_t off;
    char path[RFD_PATH_MAX];
};
static rfd fdtab[12][SCHED_MAX_FDS];

static rfd *rfd_of(int32_t fd) {
    uint32_t sid = sched_current_sid();
    if (sid >= 12 || fd < 3 || fd >= SCHED_MAX_FDS)
        return 0;
    if (!fdtab[sid][fd].used)
        return 0;
    return &fdtab[sid][fd];
}

static void rfd_clear(uint32_t sid, int32_t fd) {
    if (sid < 12 && fd >= 3 && fd < SCHED_MAX_FDS)
        fdtab[sid][fd] = {};
}

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
    __builtin_memset(fsresp, 0, sizeof(fsresp)); /* F16: no stale tail */
    if (fsroute_expect(sq, sched_name_of(sched_current_sid())) != 0) {
        return -1;
    }
    if (!ports_enqueue_by_name("fs", fsreq, reqlen)) {
        return -1;
    }
    return fsroute_wait(sq, fsresp, sizeof(fsresp));
}

/* canonical reply accessors: status i32 @24, body @28 */
static int32_t rep_status(void) {
    return (int32_t)((uint32_t)fsresp[24] | (uint32_t)fsresp[25] << 8 |
                     (uint32_t)fsresp[26] << 16 |
                     (uint32_t)fsresp[27] << 24);
}
static const uint8_t *rep_body(void) { return fsresp + 28; }
/* fresh body bytes actually received by the last roundtrip */
static uint32_t rep_body_len(int r) {
    return r > 28 ? (uint32_t)r - 28 : 0;
}

static int alloc_fd(void) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w)
        return -1;
    if (w->fds[WASI_PREOPEN_FD] == SCHED_FD_EMPTY)
        w->fds[WASI_PREOPEN_FD] = FD_PREOPEN_ROOT; /* lazy root preopen */
    for (int fd = WASI_PREOPEN_FD + 1; fd < SCHED_MAX_FDS; fd++)
        if (w->fds[fd] == SCHED_FD_EMPTY) {
            w->fds[fd] = 0x7FFFFFFF; /* reserve */
            return fd;
        }
    return -1;
}

/* copy a guest path into a kernel buffer with bounds checking */
static bool fetch_path(IM3Runtime runtime, char *path, uint32_t cap,
                       const char *gpath, int32_t gpath_len) {
    if (!gpath || gpath_len < 0)
        return false;
    uint32_t plen = (uint32_t)gpath_len;
    if (plen >= cap)
        return false;
    if (plen && !mem_ok(runtime, gpath, plen))
        return false;
    __builtin_memcpy(path, gpath, plen);
    path[plen] = 0;
    return true;
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

    char kpath[RFD_PATH_MAX];
    if (!fetch_path(runtime, kpath, sizeof(kpath), path, path_len))
        m3ApiReturn(WASI_EINVAL);
    /* Stock runtimes strip the preopen prefix ("-> relative remnant").
     * The kernel-routed transport is absolute-path based, so a relative
     * path opened against the ROOT preopen is rebased to "/" here. */
    if (kpath[0] != '/' && kpath[0] != 0 &&
        (dirfd == WASI_PREOPEN_FD)) {
        uint32_t L = (uint32_t)__builtin_strlen(kpath);
        if (L + 2 > RFD_PATH_MAX)
            m3ApiReturn(WASI_ENAMETOOLONG_WASI);
        for (uint32_t i = L + 1; i > 0; i--)
            kpath[i] = kpath[i - 1];
        kpath[0] = '/';
    }
    uint32_t plen = (uint32_t)__builtin_strlen(kpath);

    int fd = alloc_fd();
    if (fd < 0)
        m3ApiReturn(WASI_EBADF2);

    /* O_CREAT: create-or-truncate first (server CREATE semantics) */
    if (oflags & 1) {
        uint32_t rl = mk_req(FSOP_CREATE, kpath, plen, 0, 0);
        int r = fs_roundtrip(rl);
        if (r < 28) {
            rfd_clear(sched_current_sid(), fd);
            sched_wasi_state *wf = sched_wasi_current();
            wf->fds[fd] = SCHED_FD_EMPTY;
            m3ApiReturn(r < 0 ? WASI_ENOSYS : WASI_EIO);
        }
        int32_t st = rep_status();
        if (st != 0) {
            rfd_clear(sched_current_sid(), fd);
            sched_wasi_state *wf = sched_wasi_current();
            wf->fds[fd] = SCHED_FD_EMPTY;
            m3ApiReturn(fserr_to_wasi(st));
        }
    }

    /* verify existence via STAT */
    uint32_t rl = mk_req(FSOP_STAT, kpath, plen, 0, 0);
    int r = fs_roundtrip(rl);
    if (r < 28 || rep_status() != 0) {
        int32_t st = r < 28 ? FS_ST_IO : rep_status();
        rfd_clear(sched_current_sid(), fd);
        sched_wasi_state *wf = sched_wasi_current();
        wf->fds[fd] = SCHED_FD_EMPTY;
        m3ApiReturn(fserr_to_wasi(st));
    }

    rfd *e;
    uint32_t sid = sched_current_sid();
    if (sid >= 12) {
        sched_wasi_state *wf = sched_wasi_current();
        wf->fds[fd] = SCHED_FD_EMPTY;
        m3ApiReturn(WASI_ENOSYS);
    }
    e = &fdtab[sid][fd];
    e->used = true;
    e->off = 0;
    __builtin_memcpy(e->path, kpath, plen + 1);

    if (opened_fd && mem_ok(runtime, opened_fd, 4))
        *opened_fd = (uint32_t)fd;
    m3ApiReturn(0);
}

m3ApiRawFunction(wasi_path_create_directory) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, dirfd)
    m3ApiGetArgMem(char *, path)
    m3ApiGetArg(int32_t, path_len)
    (void)dirfd;
    char kpath[RFD_PATH_MAX];
    if (!fetch_path(runtime, kpath, sizeof(kpath), path, path_len))
        m3ApiReturn(WASI_EINVAL);
    uint32_t rl =
        mk_req(FSOP_MKDIR, kpath, (uint32_t)__builtin_strlen(kpath), 0, 0);
    int r = fs_roundtrip(rl);
    if (r < 28)
        m3ApiReturn(WASI_ENOSYS);
    m3ApiReturn(fserr_to_wasi(rep_status()));
}

m3ApiRawFunction(wasi_fd_close_routed) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < 3 || fd >= SCHED_MAX_FDS || w->fds[fd] == SCHED_FD_EMPTY ||
        !rfd_of(fd)) {
        m3ApiReturn(WASI_EBADF2);
    }
    /* server is stateless: no roundtrip needed */
    rfd_clear(sched_current_sid(), fd);
    w->fds[fd] = SCHED_FD_EMPTY;
    m3ApiReturn(0);
}

/* shared read/write for routed fds; returns wasi errno, sets done */
int routed_rw(int32_t fd, bool write, uint8_t *buf, uint32_t len,
              uint32_t *done) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < 3 || fd >= SCHED_MAX_FDS || w->fds[fd] == SCHED_FD_EMPTY)
        return WASI_EBADF2;
    rfd *e = rfd_of(fd);
    if (!e)
        return WASI_EBADF2;
    if (len > 8192)
        len = 8192; /* single datagram payload budget */

    /* tail payload after mk_req's {u16 pLen, path}: {u64 off, u16 cnt[,data]} */
    uint32_t plen = (uint32_t)__builtin_strlen(e->path);
    uint8_t pay[10 + 8192];
    uint32_t o = 0;
    for (int b = 0; b < 8; b++)
        pay[o++] = (uint8_t)(e->off >> (8 * b));
    pay[o++] = (uint8_t)len;
    pay[o++] = (uint8_t)(len >> 8);
    if (write) {
        for (uint32_t i = 0; i < len; i++)
            pay[o++] = buf[i];
    }
    uint32_t rl =
        mk_req(write ? FSOP_WRITE : FSOP_READ, e->path, plen, pay, o);
    int r = fs_roundtrip(rl);
    if (r < 28)
        return WASI_ENOSYS;
    int32_t st = rep_status();
    if (st != 0)
        return fserr_to_wasi(st);

    const uint8_t *body = rep_body();
    uint32_t blen = rep_body_len(r);
    if (write) {
        /* WRITE body: u32 newSize */
        if (blen >= 4)
            e->off += len;
        *done = len;
        return 0;
    }
    /* READ body: {u16 got, data} -- F16 clamp on every bound */
    if (blen < 2)
        return WASI_EIO;
    uint32_t n = (uint32_t)(body[0] | (body[1] << 8));
    uint32_t avail = blen - 2;
    uint32_t cp = n;
    if (cp > len)
        cp = len;
    if (cp > avail)
        cp = avail;
    for (uint32_t i = 0; i < cp; i++)
        buf[i] = body[2 + i];
    e->off += cp;
    *done = cp;
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

/* input/focus (abi/ABI.md §4) */
extern "C" int input_recv(uint32_t sid, void *out, uint32_t cap);
extern "C" int input_focus_set(uint32_t caller_sid, int handle);

static int irc;
m3ApiRawFunction(kern_input_recv) {
    m3ApiReturnType(int32_t)
    m3ApiGetArgMem(void *, buf)
    m3ApiGetArg(int32_t, cap)
    m3ApiReturn(input_recv(sched_current_sid(), buf,
                           cap < 0 ? 0 : (uint32_t)cap));
}

m3ApiRawFunction(kern_focus_set) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, h)
    m3ApiReturn(input_focus_set(sched_current_sid(), h));
}

/* F58 adjunct: legacy void-declared shape. Raw-call layout is
 * [rets..., args...] (m3ApiReturnType consumes a slot), so a v(i)-shaped
 * caller must be served by a wrapper that reads its arg at slot0 -- the
 * i(i)-returning implementation would read garbage. */
m3ApiRawFunction(kern_focus_set_v) {
    m3ApiGetArg(int32_t, h)
    input_focus_set(sched_current_sid(), h);
    m3ApiSuccess();
}

/* PCI/VFIO (§12) + FB (§13) + doorbell (§14) — abi/ABI.md v2.0 */
#include "pci.h"
#include "vfio.h"

m3ApiRawFunction(kern_pci_read32) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    m3ApiGetArg(int32_t, off)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int32_t v = pci_read32((uint32_t)bus, (uint32_t)dev, (uint32_t)fn, (uint32_t)off);
    m3ApiReturn(v);
}
m3ApiRawFunction(kern_pci_write32) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    m3ApiGetArg(int32_t, off)
    m3ApiGetArg(int32_t, val)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int32_t r = pci_write32((uint32_t)bus,(uint32_t)dev,(uint32_t)fn,(uint32_t)off,(uint32_t)val);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_pci_map_bar) {
    m3ApiReturnType(int64_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    m3ApiGetArg(int32_t, bar)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int64_t off = vfio_map_bar(sid, (uint32_t)bus,(uint32_t)dev,(uint32_t)fn,(uint32_t)bar);
    m3ApiReturn(off);
}
m3ApiRawFunction(kern_pci_unmap_bar) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    m3ApiGetArg(int32_t, bar)
    int r = vfio_unmap_bar(sched_current_sid(), (uint32_t)bus,(uint32_t)dev,(uint32_t)fn,(uint32_t)bar);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_pci_enable_busmaster) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int r = pci_enable_busmaster((uint32_t)bus,(uint32_t)dev,(uint32_t)fn);
    // also allow vfio to track if needed
    (void)vfio_map_bar; // silence unused
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_pci_bind_irq) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    m3ApiGetArg(int32_t, type)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int h = vfio_bind_irq(sid, (uint32_t)bus,(uint32_t)dev,(uint32_t)fn,(uint32_t)type);
    m3ApiReturn(h);
}
m3ApiRawFunction(kern_pci_flr) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, bus)
    m3ApiGetArg(int32_t, dev)
    m3ApiGetArg(int32_t, fn)
    uint32_t sid = sched_current_sid();
    if (!(sched_capmask_of(sid) & SCHED_CAP_PCI)) m3ApiReturn(-1);
    int r = pci_flr((uint32_t)bus,(uint32_t)dev,(uint32_t)fn);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_fb_set_mode) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, w)
    m3ApiGetArg(int32_t, h)
    m3ApiGetArg(int32_t, bpp)
    int r = vfio_fb_set_mode(sched_current_sid(), (uint32_t)w,(uint32_t)h,(uint32_t)bpp);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_fb_set_cursor) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, x)
    m3ApiGetArg(int32_t, y)
    int r = vfio_fb_set_cursor(sched_current_sid(), (uint32_t)x,(uint32_t)y);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_fb_present) {
    m3ApiReturnType(int32_t)
    // Present the guest framebuffer window to the physical LFB by copying
    // the session's FB BAR window (emulated DMA) into the real hardware
    // framebuffer. Returns the result of the underlying VFIO present op.
    int r = vfio_fb_present(sched_current_sid());
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_doorbell_wait) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, handle)
    m3ApiGetArg(int32_t, timeout_ms)
    int r = vfio_doorbell_wait(sched_current_sid(), (uint32_t)handle,(uint32_t)timeout_ms);
    m3ApiReturn(r);
}
m3ApiRawFunction(kern_vmware_backdoor) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, op)
    m3ApiGetArgMem(uint32_t *, time_low)
    m3ApiGetArgMem(uint32_t *, time_high)
    int r = 0;
    switch (op) {
        case 0: // present
            r = vmware_backdoor_present();
            break;
        case 1: // get_time
            {
                uint32_t low = 0, high = 0;
                vmware_backdoor_get_time(&low, &high);
                r = (int)low;
                if (time_low)  *time_low = low;
                if (time_high) *time_high = high;
            }
            break;
        default:
            r = -1;
    }
    m3ApiReturn(r);
}

struct link_entry2 {
    const char *name;
    const char *sig;
    M3RawCall fn;
    const char *alt;
    M3RawCall fn_alt;
};
static const link_entry2 kernlinks[] = {
    {"kern_port_create", "i(ii)", kern_port_create, 0},
    {"kern_port_bind", "i(ii)", kern_port_bind, 0},
    {"kern_port_send", "i(iii)", kern_port_send, 0},
    {"kern_port_recv", "i(iii)", kern_port_recv, 0},
    {"kern_blk_read", "i(iii)", kern_blk_read, 0},
    {"kern_blk_write", "i(iii)", kern_blk_write, 0},
    {"kern_input_recv", "i(ii)", kern_input_recv, 0},
    {"kern_focus_set", "i(i)", kern_focus_set, "v(i)", kern_focus_set_v}, /* lib ships both */
    {"kern_pci_read32", "i(iiii)", kern_pci_read32, 0},
    {"kern_pci_write32", "i(iiiii)", kern_pci_write32, 0},
    {"kern_pci_map_bar", "I(iiii)", kern_pci_map_bar, 0},
    {"kern_pci_unmap_bar", "i(iiii)", kern_pci_unmap_bar, 0},
    {"kern_pci_enable_busmaster", "i(iii)", kern_pci_enable_busmaster, 0},
    {"kern_pci_bind_irq", "i(iiii)", kern_pci_bind_irq, 0},
    {"kern_pci_flr", "i(iii)", kern_pci_flr, 0},
    {"kern_fb_set_mode", "i(iii)", kern_fb_set_mode, 0},
    {"kern_fb_set_cursor", "i(ii)", kern_fb_set_cursor, 0},
    {"kern_fb_present", "i()", kern_fb_present, 0},
    {"kern_doorbell_wait", "i(ii)", kern_doorbell_wait, 0},
    {"kern_vmware_backdoor", "i(iPP)", kern_vmware_backdoor, 0},
};
#define NKERN (sizeof(kernlinks) / sizeof(kernlinks[0]))

/* ---------------- conservative linkage stubs ----------------
 * Per-name stubs with preview1-correct errno semantics so stock runtimes'
 * probes behave sanely (e.g. Go's preopen scan terminates on EBADF).
 * None of them implement behavior beyond that. */

#define WASI_EBADF 8

/* poll_oneoff: return 0 events immediately (no timers in v1). This keeps
 * Go runtimes alive through fatal paths that usleep via poll.
 * Full preview1 signature: (in, out, nsubscriptions, nevents) -> errno */
m3ApiRawFunction(wasi_poll_oneoff) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, in)
    m3ApiGetArgMem(uint32_t *, out)
    m3ApiGetArg(int32_t, subcount)
    m3ApiGetArgMem(uint32_t *, nevents)
    (void)in;
    (void)subcount;
    /* NO yield here: returning immediately lets Go's Sleep become a
     * brief busy-spin that burns THIS session's quantum fairly, keeping
     * all Go goroutines responsive. Yielding inside this stub would
     * suspend the entire Go runtime mid-Sleep, starving wire-pump and
     * other goroutines for many quanta (observed as total RX stall). */
    if (nevents && mem_ok(runtime, nevents, 4))
        *nevents = 0;
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_fd_prestat_get) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    m3ApiGetArgMem(uint8_t *, buf)
    sched_wasi_state *w = wctx();
    if (!w || fd != WASI_PREOPEN_FD)
        m3ApiReturn(WASI_EBADF);
    if (w->fds[fd] == SCHED_FD_EMPTY)
        w->fds[fd] = FD_PREOPEN_ROOT; /* auto-provision root preopen */
    if (buf && mem_ok(runtime, buf, 8)) {
        buf[0] = 0; /* preopentype_dir */
        for (int i = 1; i < 8; i++)
            buf[i] = 0;
        buf[4] = 1; buf[5] = 0; buf[6] = 0; buf[7] = 0; /* name len 1 */
    }
    m3ApiReturn(WASI_ESUCCESS);
}

m3ApiRawFunction(wasi_fd_prestat_dir_name) {
    m3ApiReturnType(int32_t)
    m3ApiGetArg(int32_t, fd)
    m3ApiGetArgMem(char *, buf)
    m3ApiGetArg(int32_t, len)
    sched_wasi_state *w = wctx();
    if (!w || fd != WASI_PREOPEN_FD)
        m3ApiReturn(WASI_EBADF);
    if (w->fds[fd] == SCHED_FD_EMPTY)
        w->fds[fd] = FD_PREOPEN_ROOT;
    if (len < 1)
        m3ApiReturn(WASI_EINVAL);
    if (buf && mem_ok(runtime, buf, 1))
        buf[0] = '/';
    m3ApiReturn(WASI_ESUCCESS);
}

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
    const char *sig;
    M3RawCall fn;
    /* F58 adjunct: some shipped artifacts declare a LEGACY VOID shape for
     * the same import (guests/lib's sched_yield/focus_set). Raw-call slot
     * layout is [rets..., args...], so each accepted shape needs its own
     * implementation -- fn serves sig, fn_alt serves alt. Anything beyond
     * these two shapes still fails instantiation loudly. */
    const char *alt;
    M3RawCall fn_alt;
};

/* F58: every NAMED import carries the canonical signature from its spec
 * (abi/ABI.md §1/§3/§4 for kern_*, preview1 for wasi_*). Linking compares
 * the module's declared type against THIS string, so a wrong-arity or
 * wrong-type import fails instantiation loudly instead of corrupting the
 * raw-call stack. wasm3 sig format: result chars, then (arg chars). */
static const struct link_entry stubs[] = {
    {"fd_prestat_get", "i(ii)", wasi_fd_prestat_get, 0},
    {"fd_prestat_dir_name", "i(iii)", wasi_fd_prestat_dir_name, 0},
    {"fd_fdstat_get", "i(ii)", stub_ebadf, 0},
    {"fd_fdstat_set_flags", "i(ii)", stub_ok, 0},
    {"fd_close", "i(i)", stub_ok, 0},
    {"poll_oneoff", "i(iiii)", wasi_poll_oneoff, 0},  /* immediate: 0 events */
};
#define NSTUBS (sizeof(stubs) / sizeof(stubs[0]))

static const struct link_entry profile[] = {
    {"fd_write", "i(iiii)", wasi_fd_write, 0},
    {"proc_exit", "v(i)", wasi_proc_exit, 0},
    {"clock_time_get", "i(iIi)", wasi_clock_time_get, 0},
    {"random_get", "i(ii)", wasi_random_get, 0},
    {"args_sizes_get", "i(ii)", wasi_args_sizes_get, 0},
    {"args_get", "i(ii)", wasi_args_get, 0},
    {"environ_sizes_get", "i(ii)", wasi_environ_sizes_get, 0},
    {"environ_get", "i(ii)", wasi_environ_get, 0},
    {"sched_yield", "i()", wasi_sched_yield, "v()", wasi_sched_yield_v}, /* Go runtime i() vs lib v() */
    /* Phase 5 routed path ops (ABI v1.1) */
    {"path_open", "i(iiiiiIIii)", wasi_path_open, 0},
    {"fd_read", "i(iiii)", wasi_fd_read, 0},
    {"path_create_directory", "i(iii)", wasi_path_create_directory, 0},
    {"fd_close", "i(i)", wasi_fd_close_routed, 0},
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

extern "C" {
M3Result LinkRawFunction(IM3Module, IM3Function, ccstr_t, const void *,
                         const void *);
}

const char *wasi_link_module(IM3Module mod) {
    /* F58 adjunct: m3's public linker is BY-NAME and rebinds EVERY import
     * sharing that name with one signature -- impossible when one module
     * legitimately imports the same name under two shapes (Go runtime's
     * i() sched_yield vs guests/lib's v()). Bind each FUNCTION record
     * directly instead, with its own shape-checked signature. */

    for (u32 i = 0; i < mod->numFunctions; i++) {
        M3Function *f = &mod->functions[i];
        if (!f->import.moduleUtf8)
            continue;
        if (!strcmp(f->import.moduleUtf8, "kernel")) {
            M3RawCall fn = 0;
            unsigned kei = NKERN;
            const char *ksig = 0, *kalt = 0, *kname = 0;
            for (unsigned e = 0; e < NKERN; e++) {
                if (!strcmp(kernlinks[e].name, f->import.fieldUtf8)) {
                    fn = kernlinks[e].fn;
                    ksig = kernlinks[e].sig;
                    kalt = kernlinks[e].alt;
                    kname = kernlinks[e].name;
                    kei = e;
                    break;
                }
            }
            if (!fn) {
                console_puts("[link] unknown kernel import: ");
                console_puts(f->import.fieldUtf8);
                console_puts("\n");
                return "unknown kernel import";
            }
            /* F58: enforce the canonical shape; accept only the documented
             * legacy alt (see link_entry.alt). Link with the module's own
             * declared signature when it matches an accepted shape so the
             * raw-call slot layout always agrees with the caller. */
            const char *modsig = sig_of(f->funcType);
            const char *use;
            M3RawCall ufn = fn;
            if (!strcmp(modsig, ksig))
                use = ksig;
            else if (kalt && !strcmp(modsig, kalt)) {
                use = modsig;
                ufn = kernlinks[kei].fn_alt;
                console_puts("[link] void-shape variant: ");
                console_puts(kname);
                console_puts("\n");
            } else {
                console_puts("[link] SIGFAIL ");
                console_puts(f->import.fieldUtf8);
                console_puts(" want=");
                console_puts(ksig);
                if (kalt) {
                    console_puts(" or=");
                    console_puts(kalt);
                }
                console_puts(" module-declared=");
                console_puts(modsig);
                console_puts("\n");
                return "function signature mismatch";
            }
            M3Result r =
                LinkRawFunction(mod, f, use, (const void *)ufn, 0);
            if (r)
                return r;
            continue;
        }
        if (!strcmp(f->import.moduleUtf8, "wasi_snapshot_preview1")) {
            M3RawCall fn = 0;
            unsigned pei = NPROFILE;
            const char *sig = 0, *alt = 0;
            for (unsigned e = 0; e < NPROFILE; e++) {
                if (!strcmp(profile[e].name, f->import.fieldUtf8)) {
                    fn = profile[e].fn;
                    sig = profile[e].sig;
                    alt = profile[e].alt;
                    pei = e;
                    break;
                }
            }
            if (!fn) {
                for (unsigned t = 0; t < NSTUBS; t++) {
                    if (!strcmp(stubs[t].name, f->import.fieldUtf8)) {
                        fn = stubs[t].fn;
                        sig = stubs[t].sig;
                        alt = stubs[t].alt;
                        break;
                    }
                }
            }
            if (!fn) {
                /* generic linkage stub by result shape. No kernel-side
                 * contract exists for unknown names, so the module's own
                 * declaration defines the ABI here (self-roundtrip only);
                 * the audit line keeps these visible. */
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
            /* F58: same shape enforcement as the kernel-import branch */
            const char *modsig = sig_of(f->funcType);
            const char *use;
            M3RawCall ufn = fn;
            if (!sig)
                use = modsig; /* generic stub: self-declared */
            else if (!strcmp(modsig, sig))
                use = sig;
            else if (alt && !strcmp(modsig, alt)) {
                use = modsig;
                ufn = profile[pei].fn_alt;
                console_puts("[link] void-shape variant: ");
                console_puts(f->import.fieldUtf8);
                console_puts("\n");
            } else {
                console_puts("[link] SIGFAIL ");
                console_puts(f->import.fieldUtf8);
                console_puts(" want=");
                console_puts(sig);
                if (alt) {
                    console_puts(" or=");
                    console_puts(alt);
                }
                console_puts(" module-declared=");
                console_puts(modsig);
                console_puts("\n");
                return "function signature mismatch";
            }
            M3Result r = LinkRawFunction(mod, f, use, (const void *)fn, 0);
            if (r) {
                console_puts("[link] ERR wasi:");
                console_puts(f->import.fieldUtf8);
                console_puts(" err=");
                console_puts(r);
                console_puts(" use=");
                console_puts(use);
                console_puts(" modsig=");
                console_puts(sig_of(f->funcType));
                console_puts(" rets=");
                console_hex64(GetFuncTypeNumResults(f->funcType));
                console_puts(" args=");
                console_hex64(GetFuncTypeNumParams(f->funcType));
                console_puts("\n");
                return r;
            }
        }
    }
    return m3Err_none;
}
