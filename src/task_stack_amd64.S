/* src/task_stack_amd64.S — goroutine context-switch stubs.
 *
 * Copy of $HOME/.local/tinygo0.40.1/src/internal/task/task_stack_amd64.S.
 * TinyGo's build pipeline does not assemble .S files under our
 * "tinygo build -o *.o" flow, so the stubs are imported here and
 * linked via the existing Makefile. tinygo_task_exit is a Go function
 * exported from internal/task/task_stack.go (renamed from the 0.33.0
 * tinygo_pause; see impldoc/smp_migration_overview.md) and is provided
 * by the TinyGo runtime as linked into kernel_go.o.
 */

.section .text.tinygo_startTask
.global  tinygo_startTask
tinygo_startTask:
    .cfi_startproc
    /* Indicate to the unwinder that there is nothing to unwind, this
     * is the root frame. */
    .cfi_undefined rip

    /* Set the first argument of the goroutine start wrapper, which
     * contains all the arguments. */
    movq %r13, %rdi

    /* Branch to the "goroutine start" function. */
    callq *%r12

    /* After return, exit this goroutine. This is a tail call. */
    jmp tinygo_task_exit
    .cfi_endproc

.section .text.tinygo_swapTask
.global tinygo_swapTask
tinygo_swapTask:
    /* %rdi = newStack uintptr, %rsi = oldStack *uintptr */

    /* Save all callee-saved registers. */
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

    /* Return into the new task, as if tinygo_swapTask was a regular
     * call. */
    ret
