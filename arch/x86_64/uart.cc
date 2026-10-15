#include "io.h"
#include "plat.h"
#include "arch_lock.h"
#include "log.h"
#include "sched.h"

#define COM1 0x3F8

/* Line-start tracking: when the kernel emits a message after a newline
 * (or at boot start), the terminal cursor sits at column 0 of a line
 * that may still hold the shell's in-place echo redraw (e.g. "> cat
 * /etc/motd").  We emit a CSI-2K clear-line + CR before the first
 * printable byte of each kernel message so runtime log output does not
 * overwrite the shell prompt.  The shell's \r-based redraw sets
 * at_line_start = false, preventing spurious clears during echo. */
static bool at_line_start;

/* SMP console serialization (Phase 8.2 follow-up).  arch_irq_save only
 * stops *this* core from taking an IRQ; it does nothing for the other
 * three cores, so with -smp 4 the AP bring-up lines interleaved on the
 * wire ("[acpup2]  bcoooted") and the p14 gate grep for a whole
 * "[ap] cpu1 booted" line failed on a boot that was actually correct.
 * The COM1 THR is a single byte register with no internal framing, so a
 * byte-wise interleave is visible to the reader -- every kernel message
 * must be serialized across cores.
 *
 * Ordering convention (AGENTS Phase 8.1): arch_irq_save FIRST, then
 * arch_spinlock_acquire.  Acquire-then-IRQ would deadlock against an
 * ISR on this same core that wants the console.
 *
 * cli does NOT mask exceptions (a #PF/#UD from the poll loop still
 * fires), and isr_dump -> console_puts would then re-enter this
 * function on a core that already holds the ticket lock.  A plain
 * spinlock would self-deadlock there, so re-entrancy is detected by
 * owner identity: nested frames on the same core see their own cpu id
 * and skip the lock, because that core already holds it.
 *
 * The nesting depth is a single variable owned by the lock holder, NOT
 * a per-cpu array.  A per-cpu counter keyed on the GS-derived cpu id
 * aliases before sched_init sets GS (every unscheduled core reads
 * cpu_id 0), and an aliased fetch_sub makes the depth miss the release
 * point -- the lock is then never handed on and the boot wedges
 * mid-bring-up.  Owner + single-writer depth cannot alias that way.
 *
 * CONSOLE_NONE must be a value no cpu id can take, and console_owner
 * must START at it: left zero-initialized it reads as "cpu 0 owns the
 * console", so the BSP's first console call took the nested path and
 * released a ticket it never took -- permanently desynchronizing the
 * spinlock's serving counter.  The nested branch is additionally gated
 * on console_depth > 0 so owner alone can never authorize a release. */
#define CONSOLE_NONE 0xFFFFFFFFu
static arch_spinlock_t console_lock;
static volatile uint32_t console_owner = CONSOLE_NONE; /* cpu id in a section */
static uint32_t console_depth;           /* written only by the holder */

static inline uint32_t console_cpu_id(void) {
    return sched_current_cpu()->cpu_id;
}

/* Enter/leave a serialized console section.  irq_save first, then the
 * lock (AGENTS Phase 8.1 ordering); the token restores both.
 *
 * Re-entrancy test is owner == my cpu id, and nothing else:
 *   - console_owner starts at CONSOLE_NONE, so the BSP (cpu 0) does
 *     not false-match its own id on the first call and release a
 *     ticket it never took.
 *   - cpu ids are unique: an AP sets its GS base in sched_ap_boot
 *     BEFORE its first console write, so only the BSP is ever
 *     GS-less, and it is always id 0.
 *   - console_depth must NOT be part of the test: it is global, so a
 *     core WAITING for the lock would read the holder's non-zero
 *     depth, conclude it was nested, and print with no lock at all
 *     (byte-level interleaving, one core's CSI-2K landing in the
 *     middle of another core's line). Depth is a single-writer
 *     counter used only to decide when to hand the lock on. */
static inline arch_irq_state_t console_enter(uint32_t *id_out) {
    arch_irq_state_t irq = arch_irq_save();
    uint32_t id = console_cpu_id();
    if (id_out)
        *id_out = id;
    if (__atomic_load_n(&console_owner, __ATOMIC_ACQUIRE) != id) {
        arch_spinlock_acquire(&console_lock);
        console_depth = 1;
        __atomic_store_n(&console_owner, id, __ATOMIC_RELEASE);
    } else {
        console_depth++;  /* same core re-entered: it already owns the lock */
    }
    return irq;
}

static inline void console_leave(uint32_t id, arch_irq_state_t irq) {
    if (--console_depth == 0) {
        __atomic_store_n(&console_owner, CONSOLE_NONE, __ATOMIC_RELEASE);
        arch_spinlock_release(&console_lock);
    }
    (void)id;
    arch_irq_restore(irq);
}

void console_init(void) {
    arch_spinlock_init(&console_lock);
    outb(COM1 + 1, 0x00);    /* disable interrupts          */
    outb(COM1 + 3, 0x80);    /* DLAB on                     */
    outb(COM1 + 0, 0x01);    /* divisor 1 => 115200 baud    */
    outb(COM1 + 1, 0x00);
    outb(COM1 + 3, 0x03);    /* 8N1                         */
    outb(COM1 + 2, 0xC7);    /* FIFO on, clear              */
    outb(COM1 + 4, 0x0B);    /* RTS/DSR                     */
    at_line_start = true;
}

/* Emit one byte.  Caller holds the console section (console_enter). */
static void uart_emit(char c) {
    /* v1 syslog (Phase 15): feed the core log ring. Skip '\r' — it is
     * terminal control (shell redraws, \n translation), not content.
     * log_push takes only the ticket spinlock, never IRQs, so this is
     * safe from the isr_dump path (IRQs already off there) and SMP. */
    if (c != '\r')
        log_push(&c, 1);
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

void console_putc(char c) {
    /* Substrate-hardening (commit E): disable IRQs across the UART
     * write. The PIT IRQ0 stub (92313c5) does NOT touch the console,
     * but every other ISR (vectors 0-31) eventually calls
     * isr_dump -> console_puts -> console_putc. If a fault fires
     * from inside the UART poll loop with IF=1, we'd re-enter
     * console_putc recursively and either (a) recurse forever on
     * a slow UART or (b) corrupt at_line_start state.
     *
     * arch_irq_save is the correct primitive: it nests correctly
     * (cli is a no-op if we're already inside the IRQ path which
     * has IF=0), and the saved flag is restored on exit so we
     * don't keep IRQs off after the call.  On SMP it is not enough
     * on its own -- see console_lock above for the cross-core half. */
    uint32_t id;
    arch_irq_state_t irq = console_enter(&id);
    uart_emit(c);
    console_leave(id, irq);
}

/* One hold for the WHOLE string: a per-byte lock would still let
 * another core interleave between two characters of one line. */
void console_puts(const char *s) {
    uint32_t id;
    arch_irq_state_t irq = console_enter(&id);
    while (*s)
        uart_emit(*s++);
    console_leave(id, irq);
}

static const char hexd[] = "0123456789abcdef";

void console_hex64(uint64_t v) {
    uint32_t id;
    arch_irq_state_t irq = console_enter(&id);
    uart_emit('0');
    uart_emit('x');
    for (int i = 60; i >= 0; i -= 4)
        uart_emit(hexd[(v >> i) & 0xF]);
    console_leave(id, irq);
}


int console_rx_ready(void) {
    return (inb(COM1 + 5) & 0x01) ? 1 : 0;
}

int console_rx_byte(void) {
    return inb(COM1);
}
