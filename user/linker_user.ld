/*
 * user/linker_user.ld -- Linker script for gooos user-space ELF binaries.
 *
 * Entry at 0x40100000 -- above the kernel's 1 GiB identity map
 * (0x00000000-0x3FFFFFFF uses 2 MiB huge pages in boot.S).
 * mapPage() cannot split huge pages, so user addresses MUST be >= 0x40000000.
 */
ENTRY(_start)

SECTIONS
{
    . = 0x40100000;

    .text : {
        *(.text._start)
        *(.text .text.*)
    }

    .rodata : {
        *(.rodata .rodata.*)
    }

    . = ALIGN(4096);

    .data : {
        *(.data .data.*)
    }

    .bss : {
        *(.bss .bss.*)
        *(COMMON)
    }

    . = ALIGN(4096);
    _heap_start = .;
}
