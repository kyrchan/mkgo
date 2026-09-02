#include "efi.h"
#include "lib.h"
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
    static CHAR16 pgfx[] = {'b','o','o','t','\\','m','o','d','u','l','e','s','\\',
                            'g','r','a','p','h','i','c','s','.','w','a','s','m',0};
    static CHAR16 pconf[] = {'i','n','i','t','.','c','o','n','f',0};
    static CHAR16 papp2[] = {'v','m','\\','a','p','p','2',0};
    static CHAR16 pgate[] = {'v','m','\\','g','a','t','e',0};
    static CHAR16 pdef[] = {'v','m','\\','a','p','p',0};
    void *prog = 0, *cimg = 0, *limg = 0, *fimg = 0, *a2img = 0, *iimg = 0, *shimg = 0,
         *cfimg = 0, *nimg = 0, *p9img = 0, *gateimg = 0, *gimg = 0;
    uint64_t prog_len = 0, clen = 0, llen = 0, flen = 0, a2len = 0, ilen = 0, shlen = 0,
             cflen = 0, nlen = 0, p9len = 0, gatelen = 0, glen = 0;
    load_esp_file(image_handle, systab, pdef, &prog, &prog_len);
    load_esp_file(image_handle, systab, pcon, &cimg, &clen);
    load_esp_file(image_handle, systab, plog, &limg, &llen);
    load_esp_file(image_handle, systab, pfs, &fimg, &flen);
    load_esp_file(image_handle, systab, pini, &iimg, &ilen);
    load_esp_file(image_handle, systab, psh, &shimg, &shlen);
    load_esp_file(image_handle, systab, pnet, &nimg, &nlen);
    load_esp_file(image_handle, systab, pp9, &p9img, &p9len);
    load_esp_file(image_handle, systab, pgfx, &gimg, &glen);
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
    g_bi.mod_graphics = (uint64_t)(uintptr_t)gimg;
    g_bi.mod_graphics_len = glen;
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
            else { break; } /* stop at first non-hex (e.g. trailing newline) */
        }
        g_bi.gate_mask = mask;
    }

    /* Locate EFI GOP (Graphics Output Protocol) to get the firmware framebuffer.
     * This must happen before ExitBootServices. If present, vfio uses it as the
     * scanout target so guest renders reach the firmware display window. */
    g_bi.fb_phys = 0; g_bi.fb_size = 0; g_bi.fb_width = 0; g_bi.fb_height = 0; g_bi.fb_bpp = 32;
    {
        EFI_GRAPHICS_OUTPUT_PROTOCOL *gop = nullptr;
        EFI_GUID gop_guid = EFI_GRAPHICS_OUTPUT_PROTOCOL_GUID;
        EFI_STATUS gop_st = FW3(systab->BootServices->LocateProtocol,
                                (UINTN)&gop_guid, (UINTN)nullptr, (UINTN)&gop);
        if (gop_st == EFI_SUCCESS && gop && gop->Mode) {
            g_bi.fb_phys = gop->Mode->FrameBufferBase;
            g_bi.fb_size = gop->Mode->FrameBufferSize;
            /* ModeInfo pixels-per-scan-line is at offset 4 in the mode info;
             * derive width/height from framebuffer size when ModeInfo absent. */
            if (gop->Mode->InfoSize >= 20 && gop->Mode->ModeInfo) {
                uint32_t *info = (uint32_t *)(uintptr_t)gop->Mode->ModeInfo;
                g_bi.fb_width = info[1];
                g_bi.fb_height = info[2];
                g_bi.fb_bpp = 32;
                if (gop->Mode->InfoSize >= 28 && info[5] != 0)
                    g_bi.fb_stride = info[5] * 4; /* PixelsPerScanLine * bytes_per_px */
                console_puts("[boot] GOP mode ");
                console_hex64(g_bi.fb_width);
                console_puts("x");
                console_hex64(g_bi.fb_height);
                console_puts(" stride=");
                console_hex64(g_bi.fb_stride);
                console_puts("\n");
            } else if (g_bi.fb_size > 0) {
                g_bi.fb_width = 1024; g_bi.fb_height = 768;
            }
            console_puts("[boot] GOP framebuffer phys=");
            console_hex64(g_bi.fb_phys);
            console_puts(" size=");
            console_hex64(g_bi.fb_size);
            console_puts("\n");
        } else {
            console_puts("[boot] GOP not found (headless or firmware has no display)\n");
        }
    }

    /* Locate the ACPI MADT for AP bring-up (Phase 8.2). We walk the
     * EFI Configuration Tables looking for the RSDP, then the RSDT/XSDT
     * for the MADT. The MADT lives in EfiACPIMemoryNVS so it survives
     * ExitBootServices. */
    g_bi.madt_phys = 0;
    {
        EFI_GUID rsdp_guid = EFI_ACPI_RSDP_GUID;
        EFI_RSDP *rsdp = 0;
        /* The EFI System Table has a ConfigurationTable field. We walk
         * it looking for the RSDP GUID. */
        void *ct = systab->ConfigurationTable;
        if (ct) {
            for (int i = 0; i < 64; i++) {
                EFI_CONFIG_TABLE_ENTRY *e = (EFI_CONFIG_TABLE_ENTRY *)
                    ((uint8_t *)ct + i * sizeof(EFI_CONFIG_TABLE_ENTRY));
                if (e->guid.a == rsdp_guid.a && e->guid.b == rsdp_guid.b &&
                    e->guid.c == rsdp_guid.c &&
                    e->guid.d[0] == rsdp_guid.d[0] &&
                    e->guid.d[1] == rsdp_guid.d[1]) {
                    rsdp = (EFI_RSDP *)e->table;
                    break;
                }
                /* The last entry has a NULL GUID. */
                if (e->guid.a == 0 && e->guid.b == 0 && e->guid.c == 0)
                    break;
            }
        }
        if (rsdp && memcmp(rsdp->signature, "RSD PTR ", 8) == 0) {
            /* Walk the RSDT (or XSDT if revision >= 2) looking for the
             * MADT ("APIC"). The MADT's physical address is what we
             * pass to the kernel. */
            uint32_t *rsdt = (uint32_t *)(uintptr_t)rsdp->rsdt;
            uint64_t *xsdt = (uint64_t *)(uintptr_t)rsdp->xsdt;
            int n = 0;
            if (rsdp->revision >= 2 && xsdt) {
                n = (int)xsdt[1]; /* XSDT: [0]=signature, [1]=length */
                for (int i = 2; i < 2 + n; i++) {
                    uint64_t *t = (uint64_t *)(uintptr_t)xsdt[i];
                    if (t && memcmp(t, "APIC", 4) == 0) {
                        g_bi.madt_phys = xsdt[i];
                        break;
                    }
                }
            } else if (rsdt) {
                n = (int)rsdt[1]; /* RSDT: [0]=signature, [1]=length */
                for (int i = 2; i < 2 + n; i++) {
                    uint32_t *t = (uint32_t *)(uintptr_t)rsdt[i];
                    if (t && memcmp(t, "APIC", 4) == 0) {
                        g_bi.madt_phys = rsdt[i];
                        break;
                    }
                }
            }
            if (g_bi.madt_phys) {
                console_puts("[boot] MADT at phys=");
                console_hex64(g_bi.madt_phys);
                console_puts("\n");
            } else {
                console_puts("[boot] no MADT in RSDT/XSDT\n");
            }
        } else {
            console_puts("[boot] no RSDP (single CPU)\n");
        }
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
