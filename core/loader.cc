#include "loader.h"
#include "plat.h"

static EFI_GUID sfs_guid = EFI_SIMPLE_FILE_SYSTEM_PROTOCOL_GUID;
static EFI_GUID loaded_image_guid =
    {0x5b1b31a1u,0x9562,0x11d2,{0x8e,0x3f,0x00,0xa0,0xc9,0x69,0x72,0x3b}};

static const CHAR16 *g_path;
static CHAR16 def_path[] = {'v','m','\\','a','p','p',0};

static void *cached_sfs = 0;

/* Resolve the filesystem protocol for the volume THIS image was loaded from.
 *
 * LocateProtocol(SimpleFileSystem) returns whichever instance the firmware
 * happens to list first and we used to cache that answer forever. With a
 * single disk that is the boot volume by luck; with two -- e.g. a boot FAT16
 * image plus a separate seeded data disk -- it can latch onto the empty data
 * volume, and then every OpenFile fails with "file not found" for every path
 * and the kernel halts with "no init and no payloads".
 *
 * The loaded image protocol carries the DeviceHandle of the exact volume the
 * firmware loaded us from, so ask for that first and only fall back to
 * LocateProtocol when the handle is unavailable. */
static void *locate_boot_sfs(EFI_HANDLE image_handle,
                             EFI_SYSTEM_TABLE *st) {
    void *img = 0;
    if (image_handle &&
        FW3(st->BootServices->HandleProtocol, (UINTN)image_handle,
            (UINTN)&loaded_image_guid, (UINTN)&img) == EFI_SUCCESS && img) {
        void *dev = *(void **)((char *)img + 24); /* DeviceHandle */
        void *sfs = 0;
        if (dev &&
            FW3(st->BootServices->HandleProtocol, (UINTN)dev,
                (UINTN)&sfs_guid, (UINTN)&sfs) == EFI_SUCCESS && sfs) {
            console_puts("[loader] sfs from loaded-image device handle\n");
            return sfs;
        }
        console_puts("[loader] loaded image has no SimpleFileSystem\n");
    }
    void *sfs = 0;
    if (FW3(st->BootServices->LocateProtocol, (UINTN)&sfs_guid, 0,
            (UINTN)&sfs) == EFI_SUCCESS && sfs) {
        console_puts("[loader] sfs from LocateProtocol (no device handle)\n");
        return sfs;
    }
    return 0;
}

static int open_volume(EFI_HANDLE image_handle, EFI_SYSTEM_TABLE *st,
                       void **root_out) {
    if (!cached_sfs) {
        cached_sfs = locate_boot_sfs(image_handle, st);
        if (!cached_sfs) {
            console_puts("[loader] no SimpleFileSystem protocol\n");
            return -1;
        }
    }
    EFI_STATUS ovs = FW2(*(void **)((char *)cached_sfs + 8),
                         (UINTN)cached_sfs, (UINTN)root_out);
    console_puts("[loader] openvolume st=");
    console_hex64((uint64_t)ovs);
    console_puts(" root=");
    console_hex64((uint64_t)(uintptr_t)*root_out);
    console_puts("\n");
    if (ovs != EFI_SUCCESS || !*root_out)
        return -1;
    return 0;
}

static void close_file(void *file) {
    FW1(*(void **)((char *)file + 16), (UINTN)file);
}

int load_esp_file(EFI_HANDLE image_handle, EFI_SYSTEM_TABLE *st,
                  const CHAR16 *path, void **out, uint64_t *out_len) {
    g_path = path;
    return load_program(image_handle, st, out, out_len);
}

int load_program(EFI_HANDLE image_handle, EFI_SYSTEM_TABLE *st,
                 void **out, uint64_t *out_len) {
    if (!g_path)
        g_path = def_path;
    *out = 0;
    *out_len = 0;

    void *root = 0;
    if (open_volume(image_handle, st, &root) != 0)
        return -1;

    void *file = 0;
    if (FW4(*(void **)((char *)root + 8), (UINTN)root, (UINTN)&file,
            (UINTN)g_path, 1) != EFI_SUCCESS || !file) {
        console_puts("[loader] file not found\n");
        return -1;
    }

    UINTN size = 0;
    EFI_STATUS sps = FW2(*(void **)((char *)file + 56), (UINTN)file, ~0ULL);
    EFI_STATUS gps = FW2(*(void **)((char *)file + 48), (UINTN)file,
                         (UINTN)&size);
    console_puts("[loader] setpos=");
    console_hex64((uint64_t)sps);
    console_puts(" getpos=");
    console_hex64((uint64_t)gps);
    console_puts(" size=");
    console_hex64(size);
    console_puts("\n");
    if (!size) {
        close_file(file);
        return -1;
    }

    void *buf = 0;
    EFI_STATUS aps = FW3(st->BootServices->AllocatePool, 2 /*EfiLoaderData*/,
                         size, (UINTN)&buf);
    console_puts("[loader] alloc st=");
    console_hex64((uint64_t)aps);
    console_puts(" buf=");
    console_hex64((uint64_t)(uintptr_t)buf);
    console_puts("\n");
    if (aps != EFI_SUCCESS || !buf) {
        close_file(file);
        return -1;
    }

    FW2(*(void **)((char *)file + 56), (UINTN)file, 0);
    UINTN got = size;
    if (FW3(*(void **)((char *)file + 32), (UINTN)file, (UINTN)&got,
            (UINTN)buf) != EFI_SUCCESS || got != size) {
        console_puts("[loader] short read\n");
        FW1(st->BootServices->FreePool, (UINTN)buf);
        close_file(file);
        return -1;
    }

    close_file(file);

    *out = buf;
    *out_len = size;
    console_puts("[loader] loaded ");
    console_hex64(size);
    console_puts(" bytes\n");
    return 0;
}
