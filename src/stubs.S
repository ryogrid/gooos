/*
 * src/stubs.S -- libc/runtime symbol stubs for bare-metal TinyGo.
 *
 * TinyGo's linux runtime glue (runtime_unix.go, compiled because goos=linux)
 * references a handful of libc/runtime symbols. Some are dead code, some
 * become live when gc != "none". We provide minimal implementations here.
 *
 *   abort                         -- runtime.runtimePanicAt fallback (dead code)
 *   write                         -- runtime.putchar (dead code)
 *   mmap                          -- runtime_unix.preinit: heap allocation (LIVE with gc != none)
 *   raise                         -- tinygo_handle_fatal_signal (dead code)
 *   tinygo_register_fatal_signals -- tinygo main startup (called, but noop is safe)
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

    /* ---- memmove: overlap-safe byte copy ----------------------------- */
    .global memmove
memmove:
    /* memmove(dest=rdi, src=rsi, n=rdx) -> rax = dest */
    movq    %rdi, %rax
    cmpq    %rsi, %rdi
    je      .memmove_done
    jb      .memmove_fwd
    /* dest > src: copy backward to handle overlap */
    leaq    -1(%rdi, %rdx), %rdi
    leaq    -1(%rsi, %rdx), %rsi
    movq    %rdx, %rcx
    std
    rep movsb
    cld
    ret
.memmove_fwd:
    movq    %rdx, %rcx
    rep movsb
.memmove_done:
    ret

    /* ---- Heap region (4 MiB, NOBITS) -------------------------------- */
    /* Defined here as an @nobits section so the linker places it in a    */
    /* PT_LOAD segment with memsz > filesz, causing the ELF loader to    */
    /* zero-fill it. This is portable across GNU ld and ld.lld.          */
    .section .heap,"aw",@nobits
    .skip 0x400000                      /* 4 MiB heap */

    /* Mark the stack as non-executable to silence ld.lld's warning. */
    .section .note.GNU-stack,"",@progbits
