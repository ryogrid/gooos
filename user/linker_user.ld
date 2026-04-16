/*
 * user/linker_user.ld -- Linker script for gooos user-space ELF binaries.
 *
 * Entry at 0x40100000 -- above the kernel's 1 GiB identity map
 * (0x00000000-0x3FFFFFFF uses 2 MiB huge pages in boot.S).
 * mapPage() cannot split huge pages, so user addresses MUST be >= 0x40000000.
 *
 * Heap: a fixed 256 KiB region nested inside .bss so ld.lld
 * extends the PT_LOAD segment's memsz over it. The kernel's
 * elfLoad (src/elf.go:194-213) then walks the PT_LOAD range and
 * allocates zero-init physical pages for every covered virtual
 * page. baremetal.go:growHeap returns false on our build tag
 * combo, so this reservation is the hard per-process ceiling.
 * 256 KiB leaves enough room for the shell + scheduler + chan
 * runtime plus dozens of 8 KiB goroutine stacks.
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

        . = ALIGN(4096);
        _heap_start = .;
        . = . + 0x40000; /* 256 KiB fixed heap (matches design). */
        _heap_end = .;
    }

    /DISCARD/ : {
        *(.eh_frame)
        *(.eh_frame_hdr)
        *(.rel.eh_frame)
        *(.rela.eh_frame)
        *(.note .note.*)
        *(.comment)
    }
}
