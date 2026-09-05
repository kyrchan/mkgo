OUTPUT_FORMAT("elf64-x86-64")
OUTPUT_ARCH(i386:x86-64)
ENTRY(_start)

/* Link at 1 MiB: matches the PE ImageBase used by scripts/mkpefi.py.
 * First section sits one page above the base so PE headers fit at RVA 0. */
SECTIONS
{
    . = 0x101000;

    .text : {
        *(.text .text.*)
        *(.trampoline)
        *(.plt .plt.* .iplt)
    }

    . = ALIGN(4096);

    .rodata : {
        *(.rodata .rodata.*)
    }

    . = ALIGN(4096);

    .data : {
        *(.data .data.*)
        *(.sdata .sdata.*)
        *(.got .got.* .igot.plt)
        *(.hash .gnu.hash)
    }

    .bss : {
        *(.bss .bss.*)
        *(COMMON)
    }

    .dynamic : { *(.dynamic) }
    .rela     : { *(.rela .rela.*) }
    .dynsym   : { *(.dynsym) }
    .dynstr   : { *(.dynstr) }

    /DISCARD/ : {
        *(.eh_frame .eh_frame.*)
        *(.comment)
        *(.note .note.*)
    }
}
