/* Input subsystem (abi/ABI.md §4): serial bytes become key-down records
 * delivered ONLY to the focused session. Focus is a kernel attribute
 * moved via kern_focus_set(port_handle) by FOCUS-capable sessions. */
#include "input.h"
#include "plat.h"
#include "sched.h"
#include "ports.h"

#define QLEN 256

static struct {
    uint8_t kind[QLEN];
    uint8_t mods[QLEN];
    uint16_t cp[QLEN];
    int qh, qt, qn;
} inq;

static int32_t focus_sid = -1;

void input_init(void) {
    inq.qh = inq.qt = inq.qn = 0;
    focus_sid = -1;
}

/* map a console byte to an input record */
static void push_byte(uint8_t b) {
    if (inq.qn >= QLEN)
        return; /* drop when full */
    uint16_t cp = b;
    if (b == '\r')
        cp = '\n';
    inq.kind[inq.qt] = 1; /* key_down */
    inq.mods[inq.qt] = 0;
    inq.cp[inq.qt] = cp;
    inq.qt = (inq.qt + 1) % QLEN;
    inq.qn++;
}

void input_poll(void) {
    static int total;
    for (int n = 0; n < 64 && console_rx_ready(); n++) {
        uint8_t b = (uint8_t)console_rx_byte();
        if (total % 8 == 0) {
            console_puts("[in] #");
            console_hex64(total);
            console_puts(" f=");
            console_hex64((uint64_t)(int64_t)focus_sid);
            console_puts("\n");
        }
        total++;
        push_byte(b);
    }
}

int input_recv(uint32_t sid, void *out, uint32_t cap) {
    if (!out || cap < 4)
        return 0;
    static int dbg;
    if (dbg++ < 3 && inq.qn > 0) {
        console_puts("[irecv] sid=");
        console_hex64(sid);
        console_puts(" focus=");
        console_hex64((uint64_t)(int64_t)focus_sid);
        console_puts(" qn=");
        console_hex64((uint64_t)inq.qn);
        console_puts("\n");
    }
    if (inq.qn > 0)
        dbg = 100;
    if ((int32_t)sid != focus_sid)
        return 0; /* only the focused session receives */
    uint32_t maxrec = cap / 4;
    uint32_t copied = 0;
    uint8_t *dst = (uint8_t *)out;
    while (inq.qn > 0 && copied < maxrec) {
        dst[copied * 4 + 0] = inq.kind[inq.qh];
        dst[copied * 4 + 1] = inq.mods[inq.qh];
        dst[copied * 4 + 2] = (uint8_t)inq.cp[inq.qh];
        dst[copied * 4 + 3] = (uint8_t)(inq.cp[inq.qh] >> 8);
        inq.qh = (inq.qh + 1) % QLEN;
        inq.qn--;
        copied++;
    }
    return (int)(copied * 4);
}

int input_focus_set(uint32_t caller_sid, int handle) {
    if (!(sched_capmask_of(caller_sid) & SCHED_CAP_FOCUS)) {
        console_puts("[input] set: no FOCUS cap\n");
        return -1;
    }
    int owner = ports_owner_of_handle(caller_sid, handle);
    if (owner <= 0)
        return -1;
    focus_sid = owner;
    console_puts("[input] focus -> '");
    console_puts(sched_name_of((uint32_t)owner));
    console_puts("'\n");
    return 0;
}
