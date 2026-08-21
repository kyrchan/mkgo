#include "efi.h"
#include "serial.h"
#include "cpu.h"
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

EFI_STATUS __attribute__((ms_abi)) efi_main(EFI_HANDLE image_handle,
                                            EFI_SYSTEM_TABLE *systab) {
    g_st = systab;

    serial_init();
    serial_puts("\n[boot] microkernel UEFI stage\n");
    conout("microkernel: UEFI boot OK\r\n");

    FW4(systab->BootServices->SetWatchdogTimer, 0, 0, 0, 0);

    void *prog = 0;
    uint64_t prog_len = 0;
    serial_puts("[boot] loading guest program\n");
    load_program(image_handle, systab, &prog, &prog_len);

    static uint8_t mmapbuf[16384];
    UINTN msize = sizeof(mmapbuf), dsize, key;
    UINT32 dver;
    EFI_STATUS st;
    do {
        st = FW5(systab->BootServices->GetMemoryMap, (UINTN)&msize,
                 (UINTN)mmapbuf, (UINTN)&key, (UINTN)&dsize, (UINTN)&dver);
        if (st != EFI_SUCCESS && st != EFI_BUFFER_TOO_SMALL) {
            serial_puts("[boot] GetMemoryMap failed\n");
            return st;
        }
    } while (st != EFI_SUCCESS);

    st = FW2(systab->BootServices->ExitBootServices, (UINTN)image_handle, key);
    if (st != EFI_SUCCESS) {
        serial_puts("[boot] ExitBootServices failed\n");
        return st;
    }
    serial_puts("[boot] boot services exited\n");

    g_bi.magic = 0x424D5442; /* "BMTB" */
    g_bi.mmap_desc = (uint64_t)(uintptr_t)mmapbuf;
    g_bi.mmap_count = msize / dsize;
    g_bi.mmap_dsize = dsize;
    g_bi.prog = (uint64_t)(uintptr_t)prog;
    g_bi.prog_len = prog_len;

    serial_puts("[boot] entering microkernel\n");
    kmain(&g_bi);
    for (;;)
        hlt();
    return EFI_SUCCESS;
}
