#include "cpu.h"
#include "serial.h"

void cpu_dump_features(void) {
    struct cpuid max = cpuid(0, 0);
    serial_puts("[cpu] vendor '");
    for (int i = 0; i < 4; i++)
        serial_putc((char)((max.b >> (8 * i)) & 0xFF));
    for (int i = 0; i < 4; i++)
        serial_putc((char)((max.d >> (8 * i)) & 0xFF));
    for (int i = 0; i < 4; i++)
        serial_putc((char)((max.c >> (8 * i)) & 0xFF));
    serial_puts("'\n");

    struct cpuid f1 = cpuid(1, 0);
    serial_puts("[cpu] xsave=");
    serial_putc('0' + ((f1.c >> 26) & 1));
    serial_puts(" osxsave=");
    serial_putc('0' + ((f1.c >> 27) & 1));
    serial_puts(" avx=");
    serial_putc('0' + ((f1.c >> 28) & 1));
    if (max.a >= 7) {
        struct cpuid f7 = cpuid(7, 0);
        serial_puts(" avx2=");
        serial_putc('0' + ((f7.b >> 5) & 1));
    }
    serial_puts("\n");
}

int cpu_enable_avx2(void) {
    struct cpuid f1 = cpuid(1, 0);
    int xsave = (f1.c >> 26) & 1;
    int avx = (f1.c >> 28) & 1;
    /* NB: OSXSAVE (bit 27) reads 0 until CR4.OSXSAVE is set by us --
     * it must not gate the decision here. */
    if (!xsave || !avx)
        return -1;
    if (!(cpuid(7, 0).b & (1 << 5)))
        return -2;

    /* CR0: MP=1, EM=0 */
    uint64_t cr0 = rd_cr0();
    cr0 |= (1 << 1);
    cr0 &= ~(1ULL << 2);
    wr_cr0(cr0);

    /* CR4: OSFXSR | OSXSAVE */
    uint64_t cr4 = rd_cr4();
    cr4 |= (1 << 9) | (1 << 18);
    wr_cr4(cr4);

    /* XCR0 = x87 | SSE | YMM */
    uint64_t xcr0 = rd_xcr0(0);
    xcr0 |= (1 << 0) | (1 << 1) | (1 << 2);
    wr_xcr0(0, xcr0);

    /* verify it stuck: XCR0 bits and the now-live OSXSAVE cpuid flag */
    if ((rd_xcr0(0) & 7) != 7 || !((cpuid(1, 0).c >> 27) & 1))
        return -3;
    return 0;
}
