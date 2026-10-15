/* Shared MODERN-virtio transport interface (core/virtio_modern.cc). */
#ifndef VIRTIO_MODERN_H
#define VIRTIO_MODERN_H
#include <stdint.h>

struct vmod_dev {
    volatile uint8_t *common;
    volatile uint8_t *notify;
    volatile uint8_t *isr;
    volatile uint8_t *device;
    uint32_t notify_mult;
    uint16_t queues;
    int ready;
};

extern "C" {
int vmod_probe(uint16_t want_devid, vmod_dev *out);
uint64_t vmod_features(vmod_dev *d, uint64_t accept);
void vmod_status_add(vmod_dev *d, uint8_t bits);
void vmod_driver_ok(vmod_dev *d);
uint16_t vmod_queue_size(vmod_dev *d, uint16_t qidx);
int vmod_queue_setup(vmod_dev *d, uint16_t qidx, uint16_t size, uint64_t dp,
                     uint64_t ap, uint64_t up);
void vmod_notify(vmod_dev *d, uint16_t qidx, uint16_t queue_notify_off);
uint16_t vmod_queue_notify_off(vmod_dev *d, uint16_t qidx);
uint8_t vmod_isr(vmod_dev *d);
uint64_t vmod_cfg_u64(vmod_dev *d, uint32_t off);
}
#endif
