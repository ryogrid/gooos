/* user/runtime_asm_amd64.S — partial copy of TinyGo's
 * runtime/asm_amd64.S for Ring-3 user binaries. Mirrors the
 * kernel-side src/runtime_asm_amd64.S: TinyGo's build does not
 * assemble .S files under the `tinygo build -o *.o` flow, so
 * runtime-side asm must be pulled in here.
 *
 * Imported symbols:
 *   tinygo_longjmp          — panic-unwinder landing trampoline.
 *   tinygo_scanCurrentStack — conservative-GC stack-scan hook.
 *                             Calls tinygo_scanstack(sp). The
 *                             real body is linked in from
 *                             gc_stack_raw.go under gc=conservative;
 *                             a weak dummy defined below keeps the
 *                             link graph intact under gc=leaking.
 */

.section .text.tinygo_longjmp
.global tinygo_longjmp
tinygo_longjmp:
    /* The landing code assumes rax is set to a non-zero value when
     * we jump from here, so we use rax as the temporary for jumpPC. */
    movq 0(%rdi), %rsp              /* jumpSP */
    movq 8(%rdi), %rax              /* jumpPC */
    jmpq *%rax

/* ---------- tinygo_scanCurrentStack --------------------------------------
 *
 * Byte-for-byte port of src/stubs.S:248-264. Pushes callee-saved
 * registers so their live values are on the stack for
 * tinygo_scanstack to see, then passes the resulting RSP as arg 1.
 * The +8 sub keeps RSP 16-byte aligned across the call per SysV ABI.
 * On return, pop everything (56 bytes = 6 regs × 8 + 8 align pad).
 */

.section .text.tinygo_scanCurrentStack
.global tinygo_scanCurrentStack
tinygo_scanCurrentStack:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $8, %rsp
    movq    %rsp, %rdi
    callq   tinygo_scanstack
    addq    $56, %rsp
    retq

/* Dummy weak tinygo_scanstack. Under gc=conservative the real body
 * in gc_stack_raw.go wins via strong-over-weak precedence. Under
 * gc=leaking this is the only definition, and it's never called
 * (the scheduler's mark phase is a no-op), so the empty retq is
 * correct. Mirrors src/stubs.S:267-269. */
.section .text.tinygo_scanstack
.weak   tinygo_scanstack
tinygo_scanstack:
    retq

    /* Non-executable stack marker (matches rt0.S). */
    .section .note.GNU-stack,"",@progbits
