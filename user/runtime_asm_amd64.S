/* user/runtime_asm_amd64.S — partial copy of TinyGo's
 * runtime/asm_amd64.S for Ring-3 user binaries. Mirrors the
 * kernel-side src/runtime_asm_amd64.S: TinyGo's build does not
 * assemble .S files under the `tinygo build -o *.o` flow, so
 * runtime-side asm must be pulled in here.
 *
 * Only tinygo_longjmp is imported. tinygo_scanCurrentStack would
 * be needed under gc=conservative; userspace stays on gc=leaking
 * in v1 so it is not required yet.
 */

.section .text.tinygo_longjmp
.global tinygo_longjmp
tinygo_longjmp:
    /* The landing code assumes rax is set to a non-zero value when
     * we jump from here, so we use rax as the temporary for jumpPC. */
    movq 0(%rdi), %rsp              /* jumpSP */
    movq 8(%rdi), %rax              /* jumpPC */
    jmpq *%rax

    /* Non-executable stack marker (matches rt0.S). */
    .section .note.GNU-stack,"",@progbits
