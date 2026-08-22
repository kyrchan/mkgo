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

void fsroute_feed(const char *name, const uint8_t *data, uint32_t len) {
#ifdef HOST_BUILD
    fprintf(stderr, "[fsr] feed name=%s len=%u tab=%p\n", name, len,
            (void *)tab);
#endif
    for (int i = 0; i < MAXPENDING; i++) {
        if (!tab[i].used || tab[i].done)
            continue;
        /* match by session-name queue + reply op echo */
        const char *n = name;
        int k = 0;
        while (tab[i].name[k] && n[k] && tab[i].name[k] == n[k])
            k++;
        if (tab[i].name[k] != 0 || n[k] != 0)
            continue;
#ifdef HOST_BUILD
        fprintf(stderr, "[fsr] feed match i=%d seq=%u len=%u\n", i,
                tab[i].seq, len);
#endif
        if (len < 2)
            continue;
        uint32_t cp = len < RESP_MAX ? len : RESP_MAX;
        for (uint32_t b = 0; b < cp; b++)
            tab[i].resp[b] = data[b];
        tab[i].len = (int)cp;
        tab[i].done = true;
        return;
    }
}

int fsroute_wait(uint16_t seq, uint8_t *resp, uint32_t cap) {
    extern void sched_yield_current(void);
#ifdef HOST_BUILD
    fprintf(stderr, "[fsw] waiting seq=%u tab=%p\n", seq, (void *)tab);
#endif
    for (uint64_t spins = 0; spins < 100000000ULL; spins++) {
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
        if (spins < 8 || spins % 100000ULL == 0) {
#ifdef HOST_BUILD
            fprintf(stderr, "[fsw] spin=%llu e0(u=%d d=%d seq=%u mine=%d)\n",
                    (unsigned long long)spins, tab[0].used, tab[0].done,
                    tab[0].seq, tab[0].seq == seq);
#endif
        }
        sched_yield_current();
    }
    return -1;
}
