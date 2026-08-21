#include "efi.h"
#include "plat.h"
#include "loader.h"
#include "boot.h"

static EFI_SYSTEM_TABLE *g_st;

static CHAR16 obuf[256];

static void conout(const char *s) {
    UINTN i = 0;
    while (*s && i < 255)
        obuf[i++] = (CHAR16)*s++;
    obuf[i] = 0;
    FW2(g_st->ConOut->OutputString, (UINTN)g_st->ConOut, (UINTN)obuf);
}

static struct boot_info g_bi;

extern "C" EFI_STATUS __attribute__((ms_abi)) efi_main(EFI_HANDLE image_handle,
                                                       EFI_SYSTEM_TABLE *systab) {
    g_st = systab;

    console_init();
    console_puts("\n[boot] microkernel UEFI stage\n");
    conout("microkernel: UEFI boot OK\r\n");

    FW4(systab->BootServices->SetWatchdogTimer, 0, 0, 0, 0);

    void *prog = 0;
    uint64_t prog_len = 0;
    console_puts("[boot] loading guest program\n");
    load_program(image_handle, systab, &prog, &prog_len);

    static uint8_t mmapbuf[16384];
    UINTN msize = sizeof(mmapbuf), dsize, key;
    UINT32 dver;
    EFI_STATUS st;
    do {
        st = FW5(systab->BootServices->GetMemoryMap, (UINTN)&msize,
                 (UINTN)mmapbuf, (UINTN)&key, (UINTN)&dsize, (UINTN)&dver);
        if (st != EFI_SUCCESS && st != EFI_BUFFER_TOO_SMALL) {
            console_puts("[boot] GetMemoryMap failed\n");
            return st;
        }
    } while (st != EFI_SUCCESS);

    st = FW2(systab->BootServices->ExitBootServices, (UINTN)image_handle, key);
    if (st != EFI_SUCCESS) {
        console_puts("[boot] ExitBootServices failed\n");
        return st;
    }
    console_puts("[boot] boot services exited\n");

    g_bi.magic = 0x424D5442; /* "BMTB" */
    g_bi.mmap_desc = (uint64_t)(uintptr_t)mmapbuf;
    g_bi.mmap_count = msize / dsize;
    g_bi.mmap_dsize = dsize;
    g_bi.prog = (uint64_t)(uintptr_t)prog;
    g_bi.prog_len = prog_len;

    console_puts("[boot] entering microkernel\n");
    kmain(&g_bi);
    cpu_halt();
    return EFI_SUCCESS;
}
