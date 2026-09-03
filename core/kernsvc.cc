/* Kernel-owned service endpoints (abi/ABI.md §7): "registry", "devman",
 * "power". Requests are §1 datagrams {u16 op, u16 seq, payload}; replies
 * carry the same seq and land on the sending port's queue. Capability
 * bits enforced here; every rejection is audited to serial (§10). */
#include "ports.h"
#include "lib.h"
#include "log.h"
#include "mm.h"
#include "sched.h"
#include "plat.h"
#include "vfio.h"
#include "pci.h"

extern "C" {
uint32_t preempt_quantum_us(void);
uint8_t preempt_is_on(void);
}

extern "C" {
int netwin_attach(void *runtime);
int netwin_attached(void);
int vmod_grow_session(void *runtime, uint32_t min_bytes);
}

#include "kernsvc.h"
#include "cap_table.h"

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
static uint32_t get32(const uint8_t *p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) |
           ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

namespace {
/* single kernel thread: file-scope reply route is safe */
char g_rname[17];
uint32_t g_from_sid;
int g_reply_h;
}

static void kernsvc_reply(const uint8_t *data, uint32_t len) {
    extern bool ports_enqueue_by_name(const char *, const void *, uint32_t);
    extern void ports_kernel_enqueue(uint32_t, int, const void *, uint32_t);
    if (g_rname[0]) {
        ports_enqueue_by_name(g_rname, data, len);
        return;
    }
    /* Direct mode (rname empty): §7 endpoints reply INLINE on the
     * sending handle -- mirrors abi/ABI.md §1 and the host model.
     * Without this every direct-mode RPC burns its full recv budget. */
    if (g_reply_h >= 0)
        ports_kernel_enqueue(g_from_sid, g_reply_h, data, len);
}

/* F18 (ABI §7): "Unknown op / insufficient bit => status -1, audited."
 * Every fall-through MUST answer so clients fail fast instead of spinning
 * their full recv budget on a lost reply. Canonical form. */
static void kernsvc_nack(uint16_t op, uint16_t seq) {
    uint8_t nb[28];
    put16(nb, op);
    put16(nb + 2, seq);
    put32(nb + 4, 0); /* kernel uid */
    put32(nb + 24, 0xFFFFFFFFu);
    ports_kernel_enqueue(g_from_sid, g_reply_h, nb, sizeof(nb));
}

// kernsvc_audit: emit an audit record to the log ring (console_puts
// feeds core/log.cc's ring buffer). Format:
//   [audit] sid=X op=<op> reason=<reason> target=<target>\n
void kernsvc_audit(uint32_t sid, const char *op, const char *reason,
                   const char *target) {
    console_puts("[audit] sid=");
    console_hex64(sid);
    console_puts(" op=");
    console_puts(op);
    console_puts(" reason=");
    console_puts(reason);
    console_puts(" target=");
    console_puts(target);
    console_puts("\n");
}

void kernsvc_dispatch(const char *epname, uint32_t from_sid, int reply_h,
                      const uint8_t *data, uint32_t len) {
    /* framing: {u16 op,u16 seq,u32 uid,char rname[16],payload} */
    if (len < 24)
        return;
    uint16_t op = get16(data);
    uint16_t seq = get16(data + 2);
    g_from_sid = from_sid;
    g_reply_h = reply_h; /* direct-mode replies ride the sending handle */
    for (int i = 0; i < 16; i++) {
        g_rname[i] = (char)data[8 + i];
    }
    g_rname[16] = 0;
    const uint8_t *payload = data + 24;
    uint32_t plen = len - 24;
    /* Suppress verbose debug for routine LIST/ENUM polls (op=1 on registry/devman):
     * init's sweep and console's devman queries fire these every few seconds,
     * drowning the serial log. Keep verbose traces for SPAWN/LOGIN/KILL/etc. */
    bool is_routine_poll = (op == 1 || op == 8 || op == 9);
    if (!is_routine_poll) {
        console_puts("[ksvc] ep=");
        console_puts(epname);
        console_puts(" op=");
        console_hex64(op);
        console_puts(" rname=");
        console_puts(g_rname);
        console_puts("\n");
    }

    /* All §7 replies are CANONICAL: {u16 op,u16 seq,u32 uid=0,
     * char rname[16], body@24}. guests/lib Registry/Devman/Power clients
     * parse status/body at 24 -- this is the ratified wire form. */
    static uint8_t rbuf[4096];
    auto kbegin = [&](uint16_t rop) {
        put16(rbuf, rop);
        put16(rbuf + 2, seq);
        put32(rbuf + 4, 0); /* kernel uid */
        for (int i = 0; i < 16; i++)
            rbuf[8 + i] = 0;
        return 24u;
    };
    auto knack = [&]() {
        uint32_t o = kbegin(op);
        put32(rbuf + o, 0xFFFFFFFFu);
        kernsvc_reply(rbuf, o + 4);
    };
    uint32_t rn = 0;

    if (!strcmp(epname, "registry")) {
        switch (op) {
        case 1: { /* LIST -> body {u32 n; rec[25]} at 24 */
            rn = kbegin(1);
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
            rn = kbegin(2);
            uint32_t n = 0;
            for (uint64_t b = 0; b < 10; b++)
                if (mask & (1ULL << b))
                    n++;
            put32(rbuf + rn, n);
            rn += 4;
            for (uint64_t b = 0; b < 10 && sid != 0xFFFFFFFFu; b++) {
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
            rn = kbegin(3);
            put32(rbuf + rn, (uint32_t)rc);
            kernsvc_reply(rbuf, rn + 4);
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
                knack();
                return;
            }
            char modname[17];
            int j = 0;
            for (; j < 16 && payload[j]; j++)
                modname[j] = (char)payload[j];
            modname[j] = 0;
            /* Extract argv from the SPAWN payload (F27).
             * Layout: [16..79] path, [80..83] capmask, [86..87] argc,
             * [88..] argv strings (NUL-separated).
             * Must copy into persistent memory: the payload buffer is
             * freed after this handler returns (F42). */
            const char *argv[8] = {0};
            int argc = 0;
            int argv_total = 0;
            if (plen >= 88) {
                uint16_t na = get16(payload + 86);
                const char *ap = (const char *)payload + 88;
                const char *end = (const char *)payload + plen;
                for (int i = 0; i < (int)na && i < 6; i++) {
                    int sl = 0;
                    while (ap + sl < end && ap[sl] != 0)
                        sl++;
                    argv_total += sl + 1;
                    ap += sl + 1;
                    argc++;
                }
            }
            char *argv_store = (char *)mm_alloc(argv_total + 16, 1);
            if (!argv_store) {
                knack();
                return;
            }
            /* argv[0] = program name (modname) */
            int k = 0;
            for (; modname[k] && k < 15; k++)
                argv_store[k] = modname[k];
            argv_store[k] = 0;
            argv[0] = argv_store;
            argc = 1;
            if (plen >= 88 && argv_store) {
                uint16_t na = get16(payload + 86);
                const char *ap = (const char *)payload + 88;
                const char *end = (const char *)payload + plen;
                for (int i = 0; i < (int)na && argc < 7; i++) {
                    int sl = 0;
                    while (ap + sl < end && ap[sl] != 0)
                        sl++;
                    if (k + 1 + sl + 1 > (int)argv_total + 16)
                        break;
                    memcpy(argv_store + k + 1, ap, sl);
                    argv_store[k + 1 + sl] = 0;
                    argv[argc++] = argv_store + k + 1;
                    k += sl + 1;
                    ap += sl + 1;
                }
            }
            /* name of new session = module name (v1) */
            int nsid = sched_spawn_image(modname, sched_uid_of(from_sid), want,
                                         modname, argv, argc);
            if (nsid > 0 && !strcmp(modname, "net")) {
                /* net needs room for §6 windows at 64 MiB offset */
                vmod_grow_session(sched_runtime_of((uint32_t)nsid),
                                  68 * 1024 * 1024);
            }
            if (nsid > 0 && !strcmp(modname, "p9")) {
                /* driver needs Go heap+stack headroom */
                vmod_grow_session(sched_runtime_of((uint32_t)nsid),
                                  32 * 1024 * 1024);
            }
            if (nsid > 0 && !strcmp(modname, "shell")) {
                /* shell needs headroom for Go runtime + pkg crypto */
                vmod_grow_session(sched_runtime_of((uint32_t)nsid),
                                  16 * 1024 * 1024);
            }
            if (nsid > 0 && !strcmp(modname, "net")) {
                /* §6 windows live in the net session's linear memory */
                netwin_attach(sched_runtime_of((uint32_t)nsid));
            }
            rn = kbegin(4);
            put32(rbuf + rn, (uint32_t)nsid);
            kernsvc_reply(rbuf, rn + 4);
            return;
        }
        case 5: { /* LOGIN {char name[16], u32 uid, u32 capmask}
                     callable ONLY by the owner of the "login" port (v1.1) */
            if (!sched_is_login(from_sid)) {
                console_puts("[audit] sid=");
                console_hex64(from_sid);
                console_puts(" op=LOGIN reason=cap target=registry\n");
                knack();
                return;
            }
            char tname[17];
            int q = 0;
            for (; q < 16 && payload[q]; q++)
                tname[q] = (char)payload[q];
            tname[q] = 0;
            uint32_t nuid = payload[16] | (payload[17] << 8) |
                            (payload[18] << 16) | ((uint32_t)payload[19] << 24);
            uint32_t nmask = payload[20] | (payload[21] << 8) |
                             (payload[22] << 16) | ((uint32_t)payload[23] << 24);
            /* Minting check: caller must hold the caps it's about to grant,
             * unless the caller is init (which is trusted to mint any mask). */
            {
                uint64_t caller_caps = sched_capmask_of(from_sid);
                uint64_t nmask64 = (uint64_t)nmask;
                if ((nmask64 & ~caller_caps) != 0 && !sched_is_init(from_sid)) {
                    kernsvc_audit(from_sid, "LOGIN", "cap", "registry");
                    knack();
                    return;
                }
            }
            int tsid = sched_session_by_name(tname);
            console_puts("[ksvc] LOGIN target='");
            console_puts(tname);
            console_puts("' tsid=");
            console_hex64((uint64_t)(int64_t)tsid);
            console_puts("\n");
            if (tsid < 0) {
                knack();
                return;
            }
            sched_set_identity((uint32_t)tsid, nuid, nmask);
            rn = kbegin(5);
            put32(rbuf + rn, 0);
            kernsvc_reply(rbuf, rn + 4);
            return;
        }
        case 6: { /* SETCONF {char key[16], u64 value} -- CAP_CONF only */
            if (!(sched_capmask_of(from_sid) & SCHED_CAP_CONF)) {
                console_puts("[audit] sid=");
                console_hex64(from_sid);
                console_puts(" op=SETCONF reason=cap target=registry\n");
                knack();
                return;
            }
            char cfgkey[17];
            int q3 = 0;
            for (; q3 < 16 && payload[q3]; q3++)
                cfgkey[q3] = (char)payload[q3];
            cfgkey[q3] = 0;
            uint64_t val = 0;
            for (int b = 7; b >= 0; b--)
                val = (val << 8) | payload[16 + b];
            console_puts("[conf] ");
            console_puts(cfgkey);
            console_puts("=");
            console_hex64(val);
            console_puts("\n");
            rn = kbegin(6);
            put32(rbuf + rn, 0);
            kernsvc_reply(rbuf, rn + 4);
            return;
        }
        case 7: { /* ASSIGN_PCI {u8 bus,u8 dev,u8 fn,u32 target_sid} -- CAP_DEVMAN */
            if (!(sched_capmask_of(from_sid) & SCHED_CAP_DEVMAN)) {
                console_puts("[audit] sid=");
                console_hex64(from_sid);
                console_puts(" op=ASSIGN_PCI reason=cap target=registry\n");
                knack();
                return;
            }
            if (plen < 7) { knack(); return; }
            uint8_t bus = payload[0];
            uint8_t dev = payload[1];
            uint8_t fn = payload[2];
            uint32_t tgt = payload[3] | (payload[4] << 8) | (payload[5] << 16) | (payload[6] << 24);
            int rc = vfio_assign_pci(tgt, bus, dev, fn, from_sid);
            rn = kbegin(7);
            put32(rbuf + rn, (uint32_t)rc);
            kernsvc_reply(rbuf, rn + 4);
            return;
        }
        case 8: { /* SYSSTAT (v2.1, Phase 15) -> {u32 status=0,
                   * u64 mem_total, u64 mem_used, u32 quantum_us,
                   * u8 preempt_on, u32 ncpus}. Read-only observability
                   * for top/memstat; no capability required (v1). */
            rn = kbegin(8);
            put32(rbuf + rn, 0);
            rn += 4;
            uint64_t mtot = mm_total_bytes(), mused = mm_used_bytes();
            for (int b = 0; b < 8; b++) {
                rbuf[rn + b] = (uint8_t)(mtot >> (8 * b));
                rbuf[rn + 8 + b] = (uint8_t)(mused >> (8 * b));
            }
            rn += 16;
            put32(rbuf + rn, preempt_quantum_us());
            rn += 4;
            rbuf[rn++] = preempt_is_on();
            put32(rbuf + rn, (uint32_t)sched_ncpus());
            rn += 4;
            kernsvc_reply(rbuf, rn);
            return;
        }
        case 9: { /* LOGDUMP (v2.1, Phase 15) {u64 off} ->
                   * {u32 status=0, u64 total, u64 begin, bytes...}.
                   * Read-only v1 syslog for dmesg/audit; no capability
                   * required (v1; Phase 10 may gate to self-or-admin). */
            uint64_t off = 0;
            if (plen >= 8)
                for (int b = 0; b < 8; b++)
                    off |= (uint64_t)payload[b] << (8 * b);
            rn = kbegin(9);
            put32(rbuf + rn, 0);
            rn += 4;
            uint64_t total = 0, begin = 0;
            /* rbuf is 4096: header 24 + 20 fixed + chunk <= 4052. */
            uint32_t got = log_read(off, rbuf + rn + 20, 4000, &total, &begin);
            for (int b = 0; b < 8; b++) {
                rbuf[rn + b] = (uint8_t)(total >> (8 * b));
                rbuf[rn + 8 + b] = (uint8_t)(begin >> (8 * b));
            }
            rn += 20;
            kernsvc_reply(rbuf, rn + got);
            return;
        }
        case 10: { /* CHCAPS {u32 sid, u32 clear, u32 set} -- CAP_POWER only.
                     * Adds or removes capability bits on a live session.
                     * Clears bits first (clear mask), then sets (set mask).
                     * Caller must hold CAP_POWER. Self-modification of
                     * non-admin caps is denied (ABI §7 hardening). */
            if (!(sched_capmask_of(from_sid) & SCHED_CAP_POWER)) {
                console_puts("[audit] sid=");
                console_hex64(from_sid);
                console_puts(" op=CHCAPS reason=cap target=registry\n");
                knack();
                return;
            }
            if (plen < 12) {
                knack();
                return;
            }
            uint32_t tsid = get32(payload + 0);
            uint32_t clear = get32(payload + 4);
            uint32_t set = get32(payload + 8);
            int rc = sched_set_capmask(tsid, clear, set);
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=CHCAPS target=");
            console_hex64(tsid);
            console_puts(" clear=0x");
            console_hex64(clear);
            console_puts(" set=0x");
            console_hex64(set);
            console_puts(" rc=");
            console_hex64(rc);
            console_puts("\n");
            rn = kbegin(10);
            put32(rbuf + rn, (uint32_t)rc);
            kernsvc_reply(rbuf, rn + 4);
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
        kernsvc_nack(op, seq); /* F18: unknown/cap-denied must answer */
        return;
    }

    if (!strcmp(epname, "devman")) {
        if (!(sched_capmask_of(from_sid) & SCHED_CAP_DEVMAN)) {
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=ENUM reason=cap target=devman\n");
            kernsvc_nack(op, seq); /* F18 */
            return;
        }
        if (op == 1) { /* ENUM -> body {u32 n; rec{class,inst,u64 win}} @24 */
            rn = kbegin(1);
            uint32_t n = 1;
            uint32_t o = rn + 4;
            /* console class window (always present) */
            put32(rbuf + o, 5);
            put32(rbuf + o + 4, 0);
            put32(rbuf + o + 8, 0xF000);
            put32(rbuf + o + 12, 0);
            o += 16;
            /* the net session additionally sees its §6 RX/TX windows */
            if (!strcmp(sched_name_of(from_sid), "net")) {

                if (netwin_attached()) {
                    put32(rbuf + o, 2); /* class net */
                    put32(rbuf + o + 4, 0); /* inst 0 = RX */
                    put32(rbuf + o + 8, 0x4000000);
                    put32(rbuf + o + 12, 0);
                    o += 16;
                    put32(rbuf + o, 2);
                    put32(rbuf + o + 4, 1); /* inst 1 = TX */
                    put32(rbuf + o + 8, 0x4000000 + (393224));
                    put32(rbuf + o + 12, 0);
                    o += 16;
                    n += 2;
                }
            }
            /* PCI passthrough devices (class 10) — VFIO §12, enumerated for any DEVMAN holder */
            {
                struct vfio_pci_info infos[16];
                int pc = vfio_enumerate(infos, 16);
                for (int i = 0; i < pc && n < 16; i++) {
                    put32(rbuf + o, 10); /* class PCI */
                    /* Encode BDF into inst: bus<<16 | dev<<8 | fn */
                    uint32_t bdf = ((uint32_t)infos[i].bus << 16) |
                                   ((uint32_t)infos[i].dev << 8) |
                                   ((uint32_t)infos[i].fn);
                    put32(rbuf + o + 4, bdf);
                    put32(rbuf + o + 8, ((uint32_t)infos[i].vendor << 16) | infos[i].device);
                    put32(rbuf + o + 12, 0);
                    o += 16; n++;
                }
            }
            put32(rbuf + rn, n);
            kernsvc_reply(rbuf, o);
        } else {
            console_puts("[audit] sid=");
            console_hex64(from_sid);
            console_puts(" op=? reason=op target=devman\n");
            kernsvc_nack(op, seq); /* F18 */
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
            kernsvc_nack(op, seq); /* F18 */
            return;
        }
        if (op == 1 || op == 2) {
            console_puts("[power] ");
            console_puts(op == 1 ? "reboot" : "off");
            console_puts(" requested; halting (no ACPI in v1)\n");
            rn = kbegin(op);
            put32(rbuf + rn, 0);
            kernsvc_reply(rbuf, rn + 4);
            cpu_halt();
        }
        kernsvc_nack(op, seq); /* F18: unknown power op */
        return;
    }
}


