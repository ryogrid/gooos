/*
 * src/stubs.S -- libc/runtime symbol stubs.
 *
 * TinyGo's linux runtime glue (runtime_unix.go, compiled because goos=linux)
 * contains dead-code references to a handful of libc/runtime symbols that
 * would normally come from glibc or musl:
 *
 *   abort                              -- runtime.runtimePanicAt fallback
 *   write                              -- runtime.putchar (stdio)
 *   mmap                               -- runtime.preinit (hosted heap)
 *   raise                              -- tinygo_handle_fatal_signal
 *   tinygo_register_fatal_signals      -- tinygo main startup
 *
 * None of this code is reached on our boot path (boot.S calls kernel_main
 * directly and bypasses TinyGo's main/preinit), but the linker still requires
 * every referenced symbol to resolve. We provide minimal, safe stubs here.
 *
 * - `abort` halts the CPU in an infinite hlt loop; if it is ever actually
 *    called that is a bug we want to catch during post-mortem.
 * - The others return a plausible "failure" value or zero so that control
 *    flow stays well-defined in case of accidental dispatch.
 */

    .text

    .global abort
abort:
    cli
1:  hlt
    jmp     1b

    .global write
write:
    xorq    %rax, %rax
    ret

    .global mmap
mmap:
    movq    $-1, %rax
    ret

    .global raise
raise:
    xorq    %rax, %rax
    ret

    .global tinygo_register_fatal_signals
tinygo_register_fatal_signals:
    ret

    /* Mark the stack as non-executable to silence ld.lld's warning. */
    .section .note.GNU-stack,"",@progbits
