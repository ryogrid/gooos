/*
 * src/trampoline.S — AP (Application Processor) boot trampoline.
 *
 * This code is copied to physical address 0x8000 at runtime by the BSP.
 * Each AP starts here in 16-bit real mode after receiving a SIPI (Startup
 * Inter-Processor Interrupt), transitions through protected mode to 64-bit
 * long mode, atomically claims a stack, and calls the Go AP entry function.
 *
 * The data area at offsets 0xF00+ is patched by the BSP before sending SIPI.
 */

/*
 * Fixed base address where this trampoline is copied at runtime.
 * SIPI vector = TRAMP_BASE / 0x1000 = 0x08.
 */
.equ TRAMP_BASE, 0x8000

/* Data area offsets from TRAMP_BASE (patched by BSP before SIPI). */
.equ TRAMP_GDT,       0xF00   /* 4 GDT entries, 32 bytes */
.equ TRAMP_GDTPTR,    0xF20   /* GDT pointer: 2-byte limit + 4-byte base */
.equ TRAMP_PML4,      0xF28   /* PML4 physical address, 4 bytes */
.equ TRAMP_ENTRY64,   0xF30   /* 64-bit Go entry point, 8 bytes */
.equ TRAMP_STACKS,    0xF38   /* Pointer to AP stack array, 8 bytes */
.equ TRAMP_COUNTER,   0xF40   /* Atomic AP counter, 4 bytes */

/* ==================================================================== */
/* Trampoline code blob — placed in .rodata, copied to 0x8000 at runtime */
/* ==================================================================== */
    .section .rodata,"a",@progbits
    .align 4096

    .global trampoline_start
trampoline_start:

/* ---- 16-bit Real Mode Entry ---------------------------------------- */
/* SIPI delivers CS:IP such that CS*16 + IP = TRAMP_BASE (0x8000).     */
.code16
    cli
    cld

    /* Zero all data segment registers for flat real-mode addressing. */
    xorw    %ax, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %ss

    /* Load GDT. Operand-size override (0x66) makes lgdt read a 6-byte
       descriptor (2-byte limit + 4-byte base) instead of 5-byte. */
    .byte   0x66
    lgdt    (TRAMP_BASE + TRAMP_GDTPTR)

    /* Enable protected mode: set CR0.PE (bit 0). */
    movl    %cr0, %eax
    orl     $1, %eax
    movl    %eax, %cr0

    /* Far jump to 32-bit protected mode code (selector 0x08 = code32). */
    ljmpl   $0x08, $(TRAMP_BASE + (tramp_pm32 - trampoline_start))

/* ---- 32-bit Protected Mode ----------------------------------------- */
.code32
tramp_pm32:
    /* Reload data segments with flat data selector (0x10). */
    movw    $0x10, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %ss

    /* Enable PAE: set CR4.PAE (bit 5). */
    movl    %cr4, %eax
    orl     $(1 << 5), %eax
    movl    %eax, %cr4

    /* Load PML4 physical address into CR3 (shared with BSP). */
    movl    (TRAMP_BASE + TRAMP_PML4), %eax
    movl    %eax, %cr3

    /* Enable Long Mode: set EFER.LME (bit 8) via MSR 0xC0000080. */
    movl    $0xC0000080, %ecx
    rdmsr
    orl     $(1 << 8), %eax
    wrmsr

    /* Enable paging: set CR0.PG (bit 31). CPU enters compatibility mode. */
    movl    %cr0, %eax
    orl     $(1 << 31), %eax
    movl    %eax, %cr0

    /* Far jump to 64-bit long mode code (selector 0x18 = code64). */
    ljmpl   $0x18, $(TRAMP_BASE + (tramp_lm64 - trampoline_start))

/* ---- 64-bit Long Mode ---------------------------------------------- */
.code64
tramp_lm64:
    /* Load 64-bit data segments. */
    movw    $0x10, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %ss
    xorw    %ax, %ax
    movw    %ax, %fs
    movw    %ax, %gs

    /* Atomically claim AP index: XADD returns old counter value in eax
       (0 for first AP, 1 for second, etc.) and increments the counter.
       32-bit ops in 64-bit mode auto zero-extend to rax. */
    movl    $1, %eax
    lock xaddl  %eax, (TRAMP_BASE + TRAMP_COUNTER)

    /* Load this AP's stack from the pre-allocated stack array.
       apStacks[apIndex] = *(stackArrayPtr + apIndex * 8). */
    movq    (TRAMP_BASE + TRAMP_STACKS), %rbx
    movq    (%rbx, %rax, 8), %rsp

    /* Call Go AP entry function: apEntry(apIndex uint64).
       First arg (rdi) = AP index. Align stack to 16 bytes (ABI). */
    movq    %rax, %rdi
    movq    (TRAMP_BASE + TRAMP_ENTRY64), %rax
    andq    $-16, %rsp
    callq   *%rax

    /* Safety net: halt forever if apEntry returns. */
    cli
1:  hlt
    jmp     1b

/* ==================================================================== */
/* Data area at fixed offsets from trampoline_start.                     */
/* BSP patches runtime values (PML4, entry point, stacks) before SIPI.  */
/* ==================================================================== */

/* GDT entries for AP mode transitions. */
.org trampoline_start + TRAMP_GDT
tramp_gdt:
    .quad 0x0000000000000000    /* [0x00] null descriptor */
    .quad 0x00CF9A000000FFFF    /* [0x08] code32: flat 4G, DPL=0, exec/read, D=1 */
    .quad 0x00CF92000000FFFF    /* [0x10] data:   flat 4G, DPL=0, read/write */
    .quad 0x00209A0000000000    /* [0x18] code64: L=1, D=0, DPL=0, exec/read */

/* GDT pointer: 2-byte limit + 4-byte base address. */
.org trampoline_start + TRAMP_GDTPTR
tramp_gdt_ptr:
    .word (4 * 8 - 1)                      /* limit = 31 bytes */
    .long (TRAMP_BASE + TRAMP_GDT)         /* base = physical address of GDT */

/* PML4 physical address (patched by BSP). */
.org trampoline_start + TRAMP_PML4
tramp_pml4:
    .long 0

/* 64-bit Go entry point (patched by BSP). */
.org trampoline_start + TRAMP_ENTRY64
tramp_entry64:
    .quad 0

/* Pointer to AP stack array (patched by BSP). */
.org trampoline_start + TRAMP_STACKS
tramp_stacks:
    .quad 0

/* Atomic AP counter (incremented by each AP via lock xadd). */
.org trampoline_start + TRAMP_COUNTER
tramp_counter:
    .long 0

    .global trampoline_end
trampoline_end:

/* ==================================================================== */
/* Helper functions (in .text, called by Go code on BSP).               */
/* ==================================================================== */
    .text
    .code64

    /* trampolineStartAddr() uintptr */
    .global trampolineStartAddr
trampolineStartAddr:
    leaq    trampoline_start(%rip), %rax
    ret

    /* trampolineEndAddr() uintptr */
    .global trampolineEndAddr
trampolineEndAddr:
    leaq    trampoline_end(%rip), %rax
    ret

    /* apEntryAddr() uintptr — returns address of the //export apEntry Go function. */
    .global apEntryAddr
apEntryAddr:
    leaq    apEntry(%rip), %rax
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
