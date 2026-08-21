#include "loader.h"
#include "plat.h"

static EFI_GUID sfs_guid = EFI_SIMPLE_FILE_SYSTEM_PROTOCOL_GUID;

static CHAR16 prog_path[] = {'v','m','\\','p','r','o','g','.','v','b','i','n',0};

int load_program(EFI_HANDLE image_handle, EFI_SYSTEM_TABLE *st,
                 void **out, uint64_t *out_len) {
    *out = 0;
    *out_len = 0;
    (void)image_handle;

    void *sfs = 0;
    if (FW3(st->BootServices->LocateProtocol, (UINTN)&sfs_guid, 0,
            (UINTN)&sfs) != EFI_SUCCESS || !sfs) {
        console_puts("[loader] no SimpleFileSystem protocol\n");
        return -1;
    }

    void *root = 0;
    EFI_STATUS ovs = FW2(*(void **)((char *)sfs + 8) /* OpenVolume */,
                         (UINTN)sfs, (UINTN)&root);
    console_puts("[loader] openvolume st=");
    console_hex64((uint64_t)ovs);
    console_puts(" root=");
    console_hex64((uint64_t)(uintptr_t)root);
    console_puts("\n");
    if (ovs != EFI_SUCCESS || !root)
        return -1;

    void *file = 0;
    /* root->Open(root, &file, path, mode=READ(1), attr=0) */
    if (FW4(*(void **)((char *)root + 8), (UINTN)root, (UINTN)&file,
            (UINTN)prog_path, 1) != EFI_SUCCESS || !file) {
        console_puts("[loader] no vm/prog.vbin on ESP\n");
        return -1;
    }

    /* size: seek to EOF, ask position */
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
    if (!size)
        return -1;

    void *buf = 0;
    /* AllocatePool(Type, Size, **Buffer) -- three arguments. */
    EFI_STATUS aps = FW3(st->BootServices->AllocatePool, 2 /*EfiLoaderData*/,
                         size, (UINTN)&buf);
    console_puts("[loader] alloc st=");
    console_hex64((uint64_t)aps);
    console_puts(" buf=");
    console_hex64((uint64_t)(uintptr_t)buf);
    console_puts("\n");
    if (aps != EFI_SUCCESS || !buf)
        return -1;

    FW2(*(void **)((char *)file + 56), (UINTN)file, 0);       /* rewind */
    UINTN got = size;
    if (FW3(*(void **)((char *)file + 32), (UINTN)file, (UINTN)&got,
            (UINTN)buf) != EFI_SUCCESS || got != size) {
        console_puts("[loader] short read\n");
        return -1;
    }

    FW1(*(void **)((char *)file + 16), (UINTN)file);          /* Close */
    FW1(*(void **)((char *)root + 16), (UINTN)root);

    *out = buf;
    *out_len = size;
    console_puts("[loader] loaded ");
    console_hex64(size);
    console_puts(" bytes\n");
    return 0;
}
