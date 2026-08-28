#include "ports.h"
#include "lib.h"
#include "sched.h"
#include "plat.h"
#include "rt.h"
#include "fsroute.h"

static constexpr int MAX_PORTS = 24;
static constexpr int MAX_Q = 32;
static constexpr int MSG_MAX = 4096;
static constexpr int H_PER_SESS = 16;

struct msg {
    uint32_t from_sid;
    uint16_t len;
    uint8_t data[MSG_MAX];
};

struct port {
    bool used;
    bool kernel_endpoint;
    uint32_t creator;
    char name[16];
    uint32_t qn, qh, qt; /* count, head, tail over ring */
    msg *ring[MAX_Q];
};

static port ports[MAX_PORTS];
/* per-session handle table: -1 empty, else global port index */
static int8_t htab[12][H_PER_SESS];

void ports_init(void) {
    for (int p = 0; p < MAX_PORTS; p++)
        ports[p].used = false;
    for (int s = 0; s < 12; s++)
        for (int h = 0; h < H_PER_SESS; h++)
            htab[s][h] = -1;

    /* kernel-owned §7 endpoints (owner sid 0) */
    const char *knames[] = {"registry", "devman", "power"};
    for (int k = 0; k < 3; k++) {
        for (int p = 0; p < MAX_PORTS; p++) {
            if (ports[p].used)
                continue;
            int j = 0;
            for (; knames[k][j]; j++)
                ports[p].name[j] = knames[k][j];
            ports[p].name[j] = 0;
            ports[p].used = true;
            ports[p].kernel_endpoint = true;
            ports[p].creator = 0;
            ports[p].qn = ports[p].qh = ports[p].qt = 0;
            break;
        }
    }
}

static port *port_of(uint32_t sid, int h) {
    if (sid >= 12 || h < 0 || h >= H_PER_SESS)
        return 0;
    int8_t g = htab[sid][h];
    if (g < 0 || g >= MAX_PORTS || !ports[g].used)
        return 0;
    return &ports[g];
}

static bool name_eq(const char *a, uint32_t alen, const char *b) {
    uint32_t i = 0;
    for (; i < alen && b[i]; i++)
        if (a[i] != b[i])
            return false;
    return i == alen && b[i] == 0;
}

int port_create(uint32_t sid, const char *name, uint32_t name_len) {
    if (!name_len || name_len > 15 || sid == 0 || !sched_alive(sid))
        return -1;
    for (int p = 0; p < MAX_PORTS; p++)
        if (ports[p].used && name_eq(name, name_len, ports[p].name))
            return -1; /* one owner per name */
    for (int p = 0; p < MAX_PORTS; p++) {
        if (ports[p].used)
            continue;
        for (uint32_t i = 0; i < name_len; i++)
            ports[p].name[i] = name[i];
        ports[p].name[name_len] = 0;
        ports[p].used = true;
        ports[p].kernel_endpoint = false;
        ports[p].creator = sid;
        ports[p].qn = ports[p].qh = ports[p].qt = 0;
        for (int h = 0; h < H_PER_SESS; h++) {
            if (htab[sid][h] < 0) {
                htab[sid][h] = (int8_t)p;
                return h;
            }
        }
        ports[p].used = false;
        return -1;
    }
    return -1;
}

int port_bind(uint32_t sid, const char *name, uint32_t name_len) {
    if (sid == 0 || !sched_alive(sid)) {
        console_puts("[bind] dead caller\n");
        return -1;
    }
    bool any = false;
    for (int p = 0; p < MAX_PORTS; p++)
        if (ports[p].used && name_eq(name, name_len, ports[p].name))
            any = true;
    if (!any) {
        static int once;
        if (!once++) {
            console_puts("[bind] no such port: ");
            for (uint32_t i = 0; i < name_len && i < 15; i++)
                console_putc(name[i]);
            console_puts("\n");
        }
    }
    for (int p = 0; p < MAX_PORTS; p++) {
        if (ports[p].used && name_eq(name, name_len, ports[p].name)) {
            for (int h = 0; h < H_PER_SESS; h++) {
                if (htab[sid][h] < 0) {
                    htab[sid][h] = (int8_t)p;
                    return h;
                }
            }
            console_puts("[bind] table full sid=");
            console_hex64(sid);
            console_puts("\n");
            return -1;
        }
    }
    return -1;
}

/* ---- §7 kernel endpoint dispatch ---- */
extern void kernsvc_dispatch(const char *epname, uint32_t from_sid,
                             int reply_h, const uint8_t *data, uint32_t len);

int port_send(uint32_t sid, int h, const void *data, const uint32_t len) {
    port *p = port_of(sid, h);
    if (!p || !data || len == 0 || len > MSG_MAX)
        return -1;
    /* F32 / ABI v1.1 §1: "On SEND the kernel OVERWRITES uid with the
     * sending session's registry uid -- clients can never spoof identity."
     * Stamped on every path: kernel-endpoint dispatch, fsroute feed and
     * queue enqueue alike. Canonical header holds uid at bytes [4:8]. */
    if (len >= 8 && sid != 0) {
        uint32_t uid = sched_uid_of(sid);
        ((uint8_t *)data)[4] = (uint8_t)uid;
        ((uint8_t *)data)[5] = (uint8_t)(uid >> 8);
        ((uint8_t *)data)[6] = (uint8_t)(uid >> 16);
        ((uint8_t *)data)[7] = (uint8_t)(uid >> 24);
    }
    if (p->kernel_endpoint && sid != 0) {
        kernsvc_dispatch(p->name, sid, h, (const uint8_t *)data, len);
        return 0;
    }
    /* F28: intercept ONLY the awaited reply (name+seq match); anything
     * else -- console relays, §7 sync replies, unrelated traffic -- keeps
     * flowing to its queue. */
    if (fsroute_intercept(p->name, (const uint8_t *)data, len))
        return 0;
    if (p->qn >= MAX_Q)
        return -2; /* would-block */
    msg *m = (msg *)rt_malloc(sizeof(msg));
    if (!m)
        return -1;
    m->from_sid = sid;
    m->len = (uint16_t)len;
    const uint8_t *src = (const uint8_t *)data;
    for (uint32_t i = 0; i < len; i++)
        m->data[i] = src[i];
    p->ring[p->qt] = m;
    p->qt = (p->qt + 1) % MAX_Q;
    p->qn++;
    return 0;
}

extern "C" int ports_owner_of_handle(uint32_t sid, int h) {
    if (sid >= 12 || h < 0 || h >= H_PER_SESS)
        return -1;
    int8_t g = htab[sid][h];
    if (g < 0 || g >= MAX_PORTS || !ports[g].used)
        return -1;
    return (int)ports[g].creator; /* session that CREATED the port */
}

extern "C" bool ports_name_owned_by(uint32_t sid, const char *name) {
    /* F13: ownership means CREATOR, not binder. port_bind hands out
     * handles to existing names freely (legal fan-in); only the session
     * that created the port owns the well-known name. Kernel endpoints
     * are creator 0 and owned by no session. */
    if (sid >= 12 || sid == 0)
        return false;
    for (int h = 0; h < H_PER_SESS; h++) {
        int8_t g = htab[sid][h];
        if (g >= 0 && g < MAX_PORTS && ports[g].used &&
            ports[g].creator == sid && !strcmp(ports[g].name, name))
            return true;
    }
    return false;
}

extern "C" bool ports_enqueue_by_name(const char *name, const void *data,
                                      uint32_t len) {
    /* F28: same conditional interception as port_send -- a pending routed
     * call must not swallow unrelated kernel-side traffic. */
    if (fsroute_intercept(name, (const uint8_t *)data, len))
        return true;
    for (int p = 0; p < MAX_PORTS; p++) {
        if (ports[p].used && !strcmp(ports[p].name, name)) {
            if (ports[p].qn >= MAX_Q)
                return false;
            msg *m = (msg *)rt_malloc(sizeof(msg));
            if (!m)
                return false;
            m->from_sid = 0;
            m->len = (uint16_t)(len > MSG_MAX ? MSG_MAX : len);
            const uint8_t *src = (const uint8_t *)data;
            for (uint32_t i = 0; i < m->len; i++)
                m->data[i] = src[i];
            ports[p].ring[ports[p].qt] = m;
            ports[p].qt = (ports[p].qt + 1) % MAX_Q;
            ports[p].qn++;
            return true;
        }
    }
    return false;
}

/* kernel-side direct enqueue (replies): bypasses endpoint dispatch */
void ports_kernel_enqueue(uint32_t sid, int h, const void *data, uint32_t len) {
    port *p = port_of(sid, h);
    if (!p || !data || len == 0 || len > MSG_MAX)
        return;
    if (p->qn >= MAX_Q)
        return; /* drop */
    msg *m = (msg *)rt_malloc(sizeof(msg));
    if (!m)
        return;
    m->from_sid = sid;
    m->len = (uint16_t)len;
    const uint8_t *src = (const uint8_t *)data;
    for (uint32_t i = 0; i < len; i++)
        m->data[i] = src[i];
    p->ring[p->qt] = m;
    p->qt = (p->qt + 1) % MAX_Q;
    p->qn++;
}

int port_recv(uint32_t sid, int h, void *out, uint32_t cap) {
    port *p = port_of(sid, h);
    if (!p || !out)
        return -1;
    if (p->qn == 0)
        return 0;
    msg *m = p->ring[p->qh];
    if (!m || p->qn == 0)
        return 0;
    p->qn--;
    p->qh = (p->qh + 1) % MAX_Q;
    uint32_t n = m->len <= cap ? m->len : cap;
    uint8_t *dst = (uint8_t *)out;
    for (uint32_t i = 0; i < n; i++)
        dst[i] = m->data[i];
    rt_free(m);
    return (int)n;
}
