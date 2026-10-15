/* Input subsystem (abi/ABI.md §4): serial bytes become key-down records
 * delivered ONLY to the focused session. Focus is a kernel attribute
 * moved via kern_focus_set(port_handle) by FOCUS-capable sessions. */
#include "input.h"
#include "plat.h"
#include "sched.h"
#include "ports.h"
#include <stdio.h>
#include <cstring>

#define QLEN 256

/* Kernel debug routed through the console service (not direct UART)
 * so the console's prevEcho logic can separate it from the shell's
 * in-place echo redraw. */
static void klog(const char *msg) {
    ports_enqueue_by_name("console", msg, (uint32_t)strlen(msg));
}

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
    for (int n = 0; n < 64 && console_rx_ready(); n++) {
        int b = console_rx_byte();
        if (b < 0)
            break; /* driver signalled ready but no byte; do not
                    * insert a phantom 0xFF. */
        push_byte((uint8_t)b);
    }
}

int input_recv(uint32_t sid, void *out, uint32_t cap) {
    if (!out || cap < 4)
        return 0;
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
        klog("[input] set: no FOCUS cap");
        return -1;
    }
    int owner = ports_owner_of_handle(caller_sid, handle);
    if (owner <= 0)
        return -1;
    /* F-AUDIT-6: FOCUS-capable session must redirect focus to its OWN
     * port (or one of its own handles). Otherwise any FOCUS session
     * can steal input by handing focus to a colluding peer. */
    if ((uint32_t)owner != caller_sid) {
        klog("[input] set: owner != caller (focus-steal blocked)");
        return -1;
    }
    focus_sid = owner;
    char buf[64];
    snprintf(buf, sizeof(buf), "[input] focus -> '%s'", sched_name_of((uint32_t)owner));
    klog(buf);
    return 0;
}
