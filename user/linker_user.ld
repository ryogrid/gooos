/*
 * user/linker_user.ld -- Linker script for gooos user-space ELF binaries.
 *
 * Entry at 0x40100000 -- above the kernel's 1 GiB identity map
 * (0x00000000-0x3FFFFFFF uses 2 MiB huge pages in boot.S).
 * mapPage() cannot split huge pages, so user addresses MUST be >= 0x40000000.
 *
 * Globals range: [_globals_start, _globals_end) brackets .data + .bss.
 * Conservative GC scans this window for live pointers. The heap lives
 * OUTSIDE the window so findGlobals() does not treat heap contents as
 * roots (which would pin every reachable allocation).
 *
 * Heap: a 1 MiB region in its own `.heap : ALIGN(4096) { *(.heap) }`
 * output section, with the input section `.heap @nobits` defined in
 * user/rt0.S. ld.lld emits a PT_LOAD segment with memsz > filesz, so
 * the ELF stays small on disk while the kernel's elfLoad zero-fills the
 * 1 MiB at load time (mirrors kernel .heap at src/stubs.S:381).
 * baremetal.go:growHeap returns false, so this is a hard ceiling.
 *
 * A 1-page guard gap follows .heap (matches kernel convention at
 * src/linker.ld:71) to keep any future section clear of heap-end slop.
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

    _globals_start = .;

    . = ALIGN(4096);

    .data : {
        *(.data .data.*)
    }

    .bss : {
        *(.bss .bss.*)
        *(COMMON)
    }

    _globals_end = .;
    _globals_size = _globals_end - _globals_start;

    .heap : ALIGN(4096) {
        _heap_start = .;
        *(.heap)
        _heap_end = .;
    }

    /* Guard gap: matches kernel convention (src/linker.ld:71). */
    . += 4096;

    /DISCARD/ : {
        *(.eh_frame)
        *(.eh_frame_hdr)
        *(.rel.eh_frame)
        *(.rela.eh_frame)
        *(.note .note.*)
        *(.comment)
    }
}
