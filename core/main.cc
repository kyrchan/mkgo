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

    static CHAR16 pcon[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                            'c','o','n','s','o','l','e','.','w','a','s','m',0};
    static CHAR16 plog[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                            'l','o','g','i','n','.','w','a','s','m',0};
    static CHAR16 pfs[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                           'f','s','.','w','a','s','m',0};
    static CHAR16 pini[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                            'i','n','i','t','.','w','a','s','m',0};
    static CHAR16 psh[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                           's','h','e','l','l','.','w','a','s','m',0};
    static CHAR16 pnet[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                            'n','e','t','.','w','a','s','m',0};
    static CHAR16 pp9[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                           'p','9','.','w','a','s','m',0};
    static CHAR16 pconf[] = {'e','t','c','\\','i','n','i','t','.','c','o','n','f',0};
    static CHAR16 papp2[] = {'v','m','\\','a','p','p','2',0};
    static CHAR16 pgate[] = {'v','m','\\','g','a','t','e',0};
    void *cimg = 0, *limg = 0, *fimg = 0, *a2img = 0, *iimg = 0, *shimg = 0,
         *cfimg = 0, *nimg = 0, *p9img = 0, *gateimg = 0;
    uint64_t clen = 0, llen = 0, flen = 0, a2len = 0, ilen = 0, shlen = 0,
             cflen = 0, nlen = 0, p9len = 0, gatelen = 0;
    load_esp_file(image_handle, systab, pcon, &cimg, &clen);
    load_esp_file(image_handle, systab, plog, &limg, &llen);
    load_esp_file(image_handle, systab, pfs, &fimg, &flen);
    load_esp_file(image_handle, systab, pini, &iimg, &ilen);
    load_esp_file(image_handle, systab, psh, &shimg, &shlen);
    load_esp_file(image_handle, systab, pnet, &nimg, &nlen);
    load_esp_file(image_handle, systab, pp9, &p9img, &p9len);
    load_esp_file(image_handle, systab, papp2, &a2img, &a2len);
    load_esp_file(image_handle, systab, pconf, &cfimg, &cflen);
    load_esp_file(image_handle, systab, pgate, &gateimg, &gatelen);
    g_bi.mod_console = (uint64_t)(uintptr_t)cimg;
    g_bi.mod_console_len = clen;
    g_bi.mod_login = (uint64_t)(uintptr_t)limg;
    g_bi.mod_login_len = llen;
    g_bi.mod_fs = (uint64_t)(uintptr_t)fimg;
    g_bi.mod_fs_len = flen;
    g_bi.mod_init = (uint64_t)(uintptr_t)iimg;
    g_bi.mod_init_len = ilen;
    g_bi.mod_shell = (uint64_t)(uintptr_t)shimg;
    g_bi.mod_shell_len = shlen;
    g_bi.mod_net = (uint64_t)(uintptr_t)nimg;
    g_bi.mod_net_len = nlen;
    g_bi.mod_p9 = (uint64_t)(uintptr_t)p9img;
    g_bi.mod_p9_len = p9len;
    g_bi.conf = (uint64_t)(uintptr_t)cfimg;
    g_bi.conf_len = cflen;
    g_bi.prog2 = (uint64_t)(uintptr_t)a2img;
    g_bi.prog2_len = a2len;

    /* gate mask for legacy payload slots: read hex from vm/gate if present */
    g_bi.gate_mask = 0;
    if (gateimg && gatelen > 0) {
        uint64_t mask = 0;
        for (uint64_t i = 0; i < gatelen; i++) {
            char c = ((char *)(uintptr_t)gateimg)[i];
            mask <<= 4;
            if (c >= '0' && c <= '9') mask |= (c - '0');
            else if (c >= 'a' && c <= 'f') mask |= (c - 'a' + 10);
            else if (c >= 'A' && c <= 'F') mask |= (c - 'A' + 10);
            else { mask = 0; break; }
        }
        g_bi.gate_mask = mask;
    }

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
