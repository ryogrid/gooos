/*
 * user/rt0.S -- Startup assembly and syscall stubs for gooos userspace.
 *
 * Provides _start (calls TinyGo's main, then sys_exit) and raw syscall
 * wrappers (syscall0..syscall4) used by the gooos Go package.
 *
 * The register mapping for syscallN wrappers:
 *   Go caller (SysV ABI):  RDI=nr, RSI=a1, RDX=a2, RCX=a3, R8=a4
 *   Kernel syscall ABI:    RAX=nr, RDI=a1, RSI=a2, RDX=a3, R10=a4
 * Each wrapper shuffles registers before int $0x80.
 */

    .section .text._start, "ax"
    .global _start
_start:
    /* RSP is set by the kernel before jumping to Ring 3. */
    call    main            /* TinyGo runtime entry → main.main */

    /* If main returns, call sys_exit(0). */
    xor     %edi, %edi
    xor     %eax, %eax      /* syscall 0 = sys_exit */
    int     $0x80

    /* Should not reach here. */
    jmp     .

/* ---------- Raw syscall wrappers ---------------------------------------- */

    .section .text, "ax"

    .global syscall0
syscall0:
    movq    %rdi, %rax
    int     $0x80
    ret

    .global syscall1
syscall1:
    movq    %rdi, %rax       /* nr */
    movq    %rsi, %rdi       /* a1 */
    int     $0x80
    ret

    .global syscall2
syscall2:
    movq    %rdi, %rax       /* nr */
    movq    %rsi, %rdi       /* a1 */
    movq    %rdx, %rsi       /* a2 */
    int     $0x80
    ret

    .global syscall3
syscall3:
    movq    %rdi, %rax       /* nr */
    movq    %rsi, %rdi       /* a1 */
    movq    %rdx, %rsi       /* a2 */
    movq    %rcx, %rdx       /* a3 */
    int     $0x80
    ret

    .global syscall4
syscall4:
    movq    %rdi, %rax       /* nr */
    movq    %rsi, %rdi       /* a1 */
    movq    %rdx, %rsi       /* a2 */
    movq    %rcx, %rdx       /* a3 */
    movq    %r8,  %r10       /* a4 */
    int     $0x80
    ret

/* ---------- Runtime stubs ----------------------------------------------- */

/*
 * TinyGo's runtime (goos=linux) calls these OS functions.
 * We provide minimal stubs that map to gooos syscalls or no-ops.
 */

    .global abort
abort:
    xor     %edi, %edi       /* exit code = 0 */
    xor     %eax, %eax       /* sys_exit */
    int     $0x80
    jmp     .

    .global raise
raise:
    xor     %eax, %eax       /* return 0 (no-op) */
    ret

    .global tinygo_register_fatal_signals
tinygo_register_fatal_signals:
    ret                      /* no-op */

    /* write(fd, buf, count) → sys_write(buf, count, fd) */
    .global write
write:
    /* Go/C: RDI=fd, RSI=buf, RDX=count */
    /* Kernel: RAX=1, RDI=buf, RSI=count, RDX=fd */
    movq    %rdi, %rcx       /* save fd */
    movq    $1, %rax         /* sys_write */
    movq    %rsi, %rdi       /* buf */
    movq    %rdx, %rsi       /* count */
    movq    %rcx, %rdx       /* fd */
    int     $0x80
    ret

    /* mmap(addr, length, prot, flags, fd, offset) → sys_sbrk(length) or fail */
    .global mmap
mmap:
    /* RSI = length. Cap to 1 MiB; return -1 for larger requests. */
    /* TinyGo starts with 1 GiB and halves until success. */
    cmpq    $0x100000, %rsi  /* 1 MiB */
    ja      .mmap_fail
    movq    $10, %rax        /* sys_sbrk */
    movq    %rsi, %rdi       /* increment = length */
    int     $0x80
    cmpq    $-1, %rax
    je      .mmap_fail
    ret
.mmap_fail:
    movq    $-1, %rax
    ret

    /* memcpy(dest, src, n) — required by runtime.stringConcat. */
    .global memcpy
memcpy:
    movq    %rdi, %rax       /* return dest */
    movq    %rdx, %rcx       /* count */
    rep     movsb            /* copy RSI→RDI, RCX bytes */
    ret

    /* memset(dest, c, n) — required by runtime for zeroing memory. */
    .global memset
memset:
    movq    %rdi, %r8        /* save dest for return */
    movb    %sil, %al        /* byte value */
    movq    %rdx, %rcx       /* count */
    rep     stosb            /* fill RDI with AL, RCX times */
    movq    %r8, %rax        /* return dest */
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
