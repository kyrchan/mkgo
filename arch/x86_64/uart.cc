#include "io.h"
#include "plat.h"

#define COM1 0x3F8

/* Line-start tracking: when the kernel emits a message after a newline
 * (or at boot start), the terminal cursor sits at column 0 of a line
 * that may still hold the shell's in-place echo redraw (e.g. "> cat
 * /etc/motd").  We emit a CSI-2K clear-line + CR before the first
 * printable byte of each kernel message so runtime log output does not
 * overwrite the shell prompt.  The shell's \r-based redraw sets
 * at_line_start = false, preventing spurious clears during echo. */
static bool at_line_start;

void console_init(void) {
    outb(COM1 + 1, 0x00);    /* disable interrupts          */
    outb(COM1 + 3, 0x80);    /* DLAB on                     */
    outb(COM1 + 0, 0x01);    /* divisor 1 => 115200 baud    */
    outb(COM1 + 1, 0x00);
    outb(COM1 + 3, 0x03);    /* 8N1                         */
    outb(COM1 + 2, 0xC7);    /* FIFO on, clear              */
    outb(COM1 + 4, 0x0B);    /* RTS/DSR                     */
    at_line_start = true;
}

void console_putc(char c) {
    if (c == '\n') {
        at_line_start = true;
        outb(COM1, '\r');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '\n');
        return;
    }
    if (c == '\r') {
        /* Shell echo redraw: cursor returns but we are no longer at
         * line start — suppress clear-line on the next printable. */
        at_line_start = false;
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '\r');
        return;
    }
    /* First printable after a newline (or boot start): clear the line
     * that may contain a stale shell echo. */
    if (at_line_start && ((uint8_t)c >= 0x20 || c == '\t')) {
        outb(COM1, '\r');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '\x1b');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '[');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '2');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, 'K');
        while (!(inb(COM1 + 5) & 0x20))
            ;
        outb(COM1, '\r');
    }
    at_line_start = false;
    while (!(inb(COM1 + 5) & 0x20))
        ;
    outb(COM1, (uint8_t)c);
}

void console_puts(const char *s) {
    while (*s)
        console_putc(*s++);
}

static const char hexd[] = "0123456789abcdef";

void console_hex64(uint64_t v) {
    console_puts("0x");
    for (int i = 60; i >= 0; i -= 4)
        console_putc(hexd[(v >> i) & 0xF]);
}

int console_rx_ready(void) {
    return (inb(COM1 + 5) & 0x01) ? 1 : 0;
}

int console_rx_byte(void) {
    return inb(COM1);
}
