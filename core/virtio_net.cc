/* Virtio-net native shim (Phase 9): legacy virtio PCI device feeding the
 * §6 packet windows of abi/ABI.md.
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
 * Legacy virtio-net framing: every descriptor chain starts with a
 * 10-byte virtio_net_hdr (zeroed for TX; stripped on RX). */
#include "io.h"
#include "plat.h"
#include <stdint.h>
extern "C" {
#include "wasm3.h"
}

static uint16_t pci_config_read16(uint8_t bus, uint8_t dev, uint8_t fn,
                                  uint8_t offset) {
    uint32_t addr = 0x80000000UL | ((uint32_t)bus << 16) |
                    ((uint32_t)dev << 11) | ((uint32_t)fn << 8) |
                    (offset & 0xFC);
    outl(0xCF8, addr);
    return (uint16_t)(inl(0xCFC) >> ((offset & 2) * 8));
}

extern "C" {

#define VIRTIO_PCI_VENDOR 0x1AF4
#define VIRTIO_NET_DEVICE 0x1000

#define VIRTIO_PCI_HOST_FEATURES 0
#define VIRTIO_PCI_GUEST_FEATURES 4
#define VIRTIO_PCI_QUEUE_PFN 8
#define VIRTIO_PCI_QUEUE_NUM 12
#define VIRTIO_PCI_QUEUE_SEL 14
#define VIRTIO_PCI_QUEUE_NOTIFY 16
#define VIRTIO_PCI_STATUS 18

#define VIRTIO_STATUS_ACK 1
#define VIRTIO_STATUS_DRIVER 2
#define VIRTIO_STATUS_DRIVER_OK 4

#define VN_QNUM 256
#define VN_DESC_TBL (VN_QNUM * 16)
#define VN_AVAIL (6 + VN_QNUM * 2)
/* Mirror the working blk shim: used ring directly after avail. */
#define VN_USED_OFF (VN_DESC_TBL + VN_AVAIL)
#define VN_BUF_OFF 8192 /* past used ring end (VN_USED_OFF+4+8*256) */
#define VN_HDR_LEN 10

/* §6 window geometry (must match services/net packet.go) */
#define WIN_SLOTS 256
#define WIN_STRIDE 1536
#define WIN_HEADER 8
#define RING_SIZE (WIN_HEADER + WIN_SLOTS * WIN_STRIDE)

/* fixed offsets of the RX/TX windows inside the net session's linear
 * memory; devman ENUM reports exactly these */
#define NET_RX_WIN 0x1000000ULL
#define NET_TX_WIN (NET_RX_WIN + RING_SIZE)
#define NET_MEM_MIN ((NET_TX_WIN + RING_SIZE + 0xFFFF) & ~0xFFFFULL)

static uint16_t vn_io_base;
static int vn_ready;
static uint8_t vn_mac[6];

/* ---- per-queue storage (identity map: VA == PA) ---- */
/* RX queue: desc+avail+used then the receive buffers themselves */
#define VN_RX_BUFS 8
#define VN_BUF_SZ 2048
static uint8_t rx_vring[VN_BUF_OFF + VN_RX_BUFS * VN_BUF_SZ]
__attribute__((aligned(4096)));
static uint16_t rx_avail_idx;
static uint16_t rx_last_used;
static int rx_posted;

/* TX queue: desc+avail+used then the outgoing-frame scratch */
static uint8_t tx_vring[VN_BUF_OFF + 4096] __attribute__((aligned(4096)));
static uint16_t tx_avail_idx;
static uint16_t tx_last_used;
static uint8_t tx_scratch[VN_HDR_LEN + 1526] __attribute__((aligned(16)));

static void vq_setup(uint16_t iobase, uint16_t queue_sel,
                     volatile uint8_t *vring) {
    outw(iobase + VIRTIO_PCI_QUEUE_SEL, queue_sel);
    outw(iobase + VIRTIO_PCI_QUEUE_NUM, VN_QNUM);
    outl(iobase + VIRTIO_PCI_QUEUE_PFN,
         (uint32_t)((uint64_t)(uintptr_t)vring >> 12));
}

/* write one descriptor (16 bytes); flags occupy the high 32 bits */
static void desc_put(volatile uint8_t *tbl, uint16_t idx, uint64_t pa,
                     uint32_t len, uint64_t flags) {
    volatile uint64_t *a = (volatile uint64_t *)(tbl + (uint32_t)idx * 16);
    volatile uint64_t *b = (volatile uint64_t *)(tbl + (uint32_t)idx * 16 + 8);
    *a = pa;
    *b = (uint64_t)len | ((uint64_t)flags << 32);
}

static void avail_push(volatile uint8_t *vr, uint16_t desc_idx,
                       uint16_t *ctr) {
    volatile uint16_t *idxp = (volatile uint16_t *)(vr + VN_DESC_TBL + 2);
    volatile uint16_t *ring = (volatile uint16_t *)(vr + VN_DESC_TBL + 4);
    ring[*ctr % VN_QNUM] = desc_idx;
    (*ctr)++;
    *idxp = *ctr; /* no event idx: device polls */
}

void virtio_net_init(void) {
    vn_ready = 0;
    for (uint8_t d = 0; d < 32; d++) {
        if (pci_config_read16(0, d, 0, 0x00) == 0xFFFF)
            continue;
        uint16_t vendor = pci_config_read16(0, d, 0, 0x00);
        uint16_t devid = pci_config_read16(0, d, 0, 0x02);
        if (vendor != VIRTIO_PCI_VENDOR || devid != VIRTIO_NET_DEVICE)
            continue;

        /* found virtio-net: get BAR0 I/O base */
        uint32_t bar0_raw;
        {
            uint32_t addr = 0x80000000UL | (0UL << 16) | (d << 11) |
                            (0 << 8) | 0x10;
            outl(0xCF8, addr);
            bar0_raw = inl(0xCFC);
        }
        /* ensure IO+MEM+BUSMASTER are set -- firmware may leave unused
         * devices with DMA denied, which silently swallows completions */
        {
            outl(0xCF8, 0x80000000UL | (d << 11) | 0x04);
            uint16_t cmd = (uint16_t)inl(0xCFC);
            console_puts("[vn] pci cmd=");
            console_hex64(cmd);
            console_puts("\n");
            outl(0xCFC, (uint32_t)(cmd | 0x7));
        }
        if (!(bar0_raw & 1))
            continue; /* need the legacy I/O BAR */
        vn_io_base = (uint16_t)(bar0_raw & ~3);

        outb(vn_io_base + VIRTIO_PCI_STATUS, 0);
        uint8_t st = VIRTIO_STATUS_ACK | VIRTIO_STATUS_DRIVER;
        outb(vn_io_base + VIRTIO_PCI_STATUS, st);
        /* acknowledge NO features -- legacy requires the ack before OK */
        outl(vn_io_base + VIRTIO_PCI_GUEST_FEATURES, 0);

        /* MAC lives in device config at 0x14 (MSI-X off, legacy) */
        for (int i = 0; i < 6; i++)
            vn_mac[i] = inb(vn_io_base + 0x14 + i);

        outw(vn_io_base + VIRTIO_PCI_QUEUE_SEL, 0);
        {
            uint32_t qsize = inl(vn_io_base + VIRTIO_PCI_QUEUE_NUM);
            if (qsize == 0)
                continue;
            outw(vn_io_base + VIRTIO_PCI_QUEUE_NUM, (uint16_t)VN_QNUM);
        }
#ifdef VN_TX_ON_Q0
        /* BISECT: tx alone on q0 */
        outl(vn_io_base + VIRTIO_PCI_QUEUE_PFN,
             (uint32_t)((uint64_t)(uintptr_t)tx_vring >> 12));
#else
        outl(vn_io_base + VIRTIO_PCI_QUEUE_PFN,
             (uint32_t)((uint64_t)(uintptr_t)rx_vring >> 12));
        outw(vn_io_base + VIRTIO_PCI_QUEUE_SEL, 1);
        outw(vn_io_base + VIRTIO_PCI_QUEUE_NUM, (uint16_t)VN_QNUM);
        outl(vn_io_base + VIRTIO_PCI_QUEUE_PFN,
             (uint32_t)((uint64_t)(uintptr_t)tx_vring >> 12));
#endif
#ifdef VN_DEBUG_PFNS
        {
            outw(vn_io_base + VIRTIO_PCI_QUEUE_SEL, 0);
            uint32_t p0 = inl(vn_io_base + VIRTIO_PCI_QUEUE_PFN);
            outw(vn_io_base + VIRTIO_PCI_QUEUE_SEL, 1);
            uint32_t p1 = inl(vn_io_base + VIRTIO_PCI_QUEUE_PFN);
            console_puts("[vn] pfn0=");
            console_hex64(p0);
            console_puts(" pfn1=");
            console_hex64(p1);
            console_puts(" want1=");
            console_hex64((uint64_t)(uintptr_t)tx_vring >> 12);
            console_puts("\n");
        }
#endif

        /* post receive buffers: one single device-writable desc each */
        for (int i = 0; i < VN_RX_BUFS; i++) {
            uint64_t pa = (uint64_t)(uintptr_t)(rx_vring + VN_BUF_OFF +
                                                (uint32_t)i * VN_BUF_SZ);
            desc_put(rx_vring, (uint16_t)i, pa, VN_BUF_SZ, 2ull << 32 /* WRITE */);
            avail_push(rx_vring, (uint16_t)i, &rx_avail_idx);
            rx_posted++;
        }
        rx_last_used = 0;
        tx_last_used = 0;

        outb(vn_io_base + VIRTIO_PCI_STATUS, st | VIRTIO_STATUS_DRIVER_OK);
        vn_ready = 1;
        console_puts("[virtio-net] ready mac=");
        for (int i = 0; i < 6; i++) {
            console_hex64(vn_mac[i]);
            console_puts(i == 5 ? "\n" : ":");
        }
        return;
    }
    console_puts("[virtio-net] no device found\n");
}

int virtio_net_available(void) { return vn_ready; }
#ifdef VN_TX_ON_Q0
static uint16_t vn_tx_queue(void) { return 0; }
#else
static uint16_t vn_tx_queue(void) { return 1; }
#endif

/* ---- §6 window side ---- */

struct wstate {
    int attached;
    IM3Runtime rt; /* net session runtime (memory may relocate) */
};
static struct wstate ws;

extern "C" {
M3Result ResizeMemory(IM3Runtime io_runtime, unsigned i_numPages);
}

int netwin_attach(IM3Runtime runtime) {
    console_puts("[netwin] attach enter ready=");
    console_hex64((uint64_t)vn_ready);
    console_puts("\n");
    if (!vn_ready)
        return -1;
    ws.rt = runtime;
    /* grow linear memory to cover both windows */
    unsigned cur_bytes = 0;
    m3_GetMemory(runtime, &cur_bytes, 0);
    unsigned need_pages = (unsigned)((NET_MEM_MIN + 65535) >> 16);
    if (cur_bytes / 65536u < need_pages) {
        M3Result r = ResizeMemory(runtime, need_pages);
        if (r) {
            console_puts("[netwin] grow failed\n");
            return -1;
        }
    }
    unsigned sz = 0;
    uint8_t *mem = m3_GetMemory(runtime, &sz, 0);
    console_puts("[netwin] sz=");
    console_hex64(sz);
    console_puts(" mem=");
    console_hex64((uint64_t)(uintptr_t)mem);
    console_puts("\n");
    if (!mem || sz < NET_MEM_MIN)
        return -1;
    /* reset both ring headers: empty */
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

int netwin_attached(void) { return ws.attached; }

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

/* send one frame through the device (one outstanding, polled).
 * Legacy virtio-net wants the 10-byte header as its OWN descriptor,
 * then the frame data -- same discipline as the blk shim's chain. */
static int vn_tx(const uint8_t *frame, uint32_t len) {
    if (len == 0 || len > 1526)
        return -1;
    for (uint32_t i = 0; i < len; i++)
        tx_scratch[VN_HDR_LEN + i] = frame[i];
    for (int i = 0; i < VN_HDR_LEN; i++)
        tx_scratch[i] = 0; /* zeroed virtio_net_hdr */

    uint64_t hdr_pa = (uint64_t)(uintptr_t)tx_scratch;
    uint64_t dat_pa = (uint64_t)(uintptr_t)(tx_scratch + VN_HDR_LEN);
    desc_put(tx_vring, 0, hdr_pa, VN_HDR_LEN,
             0ull | (1ull << 32)); /* readonly + NEXT */
    desc_put(tx_vring, 1, dat_pa, len, 0); /* readonly, chain end */
    uint16_t tx_q = vn_tx_queue();
    avail_push(tx_vring, 0, &tx_avail_idx);
    outw(vn_io_base + VIRTIO_PCI_QUEUE_NOTIFY, tx_q);

    volatile uint16_t *used_idx =
        (volatile uint16_t *)(tx_vring + VN_USED_OFF + 2);
    uint64_t spins = 0;
    while (*used_idx == tx_last_used) {
        if (++spins > 20000000ULL) {
            console_puts("[netdbg] tx timeout used=");
            console_hex64(*used_idx);
            console_puts(" avail=");
            console_hex64(tx_avail_idx);
            uint8_t isr = inb(vn_io_base + 19);
            console_puts(" isr=");
            console_hex64(isr);
            console_puts(" status=");
            console_hex64(inb(vn_io_base + VIRTIO_PCI_STATUS));
            console_puts("\n");
            return -1; /* device lost: caller retries later */
        }
    }
    tx_last_used = *used_idx;
    return 0;
}

void virtio_net_poll(void) {
    if (!vn_ready || !ws.attached)
        return;
    unsigned sz = 0;
    uint8_t *mem = m3_GetMemory(ws.rt, &sz, 0);
    if (!mem || (uint64_t)sz < NET_MEM_MIN)
        return; /* session exiting or memory shrank */
    volatile uint8_t *rxw = mem + NET_RX_WIN;
    volatile uint8_t *txw = mem + NET_TX_WIN;

    /* --- drain guest TX window into the device --- */
    uint32_t th = rget32((const uint8_t *)txw);       /* consumer (us) */
    uint32_t tt = rget32((const uint8_t *)txw + 4);   /* producer (guest) */
    while (th != tt) {
        uint32_t slot = th % WIN_SLOTS;
        const uint8_t *sp =
            (const uint8_t *)(txw + WIN_HEADER + slot * WIN_STRIDE);
        uint32_t len = rget32(sp);
        if (len > 1526)
            len = 1526; /* corrupt slot clamp */
        if (vn_tx(sp + 4, len) != 0)
            break; /* leave frame queued; retry next poll */
        th++;
        rput32((uint8_t *)txw, th);
    }

    /* --- move device completions into the RX window --- */
    volatile uint16_t *used_idx =
        (volatile uint16_t *)(rx_vring + VN_USED_OFF + 2);
    volatile uint32_t *used_ring =
        (volatile uint32_t *)(rx_vring + VN_USED_OFF + 4);
    while (*used_idx != rx_last_used) {
        uint32_t e = (uint32_t)rx_last_used % VN_QNUM;
        uint32_t desc_id = used_ring[e * 2];
        uint32_t written = used_ring[e * 2 + 1];
        rx_last_used++;

        const uint8_t *buf =
            rx_vring + VN_BUF_OFF + desc_id * VN_BUF_SZ;
        if (written >= VN_HDR_LEN && ws.attached) {
            uint32_t flen = written - VN_HDR_LEN;
            if (flen > 1526)
                flen = 1526;
            uint32_t rt = rget32((const uint8_t *)rxw + 4);
            uint32_t rh = rget32((const uint8_t *)rxw);
            if (rt - rh < WIN_SLOTS) { /* full ring: drop (v1 policy) */
                uint8_t *slotp =
                    mem + NET_RX_WIN + WIN_HEADER +
                    (rt % WIN_SLOTS) * WIN_STRIDE;
                rput32(slotp, flen);
                for (uint32_t i = 0; i < flen; i++)
                    slotp[4 + i] = buf[VN_HDR_LEN + i];
                rput32(mem + NET_RX_WIN + 4, rt + 1);
            }
        }
        /* repost the buffer */
        desc_put(rx_vring, (uint16_t)desc_id,
                 (uint64_t)(uintptr_t)buf, VN_BUF_SZ, 2ull << 32 /*WRITE*/);
        avail_push(rx_vring, (uint16_t)desc_id, &rx_avail_idx);
        outw(vn_io_base + VIRTIO_PCI_QUEUE_NOTIFY, 0);
    }
}
} /* extern "C" */
