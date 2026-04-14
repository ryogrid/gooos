/*
 * src/boot.S — Multiboot 1 header, 32->64-bit bootstrap, call into TinyGo.
 *
 * Assembled by GNU `as` into an ELF64 relocatable (mixed .code32 / .code64
 * via explicit directives), then linked together with the TinyGo-emitted
 * Go object file via src/linker.ld. AT&T syntax throughout.
 */

    .set MB_MAGIC,    0x1BADB002
    .set MB_FLAGS,    0x00000000
    .set MB_CHECKSUM, -(MB_MAGIC + MB_FLAGS)

    /* ---- Multiboot 1 header ----------------------------------------- */
    /* Must be 4-byte aligned and reside within the first 8 KiB of the   */
    /* kernel file. The linker script places .multiboot first.           */
    .section .multiboot, "a", @progbits
    .align 4
    .long MB_MAGIC
    .long MB_FLAGS
    .long MB_CHECKSUM

    /* ---- BSS: stack + page tables ----------------------------------- */
    /* Use the bare .bss directive (not .section .bss, ...). Assemblers  */
    /* are picky about re-declaring well-known ELF sections with         */
    /* explicit flags; the bareword form always works.                   */
    .bss
    .align 16
stack_bottom:
    .skip 16384                      /* 16 KiB kernel stack */
    .global stack_top
stack_top:

    /* Page tables are placed in a dedicated section (.pagetables) that the
     * linker script positions AFTER _globals_end. This keeps them outside
     * the conservative GC's root scan range, preventing the GC mark phase
     * from corrupting page table entries. */
    .section .pagetables, "aw", @nobits
    .align 4096
pml4:
    .skip 4096                       /* 1 x PML4 table */
pdp:
    .skip 4096                       /* 1 x PDP  table */
pd:
    .skip 4096                       /* 1 x PD   table (512 x 2 MiB = 1 GiB) */

    /* ---- 64-bit GDT (placed in .rodata) ----------------------------- */
    .section .rodata,"a",@progbits
    .align 8
gdt64:
    .quad 0                                                          /* null descriptor    */
    .set GDT64_CODE, . - gdt64
    .quad (1 << 43) | (1 << 44) | (1 << 47) | (1 << 53)              /* 64-bit code seg    */
    .set GDT64_DATA, . - gdt64
    .quad (1 << 41) | (1 << 44) | (1 << 47)                          /* 64-bit data seg    */
gdt64_end:
    .align 8
gdt64_pointer:
    .word gdt64_end - gdt64 - 1                                      /* limit              */
    .quad gdt64                                                      /* base (64-bit addr) */

    /* ---- 32-bit entry point ----------------------------------------- */
    .text
    .code32
    .global _start
    .extern main
_start:
    /* Set up the stack (grows down). */
    movl    $stack_top, %esp
    cld

    /* PML4[0] = &pdp | PRESENT | WRITE */
    movl    $pdp, %eax
    orl     $0x03, %eax
    movl    %eax, pml4

    /* PDP[0] = &pd | PRESENT | WRITE */
    movl    $pd, %eax
    orl     $0x03, %eax
    movl    %eax, pdp

    /* Fill PD[0..511] with 2 MiB huge pages identity-mapping 0..1 GiB.
     * Each entry: (i * 0x200000) | PRESENT | WRITE | HUGE (= 0x83).
     * Page-table entries are 8 bytes. We explicitly write both halves
     * rather than relying on the loader to zero .bss, so the code works
     * even if pd is ever moved to .data or a zero-fill is skipped.     */
    xorl    %ecx, %ecx
1:
    movl    %ecx, %eax
    shll    $21, %eax                /* eax = i * 2 MiB (2^21 = 0x200000) */
    orl     $0x83, %eax              /* PRESENT | WRITE | PAGE_SIZE       */
    movl    %eax, pd(, %ecx, 8)      /* low 32 bits of entry i            */
    movl    $0,   pd+4(, %ecx, 8)    /* high 32 bits -- explicitly zero   */
    incl    %ecx
    cmpl    $512, %ecx
    jne     1b

    /* CR3 = physical address of PML4 */
    movl    $pml4, %eax
    movl    %eax, %cr3

    /* CR4.PAE (bit 5) = 1 */
    movl    %cr4, %eax
    orl     $(1 << 5), %eax
    movl    %eax, %cr4

    /* EFER.LME (bit 8) = 1 via MSR 0xC0000080 */
    movl    $0xC0000080, %ecx
    rdmsr
    orl     $(1 << 8), %eax
    wrmsr

    /* CR0.PG (bit 31) = 1 -- paging on, CPU enters compatibility mode. */
    movl    %cr0, %eax
    orl     $(1 << 31), %eax
    movl    %eax, %cr0

    /* Load the 64-bit GDT and far-jump to a 64-bit code segment. */
    lgdt    gdt64_pointer
    ljmp    $GDT64_CODE, $long_mode_start

    /* ---- 64-bit code ------------------------------------------------ */
    .code64
long_mode_start:
    /* Reload segment registers with the 64-bit data selector. */
    movw    $GDT64_DATA, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %fs
    movw    %ax, %gs
    movw    %ax, %ss

    /* Call TinyGo's runtime entry point (main from runtime_unix.go).
     * Signature: main(argc int32, argv *unsafe.Pointer) int
     * Pass argc=0, argv=nil since this is a bare-metal kernel. */
    xorl    %edi, %edi              /* argc = 0 */
    xorl    %esi, %esi              /* argv = nil (zero-extended to 64-bit) */
    call    main

    /* If main ever returns, disable interrupts and halt forever. */
hang:
    cli
    hlt
    jmp     hang

    /* Mark the stack as non-executable to silence linker warnings. */
    .section .note.GNU-stack,"",@progbits
