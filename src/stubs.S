/*
 * src/stubs.S -- libc/runtime symbol stubs for bare-metal TinyGo.
 *
 * TinyGo's linux runtime glue (runtime_unix.go, compiled because goos=linux)
 * references a handful of libc/runtime symbols. Some are dead code, some
 * become live when gc != "none". We provide minimal implementations here.
 *
 *   abort                         -- runtime.runtimePanicAt fallback (dead code)
 *   write                         -- runtime.putchar (dead code)
 *   mmap                          -- runtime_unix.preinit: heap allocation (LIVE)
 *   raise                         -- tinygo_handle_fatal_signal (dead code)
 *   tinygo_register_fatal_signals -- tinygo main startup (called, noop)
 *   memcpy                        -- runtime.stringConcat, runtime.sliceAppend
 *   memset                        -- runtime.alloc (zero new blocks), GC mark phase
 *   tinygo_scanCurrentStack       -- GC stack/register scanner trampoline
 *   __ehdr_start                  -- synthetic ELF header for findGlobals()
 */

    .text

    /* ---- abort: halt the CPU (should never be reached) -------------- */
    .global abort
abort:
    cli
1:  hlt
    jmp     1b

    /* ---- write: return 0 (dead code, putchar path) ------------------ */
    .global write
write:
    xorq    %rax, %rax
    ret

    /* ---- mmap: return _heap_start if size fits, else -1 ------------- */
    /*                                                                   */
    /* Called by runtime_unix.go:preinit() to reserve virtual heap space. */
    /* preinit() starts with 1 GB and halves on failure until success.   */
    /*                                                                   */
    /* SysV AMD64 ABI parameters:                                        */
    /*   rdi = addr  (ignored, always nil)                               */
    /*   rsi = length (requested size)                                   */
    /*   edx = prot  (ignored)                                           */
    /*   ecx = flags (ignored)                                           */
    /*   r8d = fd    (ignored)                                           */
    /*   r9  = offset (ignored)                                          */
    /*                                                                   */
    /* Returns: pointer to heap region, or -1 (MAP_FAILED).              */
    .global mmap
mmap:
    /* Calculate available heap size: _heap_end - _heap_start */
    movq    $_heap_end, %rax
    movq    $_heap_start, %rcx
    subq    %rcx, %rax              /* rax = available heap bytes */
    cmpq    %rax, %rsi              /* requested (rsi) > available (rax)? */
    ja      .mmap_fail
    movq    $_heap_start, %rax      /* success: return _heap_start */
    ret
.mmap_fail:
    movq    $-1, %rax               /* failure: return MAP_FAILED */
    ret

    /* ---- raise: return 0 (dead code, signal handler path) ----------- */
    .global raise
raise:
    xorq    %rax, %rax
    ret

    /* ---- tinygo_register_fatal_signals: noop (called but safe) ------ */
    .global tinygo_register_fatal_signals
tinygo_register_fatal_signals:
    ret

    /* ---- memcpy: simple forward byte copy ---------------------------- */
    .global memcpy
memcpy:
    /* memcpy(dest=rdi, src=rsi, n=rdx) -> rax = dest */
    movq    %rdi, %rax
    movq    %rdx, %rcx
    rep movsb
    ret

    /* ---- memmove: overlap-safe copy (same ABI as memcpy) ------------ */
    .global memmove
memmove:
    /* memmove(dest=rdi, src=rsi, n=rdx) -> rax = dest                  */
    movq    %rdi, %rax              /* return value */
    cmpq    %rsi, %rdi
    je      .Lmemmove_done          /* dest == src: nothing to do */
    jb      .Lmemmove_fwd           /* dest < src: forward copy */
    /* dest > src: forward copy would overwrite; go backward */
    movq    %rdx, %rcx
    leaq    -1(%rdi,%rdx), %rdi
    leaq    -1(%rsi,%rdx), %rsi
    std
    rep movsb
    cld
    ret
.Lmemmove_fwd:
    movq    %rdx, %rcx
    rep movsb
.Lmemmove_done:
    ret

    /* ---- memset: fill memory with a byte value ---------------------- */
    /* memset(dest=rdi, c=esi, n=rdx) -> rax = dest                     */
    .global memset
memset:
    movq    %rdi, %r8               /* save dest for return value */
    movb    %sil, %al               /* byte value to fill */
    movq    %rdx, %rcx              /* count */
    rep stosb                       /* fill [rdi..rdi+rcx) with al */
    movq    %r8, %rax               /* return original dest */
    ret

    /* ---- outb: write byte to I/O port -------------------------------- */
    /* outb(port uint16, val uint8)                                      */
    /* SysV ABI: di = port, sil = val                                    */
    .global outb
outb:
    movw    %di, %dx
    movb    %sil, %al
    outb    %al, %dx
    ret

    /* ---- inb: read byte from I/O port -------------------------------- */
    /* inb(port uint16) uint8                                            */
    /* SysV ABI: di = port; returns al (zero-extended to eax)            */
    .global inb
inb:
    movw    %di, %dx
    inb     %dx, %al
    movzbl  %al, %eax
    ret

    /* ---- readFlags: return RFLAGS register ------------------------------ */
    /* readFlags() uintptr                                                  */
    .global readFlags
readFlags:
    pushfq
    popq    %rax
    ret

    /* ---- restoreFlags: restore RFLAGS register ------------------------- */
    /* restoreFlags(flags uintptr)                                          */
    /* SysV ABI: rdi = flags value to restore.                              */
    .global restoreFlags
restoreFlags:
    pushq   %rdi
    popfq
    ret

    /* ---- cli: disable maskable interrupts ------------------------------- */
    /* No args, no return value. Simply executes the x86 CLI instruction.  */
    .global cli
cli:
    cli
    ret

    /* ---- sti: enable maskable interrupts ------------------------------- */
    /* No args, no return value. Simply executes the x86 STI instruction.  */
    .global sti
sti:
    sti
    ret

    /* ---- hlt: halt the CPU until next interrupt ------------------------- */
    /* No args, no return value. Simply executes the x86 HLT instruction.  */
    .global hlt
hlt:
    hlt
    ret

    /* ---- heapEndAddr: return the address of _heap_end linker symbol ---- */
    /* heapEndAddr() uintptr                                                */
    /* Used by the TinyGo mmap stub to determine available heap size.       */
    .global heapEndAddr
heapEndAddr:
    movq    $_heap_end, %rax
    ret

    /* ---- allocStartAddr: return the address of _alloc_start symbol ------ */
    /* allocStartAddr() uintptr                                              */
    /* First address after .pagetables section — page allocator starts here. */
    .global allocStartAddr
allocStartAddr:
    movq    $_alloc_start, %rax
    ret

    /* ---- readCR2: return the page-fault linear address ----------------- */
    /* readCR2() uintptr                                                  */
    /* Called by the page fault handler to retrieve the faulting address.  */
    .global readCR2
readCR2:
    movq    %cr2, %rax
    ret

    /* ---- readCR3: return the PML4 physical base address ---------------- */
    /* readCR3() uintptr                                                   */
    /* Returns the current value of CR3 (page table root).                 */
    .global readCR3
readCR3:
    movq    %cr3, %rax
    ret

    /* ---- writeCR3: load a new PML4 physical base into CR3 -------------- */
    /* writeCR3(pml4 uintptr)                                              */
    /* SysV ABI: rdi = phys addr of the PML4 to install.                   */
    /* Writing CR3 implicitly flushes the entire (non-global) TLB.         */
    .global writeCR3
writeCR3:
    movq    %rdi, %cr3
    ret

    /* ---- invlpg: invalidate TLB entry for a virtual address ------------ */
    /* invlpg(addr uintptr)                                                */
    /* SysV ABI: rdi = virtual address whose TLB entry to flush.           */
    .global invlpg
invlpg:
    invlpg  (%rdi)
    ret

    /* ---- defaultHandler: catch-all interrupt stub (halts CPU) -------- */
    /* Used by idtInit() to fill all 256 IDT entries before real ISR     */
    /* stubs are installed. If any interrupt fires unexpectedly, halt.   */
    .global defaultHandler
defaultHandler:
    cli
1:  hlt
    jmp     1b

    /* ---- defaultHandlerAddr: return address of defaultHandler -------- */
    /* Returns the linear address of the default handler stub so Go code  */
    /* can populate IDT gate descriptors with it.                         */
    /* SysV ABI: no args; returns address in rax.                         */
    .global defaultHandlerAddr
defaultHandlerAddr:
    leaq    defaultHandler(%rip), %rax
    ret

    /* ---- lidt: load the IDT register --------------------------------- */
    /* lidt(desc uintptr)                                                 */
    /* SysV ABI: rdi = pointer to 10-byte packed IDT descriptor.          */
    .global lidt
lidt:
    lidt    (%rdi)
    ret

    /* ---- tinygo_scanCurrentStack: GC register/stack scanner --------- */
    /* Pushes all callee-saved registers onto the stack so the GC can    */
    /* scan them for heap pointers, then calls tinygo_scanstack(rsp).    */
    /* Canonical implementation from TinyGo's src/runtime/asm_amd64.S.  */
    .global tinygo_scanCurrentStack
tinygo_scanCurrentStack:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $8, %rsp                /* maintain 16-byte stack alignment */
    movq    %rsp, %rdi              /* pass RSP as first arg */
    callq   tinygo_scanstack        /* Go function in gc_stack_raw.go */
    addq    $56, %rsp               /* clean up: 6 regs (48) + align (8) */
    retq

    /* Dummy tinygo_scanstack for gc=leaking (never called, satisfies linker). */
    .weak   tinygo_scanstack
tinygo_scanstack:
    retq

    /* ---- lgdtReload: load new GDT and reload segment registers --------- */
    /* lgdtReload(desc uintptr)                                            */
    /* SysV ABI: rdi = pointer to 10-byte packed GDT descriptor.           */
    /*                                                                      */
    /* After lgdt, reload DS/ES/FS/GS/SS with the kernel data selector,    */
    /* and reload CS via a far return (lretq) to the kernel code selector.  */
    .global lgdtReload
lgdtReload:
    lgdt    (%rdi)

    /* Reload data segments with kernel data selector (0x10). */
    movw    $0x10, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %fs
    movw    %ax, %gs
    movw    %ax, %ss

    /* Reload CS via far return: push selector + return address, then lretq. */
    pushq   $0x08
    leaq    1f(%rip), %rax
    pushq   %rax
    lretq
1:
    ret

    /* ---- ltr: load the Task Register ------------------------------------ */
    /* ltr(selector uint16)                                                  */
    /* SysV ABI: di = TSS selector.                                          */
    .global ltr
ltr:
    movw    %di, %ax
    ltr     %ax
    ret

    /* ---- jumpToRing3: transition to Ring 3 via iretq -------------------- */
    /* jumpToRing3(userRIP uintptr, userRSP uintptr)                         */
    /* SysV ABI: rdi = user RIP, rsi = user RSP.                             */
    /*                                                                        */
    /* Sets data segment registers to user data selector, builds an iretq     */
    /* stack frame (SS, RSP, RFLAGS, CS, RIP), and executes iretq to switch   */
    /* to Ring 3. Does not return.                                            */
    .global jumpToRing3
jumpToRing3:
    /* Set data segment registers to user data selector (0x20 | RPL=3). */
    movw    $0x23, %ax
    movw    %ax, %ds
    movw    %ax, %es

    /* Build iretq frame (pushed in reverse order). */
    pushq   $0x23               /* SS  = user data selector */
    pushq   %rsi                /* RSP = user stack pointer */
    pushfq                      /* RFLAGS (IF=1 from current state) */
    pushq   $0x1B               /* CS  = user code selector (0x18 | RPL=3) */
    pushq   %rdi                /* RIP = user entry point */
    iretq

    /* ================================================================= */
    /* Synthetic ELF header for findGlobals()                            */
    /*                                                                   */
    /* os_linux.go:findGlobals() parses the ELF header at __ehdr_start   */
    /* to locate writable PT_LOAD segments for GC root scanning. GRUB    */
    /* does not load the real ELF header into memory, so we provide a    */
    /* fake header that describes exactly the globals region              */
    /* (_globals_start to _globals_end).                                 */
    /*                                                                   */
    /* Overrides ld.lld's auto-defined __ehdr_start (user-defined global */
    /* symbols take precedence over linker-generated hidden symbols).    */
    /* ================================================================= */

    .section .rodata,"a",@progbits
    .align 8
    .global __ehdr_start
__ehdr_start:
    /* --- Elf64_Ehdr (64 bytes) --- */
    .byte   0x7f, 0x45, 0x4c, 0x46  /* e_ident[0..3]: ELF magic */
    .byte   2                        /* e_ident[4]: ELFCLASS64 */
    .byte   1                        /* e_ident[5]: ELFDATA2LSB (little-endian) */
    .byte   1                        /* e_ident[6]: EV_CURRENT */
    .byte   0                        /* e_ident[7]: ELFOSABI_NONE */
    .byte   0                        /* e_ident[8]: ABI version */
    .zero   7                        /* e_ident[9..15]: padding */
    .short  2                        /* e_type: ET_EXEC */
    .short  0x3E                     /* e_machine: EM_X86_64 */
    .long   1                        /* e_version: EV_CURRENT */
    .quad   0                        /* e_entry: (unused by findGlobals) */
    .quad   64                       /* e_phoff: program headers at offset 64 (right after this header) */
    .quad   0                        /* e_shoff: (unused) */
    .long   0                        /* e_flags */
    .short  64                       /* e_ehsize: sizeof(Elf64_Ehdr) */
    .short  56                       /* e_phentsize: sizeof(Elf64_Phdr) */
    .short  1                        /* e_phnum: 1 program header */
    .short  0                        /* e_shentsize: (unused) */
    .short  0                        /* e_shnum: (unused) */
    .short  0                        /* e_shstrndx: (unused) */

    /* --- Elf64_Phdr (56 bytes) — describes the writable globals region --- */
    .long   1                        /* p_type: PT_LOAD */
    .long   6                        /* p_flags: PF_R | PF_W */
    .quad   0                        /* p_offset: (unused by findGlobals) */
    .quad   _globals_start           /* p_vaddr: start of .data+.bss */
    .quad   0                        /* p_paddr: (unused) */
    .quad   0                        /* p_filesz: (unused by findGlobals) */
    .quad   _globals_size            /* p_memsz: size of globals region */
    .quad   0                        /* p_align: (unused) */

    /* ---- wrmsr: write a 64-bit value to an MSR ----------------------- */
    /* wrmsr(msr uint32, val uint64)                                    */
    /* SysV ABI: edi = MSR number, rsi = value.                         */
    /* x86 wrmsr: ECX = MSR, EDX:EAX = value.                          */
    .text
    .global wrmsr
wrmsr:
    movl    %edi, %ecx          /* MSR number */
    movl    %esi, %eax          /* low 32 bits of val */
    shrq    $32, %rsi
    movl    %esi, %edx          /* high 32 bits of val */
    wrmsr
    ret

    /* ---- rdmsr: read a 64-bit value from an MSR ---------------------- */
    /* rdmsr(msr uint32) uint64                                          */
    /* SysV ABI: edi = MSR number. Returns value in rax.                 */
    .global rdmsr
rdmsr:
    movl    %edi, %ecx
    rdmsr
    shlq    $32, %rdx
    orq     %rdx, %rax
    ret

    /* ---- cpuID: return current CPU index from per-CPU storage -------- */
    /* cpuID() uint32                                                    */
    /* Reads %gs:0 (pcpuOffCPUIndex). Must be called after percpuInit.  */
    .global cpuID
cpuID:
    movl    %gs:0, %eax
    ret

    /* ---- spinlockAcquire: xchg-based spinlock acquire ----------------- */
    /* spinlockAcquire(lock *uint32)                                     */
    /* SysV ABI: rdi = pointer to lock word. Spins until acquired.       */
    .global spinlockAcquire
spinlockAcquire:
    movl    $1, %eax
.Lspin_retry:
    xchgl   %eax, (%rdi)
    testl   %eax, %eax
    jnz     .Lspin_wait
    ret
.Lspin_wait:
    pause                       /* hint: spin-wait loop */
    movl    (%rdi), %eax        /* test without xchg (TTAS pattern) */
    testl   %eax, %eax
    jnz     .Lspin_wait
    jmp     .Lspin_retry

    /* ---- spinlockRelease: release spinlock with memory fence ---------- */
    /* spinlockRelease(lock *uint32)                                      */
    /* SysV ABI: rdi = pointer to lock word.                              */
    .global spinlockRelease
spinlockRelease:
    movl    $0, (%rdi)
    mfence
    ret

    /* ---- readInterruptDepth: per-CPU interrupt depth counter --------- */
    /* readInterruptDepth() uint32                                       */
    /* Reads %gs:4 (pcpuOffInterruptDepth).                             */
    .global readInterruptDepth
readInterruptDepth:
    movl    %gs:4, %eax
    ret

    /* ---- Heap region (4 MiB, NOBITS) -------------------------------- */
    /* Defined here as an @nobits section so the linker places it in a    */
    /* PT_LOAD segment with memsz > filesz, causing the ELF loader to    */
    /* zero-fill it. This is portable across GNU ld and ld.lld.          */
    .section .heap,"aw",@nobits
    .skip 0x400000                      /* 4 MiB heap */

    /* Mark the stack as non-executable to silence ld.lld's warning. */
    .section .note.GNU-stack,"",@progbits
