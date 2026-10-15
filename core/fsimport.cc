/* Backend-neutral kernel-routed preview1 file layer. See fsimport.h for
 * why this exists and for the wire contract. The code below was moved out
 * of core/wasi_glue.cc unchanged apart from the rename; the roundtrips,
 * bounds clamps and audit comments are the ones that were already there,
 * and they are the reason the two bridges could share it safely. */

#include "fsimport.h"

#include "fsroute.h"
#include "plat.h"
#include "sched.h"

#include <string.h>

extern "C" bool ports_enqueue_by_name(const char *, const void *, uint32_t);

static void put16_(uint8_t *p, uint16_t v) {
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
}

/* ---- wire op codes (services/fs server.go) ---- */
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

int32_t fsimport_errno_for_status(int32_t st) {
    switch (st) {
    case 0:
        return FSIMPORT_ESUCCESS;
    case FS_ST_NOENTRY:
        return FSIMPORT_ENOENT;
    case FS_ST_EXISTS:
        return FSIMPORT_EEXIST;
    case FS_ST_NOTDIR:
        return FSIMPORT_ENOTDIR;
    case FS_ST_ISDIR:
        return FSIMPORT_EISDIR;
    case FS_ST_NOSPACE:
        return FSIMPORT_ENOSPC;
    case FS_ST_BADNAME:
    case FS_ST_RANGE:
        return FSIMPORT_EINVAL;
    case FS_ST_NOTEMPTY:
        return FSIMPORT_ENOTEMPTY;
    case FS_ST_ACCESS:
        return FSIMPORT_EACCES;
    default:
        return FSIMPORT_EIO;
    }
}

/* per-session fd -> routed path + offset cursor */
struct rfd {
    bool used;
    uint64_t off;
    char path[FSIMPORT_PATH_MAX];
};
static rfd fdtab[MAX_SESSIONS][SCHED_MAX_FDS];

static rfd *rfd_of(int32_t fd) {
    uint32_t sid = sched_current_sid();
    if (sid >= MAX_SESSIONS || fd < FSIMPORT_PREOPEN_FD || fd >= SCHED_MAX_FDS)
        return 0;
    if (!fdtab[sid][fd].used)
        return 0;
    return &fdtab[sid][fd];
}

static void rfd_clear(uint32_t sid, int32_t fd) {
    if (sid < MAX_SESSIONS && fd >= FSIMPORT_PREOPEN_FD && fd < SCHED_MAX_FDS)
        fdtab[sid][fd] = {};
}

static sched_wasi_state *wctx(void) { return sched_wasi_current(); }

/* build framed request: op seq uid pathlen path payload */
/* frame: {u16 op,u16 seq,u32 uid,char rname[16],u16 path_len,path,payload} */
static uint32_t mk_req(uint16_t op, const char *path, uint32_t plen,
                       const uint8_t *payload, uint32_t paylen) {
    sched_wasi_state *w = wctx();
    if (!w)
        return 0;
    uint8_t *fsreq = w->fsreq;
    put16_(fsreq, op);
    uint16_t sq = ++w->fs_seq_ctr;
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
    /* F-AUDIT-11: refuse to silently truncate. If the framed request
     * would exceed the buffer, return 0 so the caller surfaces a
     * proper ENOSYS instead of an opaque fs parse error. */
    if ((uint64_t)plen + (uint64_t)paylen + 26 > sizeof(w->fsreq)) {
        console_puts("[wasi] mk_req: frame too large\n");
        return 0;
    }
    for (uint32_t i = 0; i < plen; i++)
        fsreq[o++] = (uint8_t)path[i];
    for (uint32_t i = 0; i < paylen; i++)
        fsreq[o++] = payload[i];
    return o;
}

static int fs_roundtrip(uint32_t reqlen) {
    sched_wasi_state *w = wctx();
    if (!w)
        return -1;
    uint8_t *fsreq = w->fsreq;
    uint8_t *fsresp = w->fsresp;
    uint16_t sq = ++w->fs_rt_seq;
    fsreq[2] = (uint8_t)sq;
    fsreq[3] = (uint8_t)(sq >> 8);
    __builtin_memset(fsresp, 0, sizeof(w->fsresp)); /* F16: no stale tail */
    if (fsroute_expect(sq, sched_name_of(sched_current_sid())) != 0) {
        return -1;
    }
    if (!ports_enqueue_by_name("fs", fsreq, reqlen)) {
        return -1;
    }
    return fsroute_wait(sq, fsresp, sizeof(w->fsresp));
}

/* canonical reply accessors: status i32 @24, body @28 */
static int32_t rep_status(void) {
    sched_wasi_state *w = wctx();
    if (!w)
        return -1;
    uint8_t *fsresp = w->fsresp;
    return (int32_t)((uint32_t)fsresp[24] | (uint32_t)fsresp[25] << 8 |
                     (uint32_t)fsresp[26] << 16 | (uint32_t)fsresp[27] << 24);
}
static const uint8_t *rep_body(void) {
    sched_wasi_state *w = wctx();
    return w ? w->fsresp + 28 : nullptr;
}
/* fresh body bytes actually received by the last roundtrip */
static uint32_t rep_body_len(int r) {
    return r > 28 ? (uint32_t)r - 28 : 0;
}

int32_t fsimport_alloc_fd(void) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w)
        return -1;
    if (w->fds[FSIMPORT_PREOPEN_FD] == SCHED_FD_EMPTY)
        w->fds[FSIMPORT_PREOPEN_FD] = FSIMPORT_FD_PREOPEN_ROOT;
    for (int fd = FSIMPORT_PREOPEN_FD + 1; fd < SCHED_MAX_FDS; fd++)
        if (w->fds[fd] == SCHED_FD_EMPTY) {
            w->fds[fd] = 0x7FFFFFFF; /* reserve */
            return fd;
        }
    return -1;
}

int32_t fsimport_fd_is_open(int32_t fd) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < FSIMPORT_PREOPEN_FD || fd >= SCHED_MAX_FDS ||
        w->fds[fd] == SCHED_FD_EMPTY)
        return 0;
    return rfd_of(fd) != 0;
}

void fsimport_release_fd(int32_t fd) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < FSIMPORT_PREOPEN_FD || fd >= SCHED_MAX_FDS)
        return;
    rfd_clear(sched_current_sid(), fd);
    w->fds[fd] = SCHED_FD_EMPTY;
}

int32_t fsimport_rebase_path(char *path, uint32_t capacity, int32_t dirfd) {
    if (path[0] == '/' || path[0] == 0 || dirfd != FSIMPORT_PREOPEN_FD)
        return FSIMPORT_ESUCCESS;
    uint32_t length = (uint32_t)strlen(path);
    if (length + 2 > capacity)
        return FSIMPORT_ENAMETOOLONG;
    for (uint32_t i = length + 1; i > 0; i--)
        path[i] = path[i - 1];
    path[0] = '/';
    return FSIMPORT_ESUCCESS;
}

int32_t fsimport_path_open(const char *path, int32_t oflags, int32_t *out_fd) {
    int32_t fd = fsimport_alloc_fd();
    if (fd < 0)
        return FSIMPORT_EBADF;
    uint32_t plen = (uint32_t)strlen(path);

    /* O_CREAT: create-or-truncate first (server CREATE semantics) */
    if (oflags & 1) {
        uint32_t rl = mk_req(FSOP_CREATE, path, plen, 0, 0);
        int r = fs_roundtrip(rl);
        if (r < 28) {
            fsimport_release_fd(fd);
            return r < 0 ? FSIMPORT_ENOSYS : FSIMPORT_EIO;
        }
        int32_t st = rep_status();
        if (st != 0) {
            fsimport_release_fd(fd);
            return fsimport_errno_for_status(st);
        }
    }

    /* verify existence via STAT */
    uint32_t rl = mk_req(FSOP_STAT, path, plen, 0, 0);
    int r = fs_roundtrip(rl);
    if (r < 28 || rep_status() != 0) {
        int32_t st = r < 28 ? FS_ST_IO : rep_status();
        fsimport_release_fd(fd);
        return fsimport_errno_for_status(st);
    }

    uint32_t sid = sched_current_sid();
    if (sid >= MAX_SESSIONS) {
        fsimport_release_fd(fd);
        return FSIMPORT_ENOSYS;
    }
    rfd *e = &fdtab[sid][fd];
    e->used = true;
    e->off = 0;
    memcpy(e->path, path, plen + 1);
    if (out_fd)
        *out_fd = fd;
    return FSIMPORT_ESUCCESS;
}

int32_t fsimport_mkdir(const char *path) {
    uint32_t rl =
        mk_req(FSOP_MKDIR, path, (uint32_t)strlen(path), 0, 0);
    int r = fs_roundtrip(rl);
    if (r < 28)
        return FSIMPORT_ENOSYS;
    return fsimport_errno_for_status(rep_status());
}

int32_t routed_rw(int32_t fd, int write, uint8_t *buf, uint32_t len,
                  uint32_t *done) {
    sched_wasi_state *w = sched_wasi_current();
    if (!w || fd < FSIMPORT_PREOPEN_FD || fd >= SCHED_MAX_FDS ||
        w->fds[fd] == SCHED_FD_EMPTY)
        return FSIMPORT_EBADF;
    rfd *e = rfd_of(fd);
    if (!e)
        return FSIMPORT_EBADF;
    if (len > FSIMPORT_MAX_IO)
        len = FSIMPORT_MAX_IO; /* single datagram payload budget */

    /* tail payload after mk_req's {u16 pLen, path}: {u64 off, u16 cnt[,data]} */
    uint32_t plen = (uint32_t)strlen(e->path);
    uint8_t pay[10 + FSIMPORT_MAX_IO];
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
        return FSIMPORT_ENOSYS;
    int32_t st = rep_status();
    if (st != 0)
        return fsimport_errno_for_status(st);

    const uint8_t *body = rep_body();
    uint32_t blen = rep_body_len(r);
    if (write) {
        /* WRITE body: u32 newSize */
        if (blen >= 4)
            e->off += len;
        *done = len;
        return FSIMPORT_ESUCCESS;
    }
    /* READ body: {u16 got, data} -- F16 clamp on every bound */
    if (blen < 2)
        return FSIMPORT_EIO;
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
    return FSIMPORT_ESUCCESS;
}

int32_t fsimport_read(int32_t fd, uint8_t *buf, uint32_t len, uint32_t *done) {
    return routed_rw(fd, 0, buf, len, done);
}

int32_t fsimport_write(int32_t fd, const uint8_t *buf, uint32_t len,
                       uint32_t *done) {
    return routed_rw(fd, 1, const_cast<uint8_t *>(buf), len, done);
}
