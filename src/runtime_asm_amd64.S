/* src/runtime_asm_amd64.S — partial copy of TinyGo's runtime/asm_amd64.S.
 * Only tinygo_longjmp is imported; tinygo_scanCurrentStack is already
 * provided by src/stubs.S.
 *
 * TinyGo's build does not assemble .S files under our
 * "-o kernel_go.o" flow, so runtime-side asm must be pulled in here.
 */

.section .text.tinygo_longjmp
.global tinygo_longjmp
tinygo_longjmp:
    /* The code we jump to assumes rax is set to a non-zero value if
     * we jump from here, so we use rax as the temporary for jumpPC. */
    movq 0(%rdi), %rsp              /* jumpSP */
    movq 8(%rdi), %rax              /* jumpPC */
    jmpq *%rax
