#include "engine.h"
#include "ports.h"
#include "input.h"
#include "devblk.h"
#include "pci.h"
#include "vfio.h"
#include "kernsvc.h"
#include "plat.h"
#include "mm.h"
#include "rt.h"
#include "lib.h"
#include "fsimport.h"
#include "sched.h"
#include "wasm/owned_session.h"
#include "wasm/wasi_bridge.h"
#include <new>

static constexpr uint64_t STACK_SLOTS = 8192;

struct OwnedEngineState {
    wasm::OwnedSession session;
    wasm::OwnedWasiContext wasi;
    uint64_t random_state;
};

static void *owned_alloc(uint64_t size, void *) {
    return rt_malloc(size);
}

static void owned_free(void *pointer, void *) {
    rt_free(pointer);
}

static int32_t owned_write(void *, int32_t fd, const uint8_t *data, uint32_t length) {
    /* fd >= 3 is the kernel-routed file layer, not the console. This used
     * to be unreachable on the owned engine, so a stock Go guest's writes
     * failed with EBADF unless the whole system ran on wasm3. */
    if (fd >= 3) {
        uint32_t done = 0;
        return fsimport_write(fd, const_cast<uint8_t *>(data), length, &done);
    }
    if (fd != 1 && fd != 2)
        return 8;
    for (uint32_t index = 0; index < length; ++index)
        console_putc(static_cast<char>(data[index]));
    return 0;
}

/* ---- kernel-routed preview1 file layer host hooks (see core/fsimport.h).
 * Thin by design: all the routing, framing, fd table and error mapping
 * lives in the engine-neutral module, and the engine only adapts the
 * guest-visible path bytes. */

static int32_t owned_path_open(void *, int32_t dirfd, const char *path,
                               uint32_t path_len, int32_t oflags,
                               int32_t *out_fd) {
    char rebased[FSIMPORT_PATH_MAX];
    if (path_len >= sizeof(rebased))
        return FSIMPORT_EINVAL;
    __builtin_memcpy(rebased, path, path_len);
    rebased[path_len] = 0;
    int32_t rc = fsimport_rebase_path(rebased, sizeof(rebased), dirfd);
    if (rc != FSIMPORT_ESUCCESS)
        return rc;
    return fsimport_path_open(rebased, oflags, out_fd);
}

static int32_t owned_mkdir(void *, int32_t dirfd, const char *path,
                           uint32_t path_len) {
    char rebased[FSIMPORT_PATH_MAX];
    if (path_len >= sizeof(rebased))
        return FSIMPORT_EINVAL;
    __builtin_memcpy(rebased, path, path_len);
    rebased[path_len] = 0;
    int32_t rc = fsimport_rebase_path(rebased, sizeof(rebased), dirfd);
    if (rc != FSIMPORT_ESUCCESS)
        return rc;
    return fsimport_mkdir(rebased);
}

static int32_t owned_read_fd(void *, int32_t fd, uint8_t *buf, uint32_t length,
                             uint32_t *done) {
    return fsimport_read(fd, buf, length, done);
}

static int32_t owned_close_fd(void *, int32_t fd) {
    if (!fsimport_fd_is_open(fd))
        return FSIMPORT_EBADF;
    fsimport_release_fd(fd);
    return FSIMPORT_ESUCCESS;
}

static void owned_exit(void *, int32_t code) {
    sched_wasi_state *state = sched_wasi_current();
    if (!state)
        return;
    state->exited = true;
    state->exit_code = code;
}

static void owned_yield(void *opaque) {
    OwnedEngineState *state = static_cast<OwnedEngineState *>(opaque);
    state->session.request_preempt();
}

static uint64_t owned_now_ns(void *) {
    return wasi_now_ns();
}

static uint32_t owned_random(void *opaque) {
    OwnedEngineState *state = static_cast<OwnedEngineState *>(opaque);
    state->random_state ^= state->random_state << 13;
    state->random_state ^= state->random_state >> 7;
    state->random_state ^= state->random_state << 17;
    return static_cast<uint32_t>(state->random_state >> 16);
}

static bool owned_range(const wasm::Runtime &runtime, uint64_t address, uint64_t size) {
    return runtime.memory() != nullptr && address <= runtime.memory_size() &&
           size <= runtime.memory_size() - address;
}

static bool owned_result_i32(wasm::Value *results, uint32_t result_count, int32_t value) {
    if (result_count != 1)
        return false;
    results[0].bits = static_cast<uint32_t>(value);
    return true;
}

static bool owned_result_i64(wasm::Value *results, uint32_t result_count, int64_t value) {
    if (result_count != 1)
        return false;
    results[0].bits = static_cast<uint64_t>(value);
    return true;
}

static bool owned_cap(uint64_t bit, const char *operation) {
    uint32_t sid = sched_current_sid();
    if ((sched_capmask_of(sid) & bit) != 0)
        return true;
    kernsvc_audit(sid, operation, "cap", "kernel");
    return false;
}

static wasm::RunStatus owned_port_create(void *, wasm::Runtime &runtime,
                                          const wasm::Value *args,
                                          wasm::Value *results,
                                          uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t length = static_cast<uint32_t>(args[1].bits);
    if (!owned_range(runtime, args[0].bits, length)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = port_create(sched_current_sid(),
                                 reinterpret_cast<const char *>(runtime.memory() + args[0].bits),
                                 length);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_port_bind(void *, wasm::Runtime &runtime,
                                        const wasm::Value *args,
                                        wasm::Value *results,
                                        uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t length = static_cast<uint32_t>(args[1].bits);
    if (!owned_range(runtime, args[0].bits, length)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    const char *name = reinterpret_cast<const char *>(runtime.memory() + args[0].bits);
    int32_t result = -1;
    if (!is_well_known_name(name, length) || owned_cap(SCHED_CAP_PORTBIND, "PORT_BIND"))
        result = port_bind(sched_current_sid(), name, length);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_port_send(void *, wasm::Runtime &runtime,
                                       const wasm::Value *args, wasm::Value *results,
                                       uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t length = static_cast<uint32_t>(args[2].bits);
    if (!owned_range(runtime, args[1].bits, length)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = port_send(sched_current_sid(), static_cast<int32_t>(args[0].bits),
                               runtime.memory() + args[1].bits, length);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_port_recv(void *, wasm::Runtime &runtime,
                                       const wasm::Value *args, wasm::Value *results,
                                       uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t capacity = static_cast<uint32_t>(args[2].bits);
    if (!owned_range(runtime, args[1].bits, capacity)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = port_recv(sched_current_sid(), static_cast<int32_t>(args[0].bits),
                               runtime.memory() + args[1].bits, capacity);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_blk_read(void *, wasm::Runtime &runtime,
                                      const wasm::Value *args, wasm::Value *results,
                                      uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t count = static_cast<uint32_t>(args[2].bits);
    uint64_t bytes = static_cast<uint64_t>(count) * 512;
    if (!owned_range(runtime, args[1].bits, bytes)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = -1;
    if (owned_cap(SCHED_CAP_FSADM, "BLK_RW"))
        result = devblk_rw(sched_current_sid(), 0, static_cast<uint32_t>(args[0].bits),
                           runtime.memory() + args[1].bits, count);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

/* kern_blk_geometry(out_ptr) -> i32 writes {u64 sectors, u32 blk_size, u32 pad}
 * at out_ptr and returns 0, or -1. Capacity is a property of the device, so
 * the guest must ask the device: reading a filesystem header at LBA 0 yields
 * the size of that filesystem instead, which truncates any disk carrying more
 * than one volume and puts real data out of reach. Capability checked, same
 * as kern_blk_read/write. */
static wasm::RunStatus owned_blk_geometry(void *, wasm::Runtime &runtime,
                                          const wasm::Value *args,
                                          wasm::Value *results,
                                          uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    if (!owned_range(runtime, args[0].bits, 16))
        return wasm::RunStatus::Trapped;
    extern uint64_t devblk_total_sectors(void);
    uint64_t sectors = devblk_total_sectors();
    uint32_t blk = 512;
    uint8_t *p = runtime.memory() + args[0].bits;
    for (int i = 0; i < 8; i++)
        p[i] = (uint8_t)(sectors >> (8 * i));
    for (int i = 0; i < 4; i++)
        p[8 + i] = (uint8_t)(blk >> (8 * i));
    p[12] = p[13] = p[14] = p[15] = 0;
    return owned_result_i32(results, result_count, 0)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_blk_write(void *, wasm::Runtime &runtime,
                                       const wasm::Value *args, wasm::Value *results,
                                       uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t count = static_cast<uint32_t>(args[2].bits);
    uint64_t bytes = static_cast<uint64_t>(count) * 512;
    if (!owned_range(runtime, args[1].bits, bytes)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = -1;
    if (owned_cap(SCHED_CAP_FSADM, "BLK_RW"))
        result = devblk_rw(sched_current_sid(), 1, static_cast<uint32_t>(args[0].bits),
                           runtime.memory() + args[1].bits, count);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_input_recv(void *, wasm::Runtime &runtime,
                                        const wasm::Value *args, wasm::Value *results,
                                        uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t capacity = static_cast<uint32_t>(args[1].bits);
    if (!owned_range(runtime, args[0].bits, capacity)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = 0;
    if (owned_cap(SCHED_CAP_FOCUS, "FOCUS_RECV"))
        result = input_recv(sched_current_sid(), runtime.memory() + args[0].bits, capacity);
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_focus_set_void(void *, wasm::Runtime &,
                                           const wasm::Value *args, wasm::Value *,
                                           uint32_t result_count, uint32_t *) {
    if (result_count != 0 || !args)
        return wasm::RunStatus::Error;
    if (owned_cap(SCHED_CAP_FOCUS, "FOCUS_SET"))
        input_focus_set(sched_current_sid(), static_cast<int32_t>(args[0].bits));
    return wasm::RunStatus::Completed;
}

static wasm::RunStatus owned_focus_set_i32(void *, wasm::Runtime &,
                                           const wasm::Value *args, wasm::Value *results,
                                           uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = -1;
    if (owned_cap(SCHED_CAP_FOCUS, "FOCUS_SET"))
        result = input_focus_set(sched_current_sid(), static_cast<int32_t>(args[0].bits));
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static bool owned_pci_access(uint32_t bus, uint32_t dev, uint32_t fn,
                             const char *operation) {
    uint32_t sid = sched_current_sid();
    if (!owned_cap(SCHED_CAP_PCI, operation))
        return false;
    if (vfio_bdf_assigned_to(sid, bus, dev, fn)) {
        return true;
    }
    kernsvc_audit(sid, operation, "assign", "pci");
    return false;
}

static wasm::RunStatus owned_pci_read32(void *, wasm::Runtime &, const wasm::Value *args,
                                        wasm::Value *results, uint32_t result_count,
                                        uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    uint32_t offset = static_cast<uint32_t>(args[3].bits);
    int32_t result = owned_pci_access(bus, dev, fn, "PCI_READ")
                          ? pci_read32(bus, dev, fn, offset) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_write32(void *, wasm::Runtime &, const wasm::Value *args,
                                         wasm::Value *results, uint32_t result_count,
                                         uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    uint32_t offset = static_cast<uint32_t>(args[3].bits);
    uint32_t value = static_cast<uint32_t>(args[4].bits);
    int32_t result = owned_pci_access(bus, dev, fn, "PCI_WRITE")
                          ? pci_write32(bus, dev, fn, offset, value) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

/* Mediated BAR access: the guest names BDF + BAR + offset and the kernel
 * performs the cycle at the physical address. A guest pointer cannot work
 * here (rule 2: one flat identity map, so the window cannot be aliased onto
 * the device), which is why the drivers use these instead of dereferencing
 * the window. See core/vfio.cc vfio_bar_resolve for the two checks. */
static bool owned_bar_args(const wasm::Value *args, uint32_t *bus, uint32_t *dev,
                           uint32_t *fn, uint32_t *bar, uint32_t *off) {
    if (!args)
        return false;
    *bus = static_cast<uint32_t>(args[0].bits);
    *dev = static_cast<uint32_t>(args[1].bits);
    *fn = static_cast<uint32_t>(args[2].bits);
    *bar = static_cast<uint32_t>(args[3].bits);
    *off = static_cast<uint32_t>(args[4].bits);
    return true;
}

static wasm::RunStatus owned_pci_bar_read(void *ctx, wasm::Runtime &rt,
                                          const wasm::Value *args, wasm::Value *results,
                                          uint32_t result_count, uint32_t width) {
    (void)ctx; (void)rt;
    uint32_t bus, dev, fn, bar, off;
    if (!owned_bar_args(args, &bus, &dev, &fn, &bar, &off))
        return wasm::RunStatus::Error;
    if (!owned_cap(SCHED_CAP_PCI, "PCI_BAR_READ"))
        return owned_result_i64(results, result_count, -1) ? wasm::RunStatus::Completed
                                                           : wasm::RunStatus::Error;
    int64_t value = vfio_bar_read(sched_current_sid(), bus, dev, fn, bar, off, width);
    return owned_result_i64(results, result_count, value) ? wasm::RunStatus::Completed
                                                         : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_bar_write(void *ctx, wasm::Runtime &rt,
                                           const wasm::Value *args, wasm::Value *results,
                                           uint32_t result_count, uint32_t width) {
    (void)ctx; (void)rt;
    uint32_t bus, dev, fn, bar, off;
    if (!owned_bar_args(args, &bus, &dev, &fn, &bar, &off))
        return wasm::RunStatus::Error;
    uint32_t value = args && args[5].bits ? static_cast<uint32_t>(args[5].bits) : 0;
    if (!owned_cap(SCHED_CAP_PCI, "PCI_BAR_WRITE"))
        return owned_result_i32(results, result_count, -1) ? wasm::RunStatus::Completed
                                                           : wasm::RunStatus::Error;
    int32_t rc = vfio_bar_write(sched_current_sid(), bus, dev, fn, bar, off, width, value);
    return owned_result_i32(results, result_count, rc) ? wasm::RunStatus::Completed
                                                       : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_bar_read8(void *c, wasm::Runtime &r, const wasm::Value *a,
                                           wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_read(c, r, a, o, rc, 1);
}
static wasm::RunStatus owned_pci_bar_read16(void *c, wasm::Runtime &r, const wasm::Value *a,
                                            wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_read(c, r, a, o, rc, 2);
}
static wasm::RunStatus owned_pci_bar_read32(void *c, wasm::Runtime &r, const wasm::Value *a,
                                            wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_read(c, r, a, o, rc, 4);
}
static wasm::RunStatus owned_pci_bar_write8(void *c, wasm::Runtime &r, const wasm::Value *a,
                                             wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_write(c, r, a, o, rc, 1);
}
static wasm::RunStatus owned_pci_bar_write16(void *c, wasm::Runtime &r, const wasm::Value *a,
                                              wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_write(c, r, a, o, rc, 2);
}
static wasm::RunStatus owned_pci_bar_write32(void *c, wasm::Runtime &r, const wasm::Value *a,
                                              wasm::Value *o, uint32_t rc, uint32_t *) {
    return owned_pci_bar_write(c, r, a, o, rc, 4);
}

/* Device-visible memory. The guest asks for a range of its own linear memory
 * to be made reachable for a device; the kernel maps it into the device's
 * IOMMU domain or refuses. -1 is a refusal, not an error to retry: there is
 * no buffer the kernel is willing to hand over. */
static wasm::RunStatus owned_pci_dma_map(void *, wasm::Runtime &, const wasm::Value *args,
                                         wasm::Value *results, uint32_t result_count,
                                         uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    uint64_t off = static_cast<uint64_t>(args[3].bits);
    uint32_t size = static_cast<uint32_t>(args[4].bits);
    int32_t rc = owned_cap(SCHED_CAP_PCI, "PCI_DMA_MAP")
                     ? vfio_dma_map(sched_current_sid(), bus, dev, fn, off, size)
                     : -1;
    return owned_result_i32(results, result_count, rc) ? wasm::RunStatus::Completed
                                                       : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_dma_unmap(void *, wasm::Runtime &, const wasm::Value *args,
                                           wasm::Value *results, uint32_t result_count,
                                           uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    uint64_t off = static_cast<uint64_t>(args[3].bits);
    uint32_t size = static_cast<uint32_t>(args[4].bits);
    int32_t rc = vfio_dma_unmap(sched_current_sid(), bus, dev, fn, off, size);
    return owned_result_i32(results, result_count, rc) ? wasm::RunStatus::Completed
                                                       : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_map_bar(void *, wasm::Runtime &, const wasm::Value *args,
                                         wasm::Value *results, uint32_t result_count,
                                         uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    uint32_t bar = static_cast<uint32_t>(args[3].bits);
    int64_t result = owned_cap(SCHED_CAP_PCI, "PCI_MAP")
                         ? vfio_map_bar(sched_current_sid(), bus, dev, fn, bar) : -1;
    return owned_result_i64(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_unmap_bar(void *, wasm::Runtime &, const wasm::Value *args,
                                           wasm::Value *results, uint32_t result_count,
                                           uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = vfio_unmap_bar(sched_current_sid(),
                                    static_cast<uint32_t>(args[0].bits),
                                    static_cast<uint32_t>(args[1].bits),
                                    static_cast<uint32_t>(args[2].bits),
                                    static_cast<uint32_t>(args[3].bits));
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_enable_busmaster(void *, wasm::Runtime &,
                                                   const wasm::Value *args,
                                                   wasm::Value *results,
                                                   uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    int32_t result = owned_pci_access(bus, dev, fn, "PCI_BMASTER")
                          ? pci_enable_busmaster(bus, dev, fn) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_bind_irq(void *, wasm::Runtime &,
                                          const wasm::Value *args, wasm::Value *results,
                                          uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = -1;
    if (owned_cap(SCHED_CAP_PCI, "PCI_IRQ"))
        result = vfio_bind_irq(sched_current_sid(), static_cast<uint32_t>(args[0].bits),
                               static_cast<uint32_t>(args[1].bits),
                               static_cast<uint32_t>(args[2].bits),
                               static_cast<uint32_t>(args[3].bits));
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_pci_flr(void *, wasm::Runtime &, const wasm::Value *args,
                                     wasm::Value *results, uint32_t result_count,
                                     uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t bus = static_cast<uint32_t>(args[0].bits);
    uint32_t dev = static_cast<uint32_t>(args[1].bits);
    uint32_t fn = static_cast<uint32_t>(args[2].bits);
    int32_t result = owned_pci_access(bus, dev, fn, "PCI_FLR") ? pci_flr(bus, dev, fn) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_fb_set_mode(void *, wasm::Runtime &, const wasm::Value *args,
                                         wasm::Value *results, uint32_t result_count,
                                         uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = owned_cap(SCHED_CAP_FB, "FB_MODE")
                          ? vfio_fb_set_mode(sched_current_sid(),
                                             static_cast<uint32_t>(args[0].bits),
                                             static_cast<uint32_t>(args[1].bits),
                                             static_cast<uint32_t>(args[2].bits)) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_fb_set_cursor(void *, wasm::Runtime &, const wasm::Value *args,
                                           wasm::Value *results, uint32_t result_count,
                                           uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = owned_cap(SCHED_CAP_FB, "FB_CURSOR")
                          ? vfio_fb_set_cursor(sched_current_sid(),
                                               static_cast<uint32_t>(args[0].bits),
                                               static_cast<uint32_t>(args[1].bits)) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_fb_present(void *, wasm::Runtime &, const wasm::Value *,
                                        wasm::Value *results, uint32_t result_count,
                                        uint32_t *) {
    int32_t result = owned_cap(SCHED_CAP_FB, "FB_PRESENT")
                          ? vfio_fb_present(sched_current_sid()) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_doorbell_wait(void *, wasm::Runtime &, const wasm::Value *args,
                                           wasm::Value *results, uint32_t result_count,
                                           uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    int32_t result = owned_cap(SCHED_CAP_DOORBELL, "DOORBELL")
                          ? vfio_doorbell_wait(sched_current_sid(),
                                               static_cast<uint32_t>(args[0].bits),
                                               static_cast<uint32_t>(args[1].bits)) : -1;
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_uart_write_byte(void *, wasm::Runtime &,
                                              const wasm::Value *args,
                                              wasm::Value *results,
                                              uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    console_putc(static_cast<char>(args[0].bits));
    return owned_result_i32(results, result_count, 1)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_uart_write(void *, wasm::Runtime &runtime,
                                         const wasm::Value *args, wasm::Value *results,
                                         uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t length = static_cast<uint32_t>(args[1].bits);
    if (!owned_range(runtime, args[0].bits, length)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    for (uint32_t index = 0; index < length; ++index)
        console_putc(static_cast<char>(runtime.memory()[args[0].bits + index]));
    return owned_result_i32(results, result_count, static_cast<int32_t>(length))
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_uart_read(void *, wasm::Runtime &runtime,
                                        const wasm::Value *args, wasm::Value *results,
                                        uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t capacity = static_cast<uint32_t>(args[1].bits);
    if (!owned_range(runtime, args[0].bits, capacity)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = 0;
    if (capacity != 0 && console_rx_ready()) {
        runtime.memory()[args[0].bits] = static_cast<uint8_t>(console_rx_byte());
        result = 1;
    }
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static wasm::RunStatus owned_vmware_backdoor(void *, wasm::Runtime &runtime,
                                              const wasm::Value *args,
                                              wasm::Value *results,
                                              uint32_t result_count, uint32_t *) {
    if (!args)
        return wasm::RunStatus::Error;
    uint32_t op = static_cast<uint32_t>(args[0].bits);
    uint64_t low_address = args[1].bits;
    uint64_t high_address = args[2].bits;
    if (!owned_range(runtime, low_address, 4) || !owned_range(runtime, high_address, 4)) {
        runtime.set_error(wasm::Status::TrapOutOfBounds);
        return wasm::RunStatus::Trapped;
    }
    int32_t result = -1;
    if (owned_cap(SCHED_CAP_VMWARE, "VMWARE")) {
        if (op == 0) {
            result = vmware_backdoor_present() ? 0 : -1;
        } else if (op == 1) {
            uint32_t low = 0;
            uint32_t high = 0;
            vmware_backdoor_get_time(&low, &high);
            runtime.memory()[low_address] = static_cast<uint8_t>(low);
            runtime.memory()[low_address + 1] = static_cast<uint8_t>(low >> 8);
            runtime.memory()[low_address + 2] = static_cast<uint8_t>(low >> 16);
            runtime.memory()[low_address + 3] = static_cast<uint8_t>(low >> 24);
            runtime.memory()[high_address] = static_cast<uint8_t>(high);
            runtime.memory()[high_address + 1] = static_cast<uint8_t>(high >> 8);
            runtime.memory()[high_address + 2] = static_cast<uint8_t>(high >> 16);
            runtime.memory()[high_address + 3] = static_cast<uint8_t>(high >> 24);
            result = static_cast<int32_t>(low);
        }
    }
    return owned_result_i32(results, result_count, result)
               ? wasm::RunStatus::Completed : wasm::RunStatus::Error;
}

static bool owned_kernel_import(const wasm::Module &module, const char *field) {
    const wasm::ModuleData &data = module.data();
    for (uint32_t index = 0; index < data.import_count; ++index) {
        const wasm::Import &import = data.imports[index];
        if (import.kind != 0 || import.module_name.size != 6 ||
            __builtin_memcmp(import.module_name.data, "kernel", 6) != 0 ||
            import.field_name.size != __builtin_strlen(field) ||
            __builtin_memcmp(import.field_name.data, field, import.field_name.size) != 0)
            continue;
        return true;
    }
    return false;
}

/* `i64_params` names which parameters are 64-bit; everything else is i32.
 * Needed for the device-visible memory calls, whose guest offset is a byte
 * offset into linear memory and does not fit in 32 bits on a 64 MiB guest
 * once the window is high enough. Without it the signature could not be
 * expressed and every module importing it failed to bind with TypeMismatch. */
static wasm::FuncType owned_kernel_type(uint32_t params, uint32_t results,
                                        bool result_i64 = false,
                                        uint32_t i64_params = 0,
                                        uint32_t i64_mask = 0) {
    wasm::FuncType type{};
    type.param_count = params;
    for (uint32_t index = 0; index < params; ++index)
        type.params[index] =
            (i64_params > index && (i64_mask & (1u << index))) ? wasm::ValueType::I64
                                                              : wasm::ValueType::I32;
    type.result_count = results;
    if (results != 0)
        type.results[0] = result_i64 ? wasm::ValueType::I64 : wasm::ValueType::I32;
    return type;
}

static wasm::Status bind_owned_kernel(wasm::OwnedSession &session) {
    struct Entry {
        const char *field;
        uint32_t params;
        uint32_t results;
        bool result_i64;
        wasm::ImportInvoke invoke;
        /* Optional trailing fields: which parameters are 64-bit. Left at 0
         * by every entry that does not need them, so the positional
         * initialisers above keep working. The default-member initialisers
         * make "left at 0" literal and silence -Wmissing-field-initializers
         * for the entries that omit these fields. */
        uint32_t i64_params = 0;
        uint32_t i64_mask = 0;
    };
    const Entry entries[] = {
        {"kern_port_create", 2, 1, false, owned_port_create},
        {"kern_port_bind", 2, 1, false, owned_port_bind},
        {"kern_port_send", 3, 1, false, owned_port_send},
        {"kern_port_recv", 3, 1, false, owned_port_recv},
        {"kern_blk_read", 3, 1, false, owned_blk_read},
        {"kern_blk_geometry", 1, 1, false, owned_blk_geometry},
        {"kern_blk_write", 3, 1, false, owned_blk_write},
        {"kern_input_recv", 2, 1, false, owned_input_recv},
        {"kern_pci_read32", 4, 1, false, owned_pci_read32},
        {"kern_pci_write32", 5, 1, false, owned_pci_write32},
        {"kern_pci_bar_read8", 5, 1, true, owned_pci_bar_read8},
        {"kern_pci_bar_read16", 5, 1, true, owned_pci_bar_read16},
        {"kern_pci_bar_read32", 5, 1, true, owned_pci_bar_read32},
        {"kern_pci_bar_write8", 6, 1, false, owned_pci_bar_write8},
        {"kern_pci_bar_write16", 6, 1, false, owned_pci_bar_write16},
        {"kern_pci_bar_write32", 6, 1, false, owned_pci_bar_write32},
        /* (bus, dev, fn, off:i64, size) */
        {"kern_pci_dma_map", 5, 1, false, owned_pci_dma_map, 4, 1u << 3},
        {"kern_pci_dma_unmap", 5, 1, false, owned_pci_dma_unmap, 4, 1u << 3},
        {"kern_pci_map_bar", 4, 1, true, owned_pci_map_bar},
        {"kern_pci_unmap_bar", 4, 1, false, owned_pci_unmap_bar},
        {"kern_pci_enable_busmaster", 3, 1, false, owned_pci_enable_busmaster},
        {"kern_pci_bind_irq", 4, 1, false, owned_pci_bind_irq},
        {"kern_pci_flr", 3, 1, false, owned_pci_flr},
        {"kern_fb_set_mode", 3, 1, false, owned_fb_set_mode},
        {"kern_fb_set_cursor", 2, 1, false, owned_fb_set_cursor},
        {"kern_fb_present", 0, 1, false, owned_fb_present},
        {"kern_doorbell_wait", 2, 1, false, owned_doorbell_wait},
        {"kern_uart_read", 2, 1, false, owned_uart_read},
        {"kern_vmware_backdoor", 3, 1, false, owned_vmware_backdoor},
    };
    for (const Entry &entry : entries) {
        if (!owned_kernel_import(session.module(), entry.field))
            continue;
        wasm::FuncType type = owned_kernel_type(entry.params, entry.results,
                                                 entry.result_i64, entry.i64_params,
                                                 entry.i64_mask);
        wasm::Status status = session.bind_import_typed("kernel", entry.field, &type,
                                                        entry.invoke, nullptr);
        if (status != wasm::Status::Ok)
            return status;
    }
    if (owned_kernel_import(session.module(), "kern_focus_set")) {
        const wasm::ModuleData &data = session.module().data();
        bool void_result = false;
        for (uint32_t index = 0; index < data.import_count; ++index) {
            const wasm::Import &import = data.imports[index];
            if (import.kind == 0 && import.module_name.size == 6 &&
                __builtin_memcmp(import.module_name.data, "kernel", 6) == 0 &&
                import.field_name.size == 14 &&
                __builtin_memcmp(import.field_name.data, "kern_focus_set", 14) == 0) {
                void_result = data.types[import.type_index].result_count == 0;
                break;
            }
        }
        wasm::FuncType type = owned_kernel_type(1, void_result ? 0 : 1);
        wasm::Status status = session.bind_import_typed(
            "kernel", "kern_focus_set", &type,
            void_result ? owned_focus_set_void : owned_focus_set_i32, nullptr);
        if (status != wasm::Status::Ok)
            return status;
    }
    if (owned_kernel_import(session.module(), "kern_uart_write")) {
        const wasm::ModuleData &data = session.module().data();
        bool one_byte = false;
        for (uint32_t index = 0; index < data.import_count; ++index) {
            const wasm::Import &import = data.imports[index];
            if (import.kind == 0 && import.module_name.size == 6 &&
                __builtin_memcmp(import.module_name.data, "kernel", 6) == 0 &&
                import.field_name.size == 15 &&
                __builtin_memcmp(import.field_name.data, "kern_uart_write", 15) == 0) {
                one_byte = data.types[import.type_index].param_count == 1;
                break;
            }
        }
        wasm::FuncType type = owned_kernel_type(one_byte ? 1 : 2, 1);
        wasm::Status status = session.bind_import_typed(
            "kernel", "kern_uart_write", &type,
            one_byte ? owned_uart_write_byte : owned_uart_write, nullptr);
        if (status != wasm::Status::Ok)
            return status;
    }
    return wasm::Status::Ok;
}

int engine_init(struct engine *e, const uint8_t *blob, uint64_t len) {
    if (!e || !blob || len < 8)
        return 1;
    e->owned = 0;
    void *memory = rt_malloc(sizeof(OwnedEngineState));
    if (!memory)
        return -1;
    OwnedEngineState *state = new (memory) OwnedEngineState();
    wasm::RuntimeConfig config{};
    config.max_values = 1u << 16;
    config.max_frames = 2048;
    config.max_labels = 4096;
    /* Go emits a table entry per indirectly-callable function: the shipped
     * services need ~2-3k, and the largest Go artifact in the tree
     * (services/goc/go.wasm, 13431 functions) declares 17527 entries and
     * was rejected outright by the old 1<<14 cap. The cap exists to bound
     * the arena (4 bytes/entry), so 1<<18 keeps that bound at 1 MiB per
     * module while leaving 15x headroom over the biggest real artifact. */
    config.max_table_entries = 1u << 18;
    config.max_memory_bytes = 64u * 1024u * 1024u;
    wasm::Status status = state->session.init_view(blob, len, owned_alloc, owned_free,
                                                   nullptr, config);
    if (status != wasm::Status::Ok) {
        /* Silent failure here is what made "init failed for 'shell'"
         * unactionable: this is the one path that reported nothing. */
        console_puts("[engine] owned module init status=");
        console_hex64(static_cast<uint64_t>(status));
        console_puts("\n");
        state->~OwnedEngineState();
        rt_free(memory);
        return 1;
    }
    state->random_state = 0x9e3779b97f4a7c15ULL;
    uint32_t mask = wasm::owned_wasi_import_mask(state->session.module());
    state->wasi = wasm::OwnedWasiContext{state,        owned_write,
                                         owned_exit,  owned_yield,
                                         owned_now_ns, owned_random,
                                         owned_path_open, owned_mkdir,
                                         owned_read_fd, owned_close_fd,
                                         nullptr,      0};
    if (mask != 0) {
        char stub_log[192];
        stub_log[0] = 0;
        status = wasm::bind_owned_wasi(state->session, state->wasi, mask,
                                       stub_log, sizeof(stub_log));
        if (stub_log[0]) {
            /* Same visibility the m3 bridge gave these: a guest that links
             * an out-of-profile preview1 import still runs, but the name is
             * on the serial log so nobody mistakes "it linked" for "it
             * works". */
            console_puts("[wasi] generic stub: ");
            console_puts(stub_log);
            console_puts("\n");
        }
        if (status != wasm::Status::Ok) {
            console_puts("[engine] owned wasi bind status=");
            console_hex64(static_cast<uint64_t>(status));
            console_puts("\n");
            state->~OwnedEngineState();
            rt_free(memory);
            return 1;
        }
    }
    status = bind_owned_kernel(state->session);
    if (status != wasm::Status::Ok) {
        console_puts("[engine] owned kernel bind status=");
        console_hex64(static_cast<uint64_t>(status));
        console_puts("\n");
        state->~OwnedEngineState();
        rt_free(memory);
        return 1;
    }
    if (!state->session.runtime().imports_bound()) {
        console_puts("[engine] owned module has unbound imports\n");
        state->~OwnedEngineState();
        rt_free(memory);
        return 1;
    }
    e->owned = state;
    console_puts("[engine] owned backend initialized used=");
    console_hex64(mm_used_bytes() / (1024 * 1024));
    console_puts("MiB\n");
    return 0;
}

int engine_set_args(struct engine *e, const char *const *args, uint32_t count) {
    if (!e || !e->owned)
        return -1;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    state->wasi.args = args;
    state->wasi.arg_count = count;
    return 0;
}

void engine_request_preempt(struct engine *e) {
    if (!e || !e->owned)
        return;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    state->session.request_preempt();
}

uint8_t *engine_memory(struct engine *e, uint32_t *size) {
    if (!e)
        return nullptr;
    if (!e || !e->owned)
        return nullptr;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    if (size)
        *size = static_cast<uint32_t>(state->session.runtime().memory_size());
    return state->session.runtime().memory();
}

int engine_ensure_memory(struct engine *e, uint32_t min_bytes) {
    if (!e)
        return -1;
    if (!e || !e->owned)
        return -1;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    return state->session.runtime().ensure_memory_bytes(min_bytes) ? 0 : -1;
}

int engine_start(struct engine *e) {
    if (!e || !e->owned)
        return -1;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    bool started = false;
    for (;;) {
        wasm::RunStatus status = started
                                      ? state->session.resume(nullptr, 0)
                                      : state->session.start(nullptr, 0);
        if (status == wasm::RunStatus::Completed || status == wasm::RunStatus::Exited)
            return 0;
        if (status == wasm::RunStatus::Yield) {
            started = true;
            sched_yield_current();
            continue;
        }
        console_puts("[engine] owned runtime trapped or errored status=");
        console_hex64(static_cast<uint64_t>(status));
        console_puts(" error=");
        console_hex64(static_cast<uint64_t>(state->session.runtime().error()));
        /* Location matters: "the guest trapped" cannot be triaged, and
         * for an engine under development the first question is always
         * which function and which instruction. */
        console_puts(" at func=");
        console_hex64(state->session.runtime().trap_function());
        console_puts(" pc=");
        console_hex64(state->session.runtime().trap_pc());
        uint32_t failed_import = state->session.runtime().error_import();
        if (failed_import != wasm::kNoImport) {
            /* Distinguish "the guest faulted" from "a host import refused":
             * the guest often handles a refused call and carries on, so
             * blaming the guest here sends triage in the wrong
             * direction. */
            console_puts(" via_import=");
            console_hex64(failed_import);
        }
        console_puts("\n");
        return 2;
    }
}

const char *engine_errstr(struct engine *e) {
    return (e && e->owned) ? "owned runtime error" : "no runtime";
}

void engine_shutdown(struct engine *e) {
    if (!e || !e->owned)
        return;
    OwnedEngineState *state = static_cast<OwnedEngineState *>(e->owned);
    state->~OwnedEngineState();
    rt_free(state);
    e->owned = 0;
}
