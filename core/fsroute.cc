#include "fsroute.h"
#include "plat.h"
#include <stdio.h>

#define MAXPENDING 8
#define RESP_MAX 4096

struct pending {
    bool used;
    uint16_t seq;
    char name[16];
    bool done;
    int len;
    uint8_t resp[RESP_MAX];
};

static pending tab[MAXPENDING];

void fsroute_init(void) {
    for (int i = 0; i < MAXPENDING; i++)
        tab[i].used = false;
}

int fsroute_expect(uint16_t seq, const char *name) {
#ifdef HOST_BUILD
    fprintf(stderr, "[fsr] expect seq=%u name=%s tab=%p\n", seq, name,
            (void *)tab);
#endif
    for (int i = 0; i < MAXPENDING; i++) {
        if (!tab[i].used) {
            tab[i].used = true;
            tab[i].seq = seq;
            tab[i].done = false;
            tab[i].len = 0;
            int k = 0;
            for (; name[k] && k < 15; k++)
                tab[i].name[k] = name[k];
            tab[i].name[k] = 0;
            return 0;
        }
    }
    return -1;
}

int fsroute_pending_for(const char *name) {
    for (int i = 0; i < MAXPENDING; i++) {
        if (tab[i].used && !tab[i].done) {
            const char *n = name;
            int k = 0;
            while (tab[i].name[k] && n[k] && tab[i].name[k] == n[k])
                k++;
            if (tab[i].name[k] == 0 && n[k] == 0)
                return 1;
        }
    }
    return 0;
}

/* F23/F28: consume a datagram ONLY when it is THE awaited reply --
 * addressed to the waiting session's name AND echoing that call's seq
 * (canonical header bytes [2:4]). Anything else falls through untouched
 * so legitimate queue traffic survives a pending routed call. */
bool fsroute_intercept(const char *name, const uint8_t *data, uint32_t len) {
    if (!name || !data || len < 4)
        return false;
    uint16_t seq = (uint16_t)(data[2] | (data[3] << 8));
    for (int i = 0; i < MAXPENDING; i++) {
        if (!tab[i].used || tab[i].done)
            continue;
        const char *n = name;
        int k = 0;
        while (tab[i].name[k] && n[k] && tab[i].name[k] == n[k])
            k++;
        if (tab[i].name[k] != 0 || n[k] != 0)
            continue; /* not addressed to this waiter */
        if (tab[i].seq != seq)
            continue; /* not THIS call's reply (F23) */
        uint32_t cp = len < RESP_MAX ? len : RESP_MAX;
        for (uint32_t b = 0; b < cp; b++)
            tab[i].resp[b] = data[b];
        tab[i].len = (int)cp;
        tab[i].done = true;
        return true;
    }
    return false;
}

void fsroute_feed(const char *name, const uint8_t *data, uint32_t len) {
    (void)fsroute_intercept(name, data, len);
}

int fsroute_wait_budget(uint16_t seq, uint8_t *resp, uint32_t cap,
                        uint64_t spins) {
    extern void sched_yield_current(void);
    for (uint64_t s = 0; s < spins; s++) {
        for (int i = 0; i < MAXPENDING; i++) {
            if (tab[i].used && tab[i].done && tab[i].seq == seq) {
                int n = tab[i].len;
                if (n > (int)cap)
                    n = (int)cap;
                for (int b = 0; b < n; b++)
                    resp[b] = tab[i].resp[b];
                tab[i].used = false;
                return n;
            }
        }
        if (s < 8 || s % 100000ULL == 0) {
#ifdef HOST_BUILD
            fprintf(stderr, "[fsw] spin=%llu e0(u=%d d=%d seq=%u mine=%d)\n",
                    (unsigned long long)s, tab[0].used, tab[0].done,
                    tab[0].seq, tab[0].seq == seq);
#endif
        }
        sched_yield_current();
    }
    /* F-AUDIT-1: reclaim the slot on timeout, otherwise after MAXPENDING
     * timeouts every fsroute_expect returns -1 and all routed FS ops
     * fail with ENOSYS until reboot. */
    for (int i = 0; i < MAXPENDING; i++) {
        if (tab[i].used && tab[i].seq == seq) {
            tab[i].used = false;
            tab[i].done = false;
            break;
        }
    }
    return -1;
}

int fsroute_wait(uint16_t seq, uint8_t *resp, uint32_t cap) {
    return fsroute_wait_budget(seq, resp, cap, 100000000ULL);
}
