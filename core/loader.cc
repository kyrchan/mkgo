#include "loader.h"
#include "plat.h"

static EFI_GUID sfs_guid = EFI_SIMPLE_FILE_SYSTEM_PROTOCOL_GUID;

static const CHAR16 *g_path;
static CHAR16 def_path[] = {'v','m','\\','a','p','p',0};

static void *cached_sfs = 0;

static int open_volume(EFI_SYSTEM_TABLE *st, void **root_out) {
    if (!cached_sfs) {
        if (FW3(st->BootServices->LocateProtocol, (UINTN)&sfs_guid, 0,
                (UINTN)&cached_sfs) != EFI_SUCCESS || !cached_sfs) {
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
    (void)image_handle;

    void *root = 0;
    if (open_volume(st, &root) != 0)
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
