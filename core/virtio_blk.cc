/* Virtio-blk native shim (Phase 8): legacy virtio PCI device.
 * Re-backs the kern_blk_* transport with real QEMU disk storage.
 * Zero guest-visible changes — same ABI, different backend (ABI §8). */
#include "io.h"
#include "plat.h"
#include <stdint.h>

extern "C" {


#define VIRTIO_PCI_VENDOR 0x1AF4
#define VIRTIO_BLK_DEVICE 0x1001

/* Legacy virtio PCI BAR0 register offsets */
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

#define VIRTIO_BLK_T_IN 0
#define VIRTIO_BLK_T_OUT 1

static uint16_t vblk_io_base;
static int vblk_ready;
static uint64_t vblk_sectors;

/* Legacy vring: 1024-entry desc table + avail ring + used ring.
 * We use a single 256-entry queue for simplicity. */
#define QNUM 256
#define DESC_TABLE_SIZE (QNUM * 16)
#define AVAIL_RING_SIZE (6 + QNUM * 2)
#define USED_RING_ALIGN 4096
static uint8_t vring_buf[DESC_TABLE_SIZE + AVAIL_RING_SIZE + USED_RING_ALIGN + 2048] __attribute__((aligned(4096)));
static uint16_t desc_next_free;
static uint16_t avail_idx;

/* sector buffer for data transfer (512 bytes per blk request) */
static uint8_t sector_data[512] __attribute__((aligned(16)));

/* Block request header for virtio-blk */
struct vblk_req {
    uint32_t type;
    uint32_t reserved;
    uint64_t sector;
} __attribute__((packed));
static struct vblk_req blk_hdr;

static uint16_t pci_config_read16(uint8_t bus, uint8_t dev, uint8_t fn, uint8_t offset) {
    uint32_t addr = 0x80000000UL | ((uint32_t)bus << 16) |
                    ((uint32_t)dev << 11) | ((uint32_t)fn << 8) | (offset & 0xFC);
    outl(0xCF8, addr);
    return (uint16_t)(inl(0xCFC) >> ((offset & 2) * 8));
}

void virtio_blk_init(void) {
    vblk_ready = 0;
    /* scan bus 0, devices 0-31 */
    for (uint8_t d = 0; d < 32; d++) {
        if (pci_config_read16(0, d, 0, 0x00) == 0xFFFF)
            continue;
        uint16_t vendor = pci_config_read16(0, d, 0, 0x00);
        uint16_t devid = pci_config_read16(0, d, 0, 0x02);
        if (vendor != VIRTIO_PCI_VENDOR || devid != VIRTIO_BLK_DEVICE)
            continue;
        /* found virtio-blk: get BAR0 I/O base */
        uint32_t bar0_raw = 0;
        /* read BAR0 as 32-bit */
        uint32_t cfg = 0x80000000UL | (0UL << 16) | (d << 11) | (0 << 8) | 0x10;
        outl(0xCF8, cfg);
        bar0_raw = inl(0xCFC);
        if (!(bar0_raw & 1))
            continue; /* not I/O BAR */
        vblk_io_base = (uint16_t)(bar0_raw & ~3);

        /* reset + ack + driver */
        outb(vblk_io_base + VIRTIO_PCI_STATUS, 0);
        uint8_t st = VIRTIO_STATUS_ACK | VIRTIO_STATUS_DRIVER;
        outb(vblk_io_base + VIRTIO_PCI_STATUS, st);

        /* set queue: select queue 0, set size, set PFN */
        outw(vblk_io_base + VIRTIO_PCI_QUEUE_SEL, 0);
        uint32_t qsize = inl(vblk_io_base + VIRTIO_PCI_QUEUE_NUM);
        if (qsize == 0) continue;
        outw(vblk_io_base + VIRTIO_PCI_QUEUE_NUM, (uint16_t)QNUM);
        uint64_t pfn_addr = (uint64_t)(uintptr_t)vring_buf;
        outl(vblk_io_base + VIRTIO_PCI_QUEUE_PFN, (uint32_t)(pfn_addr >> 12));

        /* read capacity */
        uint64_t cap_lo = inl(vblk_io_base + 0x14); /* VIRTIO_PCI_CONFIG_OFF + 0 */
        uint64_t cap_hi = inl(vblk_io_base + 0x18);
        vblk_sectors = cap_lo | (cap_hi << 32);

        /* DRIVER_OK */
        outb(vblk_io_base + VIRTIO_PCI_STATUS, st | VIRTIO_STATUS_DRIVER_OK);

        vblk_ready = 1;
        console_puts("[virtio-blk] ready sectors=");
        /* manual hex print to avoid dependency */
        console_hex64(vblk_sectors);
        console_puts("\n");
        return;
    }
    console_puts("[virtio-blk] no device found\n");
}

int virtio_blk_rw(int write, uint64_t lba, void *buf, uint32_t count) {
    if (!vblk_ready)
        return -1;
    if (lba + count > vblk_sectors)
        return -1;

    /* For simplicity: one sector at a time using the fixed sector_data buf */
    uint8_t *dst = (uint8_t *)buf;
    for (uint32_t s = 0; s < count; s++) {
        uint64_t cur_lba = lba + s;

        if (!write) {
            /* read: copy from disk into our scratch, then into caller buf */
            blk_hdr.type = VIRTIO_BLK_T_IN;
            blk_hdr.sector = cur_lba;
        } else {
            /* write: copy from caller buf into scratch */
            for (int i = 0; i < 512; i++)
                sector_data[i] = dst[s * 512 + i];
            blk_hdr.type = VIRTIO_BLK_T_OUT;
            blk_hdr.sector = cur_lba;
        }

        /* set up descriptor chain: [hdr][data][status] */
        volatile uint16_t *desc = (volatile uint16_t *)vring_buf;
        (void)desc; /* descriptors written via direct memory below */

        uint64_t hdr_pa = (uint64_t)(uintptr_t)&blk_hdr;
        uint64_t data_pa = (uint64_t)(uintptr_t)sector_data;
        static uint8_t status_byte;
        status_byte = 99;
        uint64_t status_pa = (uint64_t)(uintptr_t)&status_byte;

        /* descriptor 0: header (device-readable) */
        volatile uint64_t *d0a = (volatile uint64_t *)(vring_buf + 0*16);
        volatile uint64_t *d0b = (volatile uint64_t *)(vring_buf + 0*16 + 8);
        *d0a = hdr_pa;
        *d0b = 16 | ((uint64_t)1 << 32); /* len=16, next=1 */

        /* descriptor 1: data */
        volatile uint64_t *d1a = (volatile uint64_t *)(vring_buf + 1*16);
        volatile uint64_t *d1b = (volatile uint64_t *)(vring_buf + 1*16 + 8);
        *d1a = data_pa;
        uint64_t d1_flags = (uint64_t)2 << 32; /* write flag for READ ops */
        if (write) d1_flags = 0;
        *d1b = 512 | d1_flags | ((uint64_t)2 << 32); /* len=512, next=2 */

        /* descriptor 2: status (device-writable) */
        volatile uint64_t *d2a = (volatile uint64_t *)(vring_buf + 2*16);
        volatile uint64_t *d2b = (volatile uint64_t *)(vring_buf + 2*16 + 8);
        *d2a = status_pa;
        *d2b = 1 | ((uint64_t)2 << 32); /* len=1, write */

        /* avail ring: after desc table */
        volatile uint16_t *avail_flags = (volatile uint16_t *)(vring_buf + DESC_TABLE_SIZE);
        volatile uint16_t *avail_idx_p = (volatile uint16_t *)(vring_buf + DESC_TABLE_SIZE + 2);
        volatile uint16_t *avail_ring = (volatile uint16_t *)(vring_buf + DESC_TABLE_SIZE + 4);

        (void)avail_flags;
        uint16_t idx = avail_idx;
        avail_ring[idx % QNUM] = 0; /* head descriptor index */
        avail_idx++;
        *avail_idx_p = avail_idx;

        /* notify */
        outw(vblk_io_base + VIRTIO_PCI_QUEUE_NOTIFY, 0);

        /* poll used ring for completion */
        volatile uint16_t *used_flags = (volatile uint16_t *)(vring_buf + DESC_TABLE_SIZE + AVAIL_RING_SIZE);
        volatile uint16_t *used_idx = (volatile uint16_t *)(vring_buf + DESC_TABLE_SIZE + AVAIL_RING_SIZE + 2);
        volatile uint32_t *used_ring = (volatile uint32_t *)(vring_buf + DESC_TABLE_SIZE + AVAIL_RING_SIZE + 4);
        (void)used_flags;

        uint16_t last_used = 0; /* simplified: we only have 1 outstanding req */
        while (*used_idx == last_used) {
            /* busy wait (interrupts disabled in caller context or cooperative) */
        }
        last_used = *used_idx;
        uint32_t done_id = used_ring[0];
        int32_t done_status = (int32_t)used_ring[1];
        (void)done_id;

        if (done_status != 0)
            return -1;

        if (!write) {
            for (int i = 0; i < 512; i++)
                dst[s * 512 + i] = sector_data[i];
        }
    }
    return 0;
}

int virtio_blk_available(void) { return vblk_ready; }
} /* extern "C" */
