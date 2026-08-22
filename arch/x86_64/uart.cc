#include "io.h"
#include "plat.h"

#define COM1 0x3F8

void console_init(void) {
    outb(COM1 + 1, 0x00);    /* disable interrupts          */
    outb(COM1 + 3, 0x80);    /* DLAB on                     */
    outb(COM1 + 0, 0x01);    /* divisor 1 => 115200 baud    */
    outb(COM1 + 1, 0x00);
    outb(COM1 + 3, 0x03);    /* 8N1                         */
    outb(COM1 + 2, 0xC7);    /* FIFO on, clear              */
    outb(COM1 + 4, 0x0B);    /* RTS/DSR                     */
}

void console_putc(char c) {
    if (c == '\n')
        console_putc('\r');
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
