#include "efi.h"
#include "serial.h"
#include "io.h"
#include "loader.h"
#include "boot.h"
#include "cpu.h"
#include "mm.h"
#include "gdt_idt.h"

/* Patched by scripts/mkpefi.py with the Go kernel's real addresses. */
#define GO_MAGIC_ENTRY 0xB10B1A7C0FFEE001ULL
#define GO_MAGIC_END   0xB10B1A7C0FFEE002ULL

static volatile uint64_t go_entry_marker = GO_MAGIC_ENTRY;
static volatile uint64_t go_end_marker = GO_MAGIC_END;

typedef void (*go_entry_fn)(uint64_t bootinfo);

static EFI_SYSTEM_TABLE *g_st;

static CHAR16 obuf[256];

static void conout(const char *s) {
    UINTN i = 0;
    while (*s && i < 255)
        obuf[i++] = (CHAR16)*s++;
    obuf[i] = 0;
    FW2(g_st->ConOut->OutputString, (UINTN)g_st->ConOut, (UINTN)obuf);
}

/* Rough TSC frequency via PIT channel 2 (~18.2 Hz reference). */
static uint64_t measure_tsc_khz(void) {
    outb(0x61, (inb(0x61) & ~0x02) | 0x01);
    outb(0x43, 0xB0);
    outb(0x42, 1193 & 0xFF); /* 65536/1193 ~ 55ms */
    outb(0x42, 1193 >> 8);
    uint64_t t0;
    { uint32_t hi, lo; __asm__ volatile("rdtsc" : "=a"(lo), "=d"(hi)); t0 = ((uint64_t)hi << 32) | lo; }
    uint16_t last = 0, ticks = 0;
    while (ticks < 3) {
        uint8_t st = inb(0x61);
        (void)st;
        uint16_t cnt = (uint16_t)inb(0x42);
        cnt |= (uint16_t)inb(0x42) << 8;
        if (cnt > last) { /* wrapped */
            ticks++;
        }
        last = cnt;
    }
    uint64_t t1;
    { uint32_t hi, lo; __asm__ volatile("rdtsc" : "=a"(lo), "=d"(hi)); t1 = ((uint64_t)hi << 32) | lo; }
    outb(0x61, inb(0x61) & ~0x03);
    /* 3 wraps * 65536 counts @1.193MHz -> elapsed_us = ticks*65536*1e6/1193181 */
    uint64_t elapsed_us = (uint64_t)ticks * 65536ULL * 1000000ULL / 1193181ULL;
    if (!elapsed_us)
        return 0;
    uint64_t khz = (t1 - t0) / elapsed_us;
    if (khz < 100000 || khz > 10000000)
        return 0; /* implausible: let the runtime use its fallback */
    return khz;
}

static struct boot_info g_bi;

EFI_STATUS __attribute__((ms_abi)) efi_main(EFI_HANDLE image_handle,
                                            EFI_SYSTEM_TABLE *systab) {
    g_st = systab;

    serial_init();
    serial_puts("\n[boot] go-kernel UEFI stage\n");
    conout("go-kernel: UEFI boot OK\r\n");

    FW4(systab->BootServices->SetWatchdogTimer, 0, 0, 0, 0);

    void *prog = 0;
    uint64_t prog_len = 0;
    serial_puts("[boot] about to load program\n");
    load_program(image_handle, systab, &prog, &prog_len);
    serial_puts("[boot] load_program returned\n");

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

    /* minimal machine bring-up so the Go runtime lands safely */
    const struct boot_mmap bm = {mmapbuf, msize / dsize, dsize};
    gdt_install();
    idt_install();
    mm_init(&bm);
    paging_identity_init();

    uint64_t pool_base, pool_end;
    mm_pool(&pool_base, &pool_end);

    g_bi.magic = 0x424D5442; /* "BMTB" */
    g_bi.mmap_desc = (uint64_t)(uintptr_t)mmapbuf;
    g_bi.mmap_count = msize / dsize;
    g_bi.mmap_dsize = dsize;
    g_bi.prog = (uint64_t)(uintptr_t)prog;
    g_bi.prog_len = prog_len;
    g_bi.tsc_khz = measure_tsc_khz();

    /* Go heap pool: our bump region, clamped above the Go image */
    if (pool_base < go_end_marker)
        pool_base = (go_end_marker + 0xFFFFF) & ~0xFFFFFULL;
    g_bi.free_base = pool_base;
    g_bi.free_end = pool_end;

    serial_puts("[boot] tsc khz=");
    serial_hex64(g_bi.tsc_khz);
    serial_puts("\n");

    /* hand off to the Go microkernel; never returns */
    serial_puts("[boot] entering go kernel\n");
    ((go_entry_fn)go_entry_marker)((uint64_t)(uintptr_t)&g_bi);
    for (;;)
        hlt();
    return EFI_SUCCESS;
}
