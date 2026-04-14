/* src/linker.ld — kernel layout with heap region. Load address = 1 MiB. */

ENTRY(_start)

SECTIONS
{
    . = 0x100000;

    /* Multiboot 1 header MUST be within the first 8 KiB of the kernel
     * file and 4-byte aligned. KEEP prevents --gc-sections from dropping it. */
    .multiboot : ALIGN(4)
    {
        KEEP(*(.multiboot))
    }

    .text : ALIGN(16)
    {
        *(.text .text.*)
    }

    .rodata : ALIGN(16)
    {
        *(.rodata .rodata.*)
    }

    _globals_start = .;

    .data : ALIGN(16)
    {
        *(.data .data.*)
    }

    /* .bss holds the stack AND the 4 KiB-aligned page tables (pml4, pdp,
     * pd). CR3 requires 4 KiB alignment on PML4, so ALIGN(4096) is
     * load-bearing — do not relax it.
     *
     * NOTE: The conservative GC scans this region for root pointers.
     * Page table entries (e.g., 0x83 flags) may cause false positives
     * (the GC treats them as live heap pointers, preventing some blocks
     * from being freed). This is harmless for correctness but slightly
     * reduces GC effectiveness. */
    .bss : ALIGN(4096)
    {
        *(COMMON)
        *(.bss .bss.*)
    }

    _globals_end = .;
    _globals_size = _globals_end - _globals_start;

    /* Heap region: 4 MiB, page-aligned. The .heap input section is defined
     * in stubs.S as @nobits, so ld.lld emits it in a PT_LOAD segment with
     * memsz > filesz. The ELF loader (GRUB or QEMU) zero-fills the
     * memsz - filesz gap, satisfying the GC's assumption that heap memory
     * is pre-zeroed.
     *
     * NOTE: Do NOT use (NOLOAD) here. Despite its name, (NOLOAD) has
     * different semantics in GNU ld (skip loading entirely) vs ld.lld
     * (set SHT_NOBITS). Using an explicit @nobits input section is
     * portable across both linkers. */
    .heap : ALIGN(4096)
    {
        _heap_start = .;
        *(.heap)
        _heap_end = .;
    }

    /* Page tables: placed AFTER the heap and _heap_end.
     *
     * The conservative GC's metadata memset covers metadataStart to
     * _heap_end. By placing page tables AFTER _heap_end, they are
     * outside both the GC scan range and the metadata memset range.
     *
     * allocPage() must start AFTER page tables. _alloc_start marks
     * the first address available for dynamic page allocation. */
    .pagetables : ALIGN(4096)
    {
        *(.pagetables)
    }

    . = ALIGN(4096);
    _alloc_start = .;

    /* _stack_top is defined in boot.S as a label; re-export it here
     * for the GC's stack scanning (markRoots scans sp to stackTop). */
    _stack_top = stack_top;

    /DISCARD/ :
    {
        *(.eh_frame)
        *(.eh_frame_hdr)
        *(.rel.eh_frame)
        *(.rela.eh_frame)
        *(.note .note.*)
        *(.comment)
    }
}
