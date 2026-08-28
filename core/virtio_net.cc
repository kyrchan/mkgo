/* Virtio-net native shim -- MODERN (virtio 1.x) transport via
 * core/virtio_modern.cc, feeding the §6 packet windows of abi/ABI.md.
 *
 * Ownership split (AGENTS.md rule: raw HW stays in tiny shims):
 *   - this file owns the real virtio rings and DMA;
 *   - guests see ONLY the two §6 windows (RX/TX ring buffers) mapped at
 *     fixed offsets in the net session's linear memory, reported through
 *     devman ENUM (class 2, instance 0=RX 1=TX);
 *   - L2/L3/L4 policy lives entirely in services/net.
 *
 * Polled operation (no interrupts in v1): sched_run calls
 * virtio_net_poll() every iteration; it drains the guest TX window into
 * the device and moves device completions into the RX window.
 *
 * Framing: 10-byte virtio_net_hdr (no MRG_RXBUF negotiated), separate
 * header/data descriptors on TX; RX posts single writable buffers. */
#include "io.h"
#include "plat.h"
#include "sched.h"
#include "virtio_modern.h"
#include "sched.h"
#include <stdint.h>

#define VN_QNUM 256
#define VN_DESC_TBL (VN_QNUM * 16)
#define VN_AVAIL_SZ (6 + VN_QNUM * 2)
#define VN_USED_OFF ((VN_DESC_TBL + VN_AVAIL_SZ + 4095u) & ~4095u)
/* With VERSION_1 the device consumes a 12-byte header when the layout
 * matches mergeable format (num_buffers field present); empirically
 * QEMU10 modern-net strips 12 even without MRG_RXBUF acked. */
#define VN_HDR_LEN 12

/* §6 window geometry (must match services/net packet.go) */
#define WIN_SLOTS 256
#define WIN_STRIDE 1536
#define WIN_HEADER 8
#define RING_SIZE (WIN_HEADER + WIN_SLOTS * WIN_STRIDE)

/* fixed offsets of the RX/TX windows inside the net session's linear
 * memory; devman ENUM reports exactly these */
/* 512 MiB: far above anything the Go runtime will ever allocate, so the
 * window pages can never collide with guest heap/stack objects (an
 * earlier placement at 16 MiB was being overwritten by Go heap growth,
 * crashing the runtime with stack-corruption panics). */
#define NET_RX_WIN 0x4000000ULL  /* 64 MiB: proven working placement */
#define NET_TX_WIN (NET_RX_WIN + RING_SIZE)
#define NET_MEM_MIN ((NET_TX_WIN + RING_SIZE + 0xFFFFULL) & ~0xFFFFULL)

/* RX queue storage: ring pages then receive buffers */
#define VN_RX_BUFS 32
#define VN_BUF_SZ 2048
#define VN_BUF_OFF ((VN_USED_OFF + 4u + 8u * VN_QNUM + 4095u) & ~4095u)

static uint8_t rx_vring[VN_BUF_OFF + VN_RX_BUFS * VN_BUF_SZ]
__attribute__((aligned(4096)));
static uint16_t rx_avail_idx;
static uint16_t rx_last_used;

static uint8_t tx_vring[VN_BUF_OFF + 4096] __attribute__((aligned(4096)));
static uint16_t tx_avail_idx;
static uint16_t tx_last_used;

static uint8_t tx_scratch[VN_HDR_LEN + 1526] __attribute__((aligned(16)));

static vmod_dev dev;
static uint16_t rx_qoff, tx_qoff;
static int vn_ready;

static uint8_t vn_mac[6] = {0x02, 0x00, 0x00, 0x00, 0x00, 0x09};

static void desc_put(uint16_t idx, uint64_t pa, uint32_t len, uint16_t flags,
                     uint16_t next) {
    volatile uint64_t *a = (volatile uint64_t *)(tx_vring + idx * 16);
    volatile uint64_t *b = (volatile uint64_t *)(tx_vring + idx * 16 + 8);
    if (idx >= 2) { /* only the two TX scratch descs live here */
        volatile uint64_t *ra =
            (volatile uint64_t *)(rx_vring + VN_DESC_TBL + idx * 16);
        volatile uint64_t *rb =
            (volatile uint64_t *)(rx_vring + VN_DESC_TBL + idx * 16 + 8);
        (void)ra;
        (void)rb;
    }
    *a = pa;
    *b = (uint64_t)len | ((uint64_t)flags << 32) | ((uint64_t)next << 48);
}

void virtio_net_init(void) {
    vn_ready = 0;
    if (vmod_probe(0x1000, &dev) != 0 &&
        vmod_probe(0x1040, &dev) != 0) {
        console_puts("[virtio-net] no device\n");
        return;
    }
    /* VERSION_1 + MAC (so the cfg region carries our address) */
    uint64_t got = vmod_features(&dev, (1ull << 32) | (1ull << 5) |
                                           (1ull << 15));
    vmod_status_add(&dev, 8); /* FEATURES_OK */
    if (!(dev.common[0x14] & 8)) {
        console_puts("[virtio-net] FEATURES_OK refused\n");
        return;
    }
    if (got & (1ull << 5))
        for (int i = 0; i < 6; i++)
            vn_mac[i] = dev.device[i];

    uint16_t rxs = vmod_queue_size(&dev, 0);
    uint16_t txs = vmod_queue_size(&dev, 1);
    if (!rxs || !txs) {
        console_puts("[virtio-net] queues dead\n");
        return;
    }
    if (rxs > VN_QNUM)
        rxs = VN_QNUM;
    if (txs > VN_QNUM)
        txs = VN_QNUM;

    uint64_t rbase = (uint64_t)(uintptr_t)rx_vring;
    if (vmod_queue_setup(&dev, 0, rxs, rbase, rbase + VN_DESC_TBL,
                         rbase + VN_USED_OFF) < 0) {
        console_puts("[virtio-net] rx qsetup failed\n");
        return;
    }
    uint64_t tbase = (uint64_t)(uintptr_t)tx_vring;
    if (vmod_queue_setup(&dev, 1, txs, tbase, tbase + VN_DESC_TBL,
                         tbase + VN_USED_OFF) < 0) {
        console_puts("[virtio-net] tx qsetup failed\n");
        return;
    }
    rx_qoff = vmod_queue_notify_off(&dev, 0);
    tx_qoff = vmod_queue_notify_off(&dev, 1);

    /* post receive buffers: single device-writable desc each */
    for (int i = 0; i < VN_RX_BUFS; i++) {
        uint64_t pa =
            (uint64_t)(uintptr_t)(rx_vring + VN_BUF_OFF +
                                  (uint32_t)i * VN_BUF_SZ);
        volatile uint64_t *a =
            (volatile uint64_t *)(rx_vring + (uint32_t)i * 16);
        volatile uint64_t *b =
            (volatile uint64_t *)(rx_vring + (uint32_t)i * 16 + 8);
        *a = pa;
        *b = (uint64_t)VN_BUF_SZ | (2ull << 32); /* WRITE, chain end */
        volatile uint16_t *aidx =
            (volatile uint16_t *)(rx_vring + VN_DESC_TBL + 2);
        volatile uint16_t *aring =
            (volatile uint16_t *)(rx_vring + VN_DESC_TBL + 4);
        aring[rx_avail_idx % VN_QNUM] = (uint16_t)i;
        rx_avail_idx++;
        *aidx = rx_avail_idx;
    }
    rx_last_used = 0;
    tx_last_used = 0;

    vmod_driver_ok(&dev);
    vn_ready = 1;
    console_puts("[virtio-net] modern ready mac=");
    for (int i = 0; i < 6; i++) {
        console_hex64(vn_mac[i]);
        console_puts(i == 5 ? "\n" : ":");
    }
}

int virtio_net_available(void) { return vn_ready; }

/* ---- §6 window side ---- */

struct wstate {
    int attached;
    void *rt; /* IM3Runtime of the net session (memory may relocate) */
};
static struct wstate ws;

extern "C" {
#include "wasm3.h"
M3Result ResizeMemory(IM3Runtime io_runtime, unsigned i_numPages);
}

extern "C" int netwin_attach(void *runtime);

int netwin_attach_impl(IM3Runtime runtime) {
    if (!vn_ready)
        return -1;
    ws.rt = runtime;
    unsigned cur = 0;
    m3_GetMemory(runtime, &cur, 0);
    unsigned need_pages = (unsigned)((NET_MEM_MIN + 65535) >> 16);
    if (cur / 65536u < need_pages) {
        M3Result r = ResizeMemory(runtime, need_pages);
        if (r) {
            console_puts("[netwin] grow failed: ");
            console_puts(r);
            console_puts("\n");
            return -1;
        }
    }
    unsigned sz = 0;
    uint8_t *mem = m3_GetMemory(runtime, &sz, 0);
    if (!mem || sz < NET_MEM_MIN)
        return -1;
    for (uint32_t i = 0; i < WIN_HEADER; i++) {
        mem[NET_RX_WIN + i] = 0;
        mem[NET_TX_WIN + i] = 0;
    }
    ws.attached = 1;
    console_puts("[netwin] rx@");
    console_hex64(NET_RX_WIN);
    console_puts(" tx@");
    console_hex64(NET_TX_WIN);
    console_puts("\n");
    return 0;
}

extern "C" int netwin_attached(void) { return ws.attached; }

/* grow any managed-runtime session's linear memory to at least min_bytes */
extern "C" int vmod_grow_session(void *runtime, uint32_t min_bytes) {
    IM3Runtime rt = (IM3Runtime)runtime;
    unsigned cur = 0;
    m3_GetMemory(rt, &cur, 0);
    unsigned need_pages = (min_bytes + 65535u) / 65536u;
    if (cur / 65536u >= need_pages)
        return 0;
    M3Result r = ResizeMemory(rt, need_pages);
    return r ? -1 : 0;
}

/* ABI-stable entry used by kernsvc SPAWN hook (C linkage there) */
extern "C" int netwin_attach(void *runtime) {
    return netwin_attach_impl((IM3Runtime)runtime);
}

static uint32_t rget32(const uint8_t *p) {
    return (uint32_t)p[0] | (uint32_t)p[1] << 8 | (uint32_t)p[2] << 16 |
           (uint32_t)p[3] << 24;
}
static void rput32(uint8_t *p, uint32_t v) {
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
    p[2] = (uint8_t)(v >> 16);
    p[3] = (uint8_t)(v >> 24);
}

/* TX is single-outstanding and fully non-blocking: virtio_net_poll runs
 * in SCHEDULER context -- any wait or yield here would stall the whole
 * guest (spin) or re-enter the scheduler (corruption). State machine:
 *   idle      -> kick frame, pending=true
 *   pending   -> reap used ring on later poll iterations
 * The caller leaves the frame queued until we report it sent. */
static bool tx_pending;

static void vn_tx_kick(const uint8_t *frame, uint32_t len) {
    for (uint32_t i = 0; i < len; i++)
        tx_scratch[VN_HDR_LEN + i] = frame[i];
    for (int i = 0; i < VN_HDR_LEN; i++)
        tx_scratch[i] = 0; /* zeroed virtio_net_hdr */

    uint64_t hdr_pa = (uint64_t)(uintptr_t)tx_scratch;
    uint64_t dat_pa = (uint64_t)(uintptr_t)(tx_scratch + VN_HDR_LEN);
    desc_put(0, hdr_pa, VN_HDR_LEN, 1, 1); /* ro + NEXT -> 1 */
    desc_put(1, dat_pa, len, 0, 0);        /* ro, chain end */
    volatile uint16_t *aidx =
        (volatile uint16_t *)(tx_vring + VN_DESC_TBL + 2);
    volatile uint16_t *aring =
        (volatile uint16_t *)(tx_vring + VN_DESC_TBL + 4);
    aring[tx_avail_idx % VN_QNUM] = 0;
    tx_avail_idx++;
    *aidx = tx_avail_idx;
    vmod_notify(&dev, 1, tx_qoff);
}

/* reap a completed TX if the device has posted one; returns true when
 * the outstanding frame is done */
static bool vn_tx_reap(void) {
    volatile uint16_t *uidx = (volatile uint16_t *)(tx_vring + VN_USED_OFF + 2);
    if (*uidx == tx_last_used)
        return false;
    tx_last_used = *uidx;
    return true;
}

void virtio_net_poll(void) {
    if (!vn_ready)
        return;
    if (!ws.attached)
        return;
    unsigned sz = 0;
    uint8_t *mem =
        m3_GetMemory((IM3Runtime)ws.rt, &sz, 0);
    if (!mem || (uint64_t)sz < NET_MEM_MIN)
        return; /* session exiting or memory shrank */
    volatile uint8_t *rxw = mem + NET_RX_WIN;
    volatile uint8_t *txw = mem + NET_TX_WIN;

        /* --- TX: single-outstanding non-blocking state machine ---
     * pending -> reap completion on a later poll; idle+queued -> kick.
     * NEVER spin or yield inside poll (scheduler context!). */
    uint32_t th = rget32((const uint8_t *)txw);
    uint32_t tt = rget32((const uint8_t *)txw + 4);
    volatile uint16_t *tuidx =
        (volatile uint16_t *)(tx_vring + VN_USED_OFF + 2);
    if (tx_pending) {
        if (*tuidx != tx_last_used) {
            tx_last_used = *tuidx;
            tx_pending = false;
            th++;
            rput32((uint8_t *)txw, th);
        }
    } else if (th != tt) {
        uint32_t slot = th % WIN_SLOTS;
        const uint8_t *sp =
            (const uint8_t *)(txw + WIN_HEADER + slot * WIN_STRIDE);
        uint32_t len = rget32(sp);
        if (len > 1526)
            len = 1526; /* corrupt slot clamp */
        vn_tx_kick(sp + 4, len);
        tx_pending = true;
    }

/* --- move device completions into the RX window --- */
    volatile uint16_t *uidx = (volatile uint16_t *)(rx_vring + VN_USED_OFF + 2);
    volatile uint32_t *uring = (volatile uint32_t *)(rx_vring + VN_USED_OFF + 4);
    while (*uidx != rx_last_used) {
        uint32_t e = (uint32_t)rx_last_used % VN_QNUM;
        uint32_t desc_id = uring[e * 2];
        uint32_t written = uring[e * 2 + 1];
        rx_last_used++;

        const uint8_t *buf = rx_vring + VN_BUF_OFF + desc_id * VN_BUF_SZ;
        if (written >= VN_HDR_LEN && ws.attached) {
            uint32_t flen = written - VN_HDR_LEN;
            if (flen > 1526)
                flen = 1526;
            uint32_t rt = rget32((const uint8_t *)rxw + 4);
            uint32_t rh = rget32((const uint8_t *)rxw);
            if (rt - rh < WIN_SLOTS) { /* full ring: drop (v1 policy) */
                uint8_t *slotp = mem + NET_RX_WIN + WIN_HEADER +
                                 (rt % WIN_SLOTS) * WIN_STRIDE;
                rput32(slotp, flen);
                for (uint32_t i = 0; i < flen; i++)
                    slotp[4 + i] = buf[VN_HDR_LEN + i];
                rput32(mem + NET_RX_WIN + 4, rt + 1);
            }
        }
        /* repost the buffer */
        volatile uint64_t *a =
            (volatile uint64_t *)(rx_vring + (uint32_t)desc_id * 16);
        volatile uint64_t *b =
            (volatile uint64_t *)(rx_vring + (uint32_t)desc_id * 16 + 8);
        *a = (uint64_t)(uintptr_t)buf;
        *b = (uint64_t)VN_BUF_SZ | (2ull << 32); /* WRITE */
        volatile uint16_t *aidx =
            (volatile uint16_t *)(rx_vring + VN_DESC_TBL + 2);
        volatile uint16_t *aring =
            (volatile uint16_t *)(rx_vring + VN_DESC_TBL + 4);
        aring[rx_avail_idx % VN_QNUM] = (uint16_t)desc_id;
        rx_avail_idx++;
        *aidx = rx_avail_idx;
        vmod_notify(&dev, 0, rx_qoff);
    }
}
