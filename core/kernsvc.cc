/* Kernel-owned service endpoints (abi/ABI.md §7): "registry", "devman",
 * "power". Requests are §1 datagrams {u16 op, u16 seq, payload}; replies
 * carry the same seq and land on the sending port's queue. Capability
 * bits enforced here; every rejection is audited to serial (§10). */
#include "ports.h"
#include "lib.h"
#include "sched.h"
#include "plat.h"

static void put16(uint8_t *p, uint16_t v) {
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
}
static uint16_t get16(const uint8_t *p) {
    return (uint16_t)(p[0] | (p[1] << 8));
}
static void put32(uint8_t *p, uint32_t v) {
    for (int i = 0; i < 4; i++)
        p[i] = (uint8_t)(v >> (8 * i));
}

namespace {
/* single kernel thread: file-scope reply route is safe */
struct reply_route { uint32_t sid; int h; };
reply_route g_rt;
}

static void kernsvc_reply(const uint8_t *data, uint32_t len) {
    extern void ports_kernel_enqueue(uint32_t sid, int h, const void *data,
                                     uint32_t len);
    ports_kernel_enqueue(g_rt.sid, g_rt.h, data, len);
}

void kernsvc_dispatch(const char *epname, uint32_t from_sid, int reply_h,
                      const uint8_t *data, uint32_t len) {
    g_rt.sid = from_sid;
    g_rt.h = reply_h;
    if (len < 4)
        return;
    uint16_t op = get16(data);
    uint16_t seq = get16(data + 2);
    const uint8_t *payload = data + 4;
    uint32_t plen = len - 4;

    static uint8_t rbuf[4096];
    uint32_t rn = 0;

    if (!strcmp(epname, "registry")) {
        switch (op) {
        case 1: { /* LIST */
            put16(rbuf, 1);
            put16(rbuf + 2, seq);
            rn = 4;
            char names[12][16];
            uint32_t recs[12 * 3];
            uint32_t n = sched_list(recs, names, 12);
            put32(rbuf + rn, n);
            rn += 4;
            for (uint32_t i = 0; i < n; i++) {
                put32(rbuf + rn, recs[i * 3 + 0]); /* sid */
                put32(rbuf + rn + 4, recs[i * 3 + 1]); /* uid */
                rbuf[rn + 8] = (uint8_t)recs[i * 3 + 2]; /* state */
                for (int k = 0; k < 16; k++)
                    rbuf[rn + 9 + k] = names[i][k];
                rn += 25;
            }
            kernsvc_reply(rbuf, rn);
            return;
        }
        case 2: { /* CAPS {u32 sid} -> {u32 n; rec{u32 cap_id,u64 rights}} */
            uint32_t sid = plen >= 4 ? (uint32_t)(payload[0] | (payload[1] << 8) |
                                                  (payload[2] << 16) |
                                                  ((uint32_t)payload[3] << 24)) : 0xFFFFFFFFu;
            uint64_t mask = sid != 0xFFFFFFFFu ? sched_capmask_of(sid) : 0;
            put16(rbuf, 2);
            put16(rbuf + 2, seq);
            rn = 4;
            uint32_t n = 0;
            for (uint64_t b = 0; b < 7; b++)
                if (mask & (1ULL << b))
                    n++;
            put32(rbuf + rn, n);
            rn += 4;
            for (uint64_t b = 0; b < 7 && sid != 0xFFFFFFFFu; b++) {
                if (mask & (1ULL << b)) {
                    put32(rbuf + rn, (uint32_t)b);
                    put32(rbuf + rn + 4, (uint32_t)(1ULL << b));
                    put32(rbuf + rn + 8, (uint32_t)((1ULL << b) >> 32));
                    rn += 12;
                }
            }
            kernsvc_reply(rbuf, rn);
            return;
        }
        case 3: { /* KILL {u32 sid} */
            uint32_t sid = payload[0] | (payload[1] << 8) | (payload[2] << 16) |
                           ((uint32_t)payload[3] << 24);
            int rc = sched_kill(sid); /* checks CAP_KILL itself */
            put16(rbuf, 3);
            put16(rbuf + 2, seq);
            put32(rbuf + 4, (uint32_t)rc);
            kernsvc_reply(rbuf, 8);
            return;
        }
        case 4: { /* SPAWN {char name[16], char path[64], u32 capmask,
                     u16 argc; args bytes} -- v1: path ignored, module
                     resolved from the preloaded /boot/modules table */
            if (!(sched_capmask_of(from_sid) & SCHED_CAP_SPAWN))
                break;
            uint64_t caller = sched_capmask_of(from_sid);
            uint64_t want = payload[80] | (payload[81] << 8) |
                            (payload[82] << 16) | ((uint32_t)payload[83] << 24);
            if ((want & ~caller) != 0) {
                console_puts("[audit] sid=");
                console_hex64(from_sid);
                console_puts(" op=SPAWN reason=cap target=registry\n");
                put16(rbuf, 4);
                put16(rbuf + 2, seq);
                put32(rbuf + 4, 0xFFFFFFFFu);
                kernsvc_reply(rbuf, 8);
                return;
            }
            char modname[17];
            int j = 0;
            for (; j < 16 && payload[j]; j++)
                modname[j] = (char)payload[j];
            modname[j] = 0;
            /* name of new session = module name (v1) */
            int nsid = sched_spawn_image(modname, sched_uid_of(from_sid), want,
                                         modname);
            put16(rbuf, 4);
            put16(rbuf + 2, seq);
            put32(rbuf + 4, (uint32_t)nsid);
            kernsvc_reply(rbuf, 8);
            return;
        }
        default:
            break;
        }
        console_puts("[audit] sid=");
        console_hex64(from_sid);
        console_puts(" uid=");
        console_hex64(sched_uid_of(from_sid));
        console_puts(" op=");
        console_hex64(op);
        console_puts(" reason=op target=registry\n");
        return;
    }

    if (!strcmp(epname, "devman")) {
        if (!(sched_capmask_of(from_sid) & SCHED_CAP_DEVMAN)) {
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=ENUM reason=cap target=devman\n");
            return;
        }
        if (op == 1) { /* ENUM -> one record: console class window */
            put16(rbuf, 1);
            put16(rbuf + 2, seq);
            put32(rbuf + 4, 1); /* one device */
            put32(rbuf + 8, 5); /* class console */
            put32(rbuf + 12, 0); /* inst */
            put32(rbuf + 16, 0);
            put32(rbuf + 20, 0xF000); /* win_off low */
            put32(rbuf + 24, 0);
            kernsvc_reply(rbuf, 28);
        } else {
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=? reason=op target=devman\n");
        }
        return;
    }

    if (!strcmp(epname, "power")) {
        if (!(sched_capmask_of(from_sid) & SCHED_CAP_POWER)) {
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=");
            console_hex64(op);
            console_puts(" reason=cap target=power\n");
            return;
        }
        if (op == 1 || op == 2) {
            console_puts("[power] ");
            console_puts(op == 1 ? "reboot" : "off");
            console_puts(" requested; halting (no ACPI in v1)\n");
            put16(rbuf, op);
            put16(rbuf + 2, seq);
            put32(rbuf + 4, 0);
            kernsvc_reply(rbuf, 8);
            cpu_halt();
        }
        return;
    }
}


