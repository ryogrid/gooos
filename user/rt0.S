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

    /* write(fd, buf, count) → sys_write(fd, buf, count) — direct.
     * Since the 1c ABI shift, kernel sys_write takes (fd, buf, count)
     * in RDI/RSI/RDX, matching the C/Go calling convention exactly,
     * so no register shuffle is needed. */
    .global write
write:
    movq    $1, %rax         /* sys_write */
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

    /* memmove(dest, src, n) — required by runtime.sliceAppend.
     * Handles overlap: copy backward when dest > src && dest < src+n. */
    .global memmove
memmove:
    movq    %rdi, %rax       /* return dest */
    movq    %rdx, %rcx       /* count */
    cmpq    %rsi, %rdi
    jbe     .memmove_fwd     /* dest <= src → forward copy */
    movq    %rsi, %r8
    addq    %rdx, %r8
    cmpq    %r8, %rdi
    jae     .memmove_fwd     /* dest >= src+n → no overlap */
    /* Backward copy: rsi += n-1, rdi += n-1, std, rep movsb, cld. */
    leaq    -1(%rsi,%rdx), %rsi
    leaq    -1(%rdi,%rdx), %rdi
    std
    rep     movsb
    cld
    ret
.memmove_fwd:
    rep     movsb
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

/* ---------- Synthetic ELF header for findGlobals() ----------------------
 *
 * runtime/os_linux.go:findGlobals() parses the Elf64 header at
 * __ehdr_start to locate writable PT_LOAD segments for conservative-GC
 * root scanning. The kernel's elfSpawn/elfLoad copies PT_LOAD segments
 * only — the real ELF header is NOT present in the user address space.
 * We synthesize one in .rodata describing exactly the globals region
 * (_globals_start..end). Mirrors src/stubs.S:341-375; user-defined
 * __ehdr_start overrides ld.lld's auto-defined hidden symbol.
 */

    .section .rodata,"a",@progbits
    .align 8
    .global __ehdr_start
__ehdr_start:
    /* --- Elf64_Ehdr (64 bytes) --- */
    .byte   0x7f, 0x45, 0x4c, 0x46   /* e_ident[0..3]: ELF magic */
    .byte   2                         /* ELFCLASS64 */
    .byte   1                         /* ELFDATA2LSB */
    .byte   1                         /* EV_CURRENT */
    .byte   0                         /* ELFOSABI_NONE */
    .byte   0                         /* ABI version */
    .zero   7                         /* e_ident[9..15]: padding */
    .short  2                         /* e_type: ET_EXEC */
    .short  0x3E                      /* e_machine: EM_X86_64 */
    .long   1                         /* e_version: EV_CURRENT */
    .quad   0                         /* e_entry (unused by findGlobals) */
    .quad   64                        /* e_phoff: phdr right after ehdr */
    .quad   0                         /* e_shoff (unused) */
    .long   0                         /* e_flags */
    .short  64                        /* e_ehsize */
    .short  56                        /* e_phentsize */
    .short  1                         /* e_phnum: 1 program header */
    .short  0                         /* e_shentsize (unused) */
    .short  0                         /* e_shnum (unused) */
    .short  0                         /* e_shstrndx (unused) */

    /* --- Elf64_Phdr (56 bytes) — writable globals region --- */
    .long   1                         /* p_type: PT_LOAD */
    .long   6                         /* p_flags: PF_R | PF_W */
    .quad   0                         /* p_offset (unused) */
    .quad   _globals_start            /* p_vaddr */
    .quad   0                         /* p_paddr (unused) */
    .quad   0                         /* p_filesz (unused by findGlobals) */
    .quad   _globals_size             /* p_memsz */
    .quad   0                         /* p_align (unused) */

/* ---------- User heap (1 MiB, @nobits) ------------------------------------
 *
 * Collected by user/linker_user.ld's .heap output section, bracketed by
 * _heap_start / _heap_end. @nobits so the ELF loader zero-fills the 1 MiB
 * at load without bloating the on-disk ELF. Mirrors kernel .heap at
 * src/stubs.S:381. baremetal.go:growHeap returns false — this is the
 * per-process hard ceiling for TinyGo's conservative GC.
 */

    .section .heap,"aw",@nobits
    .skip 0x100000

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
