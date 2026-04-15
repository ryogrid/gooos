/*
 * user/linker_user.ld -- Linker script for gooos user-space ELF binaries.
 *
 * Entry at 0x40100000 -- above the kernel's 1 GiB identity map
 * (0x00000000-0x3FFFFFFF uses 2 MiB huge pages in boot.S).
 * mapPage() cannot split huge pages, so user addresses MUST be >= 0x40000000.
 *
 * Heap region: a 1 MiB reservation between _heap_start and _heap_end
 * matches the sys_sbrk single-request cap in rt0.S:mmap. TinyGo's
 * baremetal runtime (baremetal.go) refuses to grow the heap further,
 * so 1 MiB is the hard per-process ceiling. Enough for the shell +
 * scheduler + chan runtime with goroutine churn in test programs.
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
    . += 0x100000; /* 1 MiB fixed heap — matches rt0.S mmap cap. */
    _heap_end = .;

    /DISCARD/ : {
        *(.eh_frame)
        *(.eh_frame_hdr)
        *(.rel.eh_frame)
        *(.rela.eh_frame)
        *(.note .note.*)
        *(.comment)
    }
}
