/*
 * src/isr.S -- 256 ISR (Interrupt Service Routine) entry stubs for x86_64.
 *
 * Each stub creates a uniform stack frame (error code + vector number),
 * saves all general-purpose registers, calls the Go-side dispatcher
 * (go_interrupt_handler), restores state, and returns via iretq.
 *
 * Vectors where the CPU pushes an error code: 8, 10-14, 17, 21.
 * All other vectors get a dummy zero pushed to keep the frame uniform.
 */

    .text

/* ---- Macros ----------------------------------------------------------- */

/* ISR stub for vectors where the CPU does NOT push an error code. */
.macro ISR_NOERR vector
    .global isr\vector
    isr\vector:
        pushq   $0              /* dummy error code */
        pushq   $\vector        /* vector number */
        jmp     isr_common
.endm

/* ISR stub for vectors where the CPU pushes an error code. */
.macro ISR_ERR vector
    .global isr\vector
    isr\vector:
        /* error code already on stack from CPU */
        pushq   $\vector        /* vector number */
        jmp     isr_common
.endm

/* ---- Exception vectors 0-31 ------------------------------------------- */

ISR_NOERR 0                    /* #DE  - Division Error */
ISR_NOERR 1                    /* #DB  - Debug */
ISR_NOERR 2                    /* NMI  - Non-Maskable Interrupt */
ISR_NOERR 3                    /* #BP  - Breakpoint */
ISR_NOERR 4                    /* #OF  - Overflow */
ISR_NOERR 5                    /* #BR  - Bound Range Exceeded */
ISR_NOERR 6                    /* #UD  - Invalid Opcode */
ISR_NOERR 7                    /* #NM  - Device Not Available */
ISR_ERR   8                    /* #DF  - Double Fault */
ISR_NOERR 9                    /* Coprocessor Segment Overrun (legacy) */
ISR_ERR   10                   /* #TS  - Invalid TSS */
ISR_ERR   11                   /* #NP  - Segment Not Present */
ISR_ERR   12                   /* #SS  - Stack-Segment Fault */
ISR_ERR   13                   /* #GP  - General Protection Fault */
ISR_ERR   14                   /* #PF  - Page Fault */
ISR_NOERR 15                   /* Reserved */
ISR_NOERR 16                   /* #MF  - x87 FPU Error */
ISR_ERR   17                   /* #AC  - Alignment Check */
ISR_NOERR 18                   /* #MC  - Machine Check */
ISR_NOERR 19                   /* #XM  - SIMD Floating-Point */
ISR_NOERR 20                   /* #VE  - Virtualization Exception */
ISR_ERR   21                   /* #CP  - Control Protection */
ISR_NOERR 22                   /* Reserved */
ISR_NOERR 23                   /* Reserved */
ISR_NOERR 24                   /* Reserved */
ISR_NOERR 25                   /* Reserved */
ISR_NOERR 26                   /* Reserved */
ISR_NOERR 27                   /* Reserved */
ISR_NOERR 28                   /* Reserved */
ISR_NOERR 29                   /* Reserved */
ISR_NOERR 30                   /* Reserved */
ISR_NOERR 31                   /* Reserved */

/* ---- IRQ and software vectors 32-255 ---------------------------------- */

.altmacro
.set VECTOR, 32
.rept 224
    ISR_NOERR %VECTOR
    .set VECTOR, VECTOR+1
.endr
.noaltmacro

/* ---- Common ISR handler ------------------------------------------------ */
/*                                                                           */
/* Stack frame at entry (high address to low):                               */
/*   [CPU pushed: SS, RSP, RFLAGS, CS, RIP]                                 */
/*   error code  (real or dummy 0)                                           */
/*   vector number                                                           */
/*                                                                           */
/* We save all 15 GPRs, call the Go dispatcher, restore, and iretq.         */

isr_common:
    /* Save all general-purpose registers */
    pushq   %rax
    pushq   %rbx
    pushq   %rcx
    pushq   %rdx
    pushq   %rsi
    pushq   %rdi
    pushq   %rbp
    pushq   %r8
    pushq   %r9
    pushq   %r10
    pushq   %r11
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    /* Vector number is at RSP + 15*8 = RSP + 120 */
    /* Error code  is at RSP + 15*8 + 8 = RSP + 128 */
    movq    120(%rsp), %rdi         /* first arg  = vector number */
    movq    128(%rsp), %rsi         /* second arg = error code    */

    /* Align stack to 16 bytes for SysV AMD64 ABI call convention. */
    /* Save original RSP in callee-saved %rbx (restored from stack later). */
    movq    %rsp, %rbx
    movq    %rbx, %rdx              /* third arg = register frame pointer */
    andq    $-16, %rsp

    call    go_interrupt_handler

    movq    %rbx, %rsp              /* restore original RSP */

    /* Restore all general-purpose registers */
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %r11
    popq    %r10
    popq    %r9
    popq    %r8
    popq    %rbp
    popq    %rdi
    popq    %rsi
    popq    %rdx
    popq    %rcx
    popq    %rbx
    popq    %rax

    addq    $16, %rsp               /* skip vector number + error code */
    iretq

/* ---- ISR address table ------------------------------------------------- */
/* A table of 256 function pointers, one per ISR stub.                       */
/* Go code reads this via isrTableAddr() to populate IDT entries.            */

.macro ISR_TABLE_ENTRY n
    .quad isr\n
.endm

    .section .rodata
    .align 8
    .global isr_table
isr_table:

.altmacro
.set TVECTOR, 0
.rept 256
    ISR_TABLE_ENTRY %TVECTOR
    .set TVECTOR, TVECTOR+1
.endr
.noaltmacro

/* ---- isrTableAddr: return pointer to isr_table for Go ------------------- */
    .text
    .global isrTableAddr
isrTableAddr:
    leaq    isr_table(%rip), %rax
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
