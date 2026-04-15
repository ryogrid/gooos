/*
 * src/switch.S -- Context switch and task entry helpers for the scheduler.
 *
 * switchContext saves callee-saved registers and RSP for the current task,
 * loads the new task's RSP, restores its callee-saved registers, and returns.
 *
 * Task entry address helpers return the address of //export Go functions
 * so createTask can place them on the initial stack.
 */

    .text

    /* ---- switchContext: cooperative context switch ---------------------- */
    /* switchContext(oldSP *uintptr, newSP uintptr)                         */
    /* SysV ABI: rdi = pointer to save current RSP, rsi = new RSP to load.  */
    /*                                                                       */
    /* Saves callee-saved registers (rbx, rbp, r12-r15) on the current       */
    /* stack, writes RSP to *oldSP, loads newSP into RSP, restores the       */
    /* callee-saved registers from the new stack, and returns.               */
    .global switchContext
switchContext:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15

    movq    %rsp, (%rdi)        /* save current RSP to *oldSP */
    movq    %rsi, %rsp          /* switch to new stack */

    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbp
    popq    %rbx

    ret

    /* ---- taskReturnHalt: safety net if a task function returns ---------- */
    /* A task entry function should loop forever. If it returns, halt.       */
    .global taskReturnHalt
taskReturnHalt:
    cli
1:  hlt
    jmp     1b

    /* ---- Address helpers for Go task entry functions --------------------- */
    /* Phase B retired every *TaskAddr stub except the two still
     * referenced from Go: taskReturnHaltAddr (safety-net return for
     * ring3Wrapper's eventual exit path) and elfExecTrampolineAddr
     * (still referenced from process.go's ring3Wrapper path). All
     * others were for dead goroutines and have been deleted. */

    .global taskReturnHaltAddr
taskReturnHaltAddr:
    leaq    taskReturnHalt(%rip), %rax
    ret

    .global elfExecTrampolineAddr
elfExecTrampolineAddr:
    leaq    elfExecTrampoline(%rip), %rax
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
