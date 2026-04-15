/* user/task_stack_amd64.S — goroutine context-switch stubs for
 * Ring-3 user binaries.
 *
 * Verbatim copy of TinyGo's internal/task/task_stack_amd64.S, same
 * pattern as the kernel-side src/task_stack_amd64.S. TinyGo's
 * `-o *.o` build flow does not assemble embedded .S files, so the
 * stubs are re-imported here and linked via user/Makefile.
 * tinygo_pause is a Go function exported from
 * internal/task/task_stack.go and is provided by the TinyGo runtime
 * as linked into the per-command Go object (e.g. build/sh_go.o).
 */

.section .text.tinygo_startTask
.global  tinygo_startTask
tinygo_startTask:
    .cfi_startproc
    /* Root frame — nothing to unwind. */
    .cfi_undefined rip

    /* First arg of the goroutine start wrapper → arg-bundle ptr. */
    movq %r13, %rdi

    /* Branch to the "goroutine start" function. */
    callq *%r12

    /* After return, exit this goroutine (tail call). */
    jmp tinygo_pause
    .cfi_endproc

.section .text.tinygo_swapTask
.global tinygo_swapTask
tinygo_swapTask:
    /* %rdi = newStack uintptr, %rsi = oldStack *uintptr */

    /* Save callee-saved registers. */
    pushq %r15
    pushq %r14
    pushq %r13
    pushq %r12
    pushq %rbp
    pushq %rbx

    /* Save the current stack pointer in oldStack. */
    movq %rsp, (%rsi)

    /* Switch to the new stack pointer. */
    movq %rdi, %rsp

    /* Load saved registers from the new stack. */
    popq %rbx
    popq %rbp
    popq %r12
    popq %r13
    popq %r14
    popq %r15

    /* Return into the new task, as if tinygo_swapTask were a
     * regular call. */
    ret

    /* Non-executable stack marker (matches rt0.S). */
    .section .note.GNU-stack,"",@progbits
