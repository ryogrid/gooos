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
    /* These return the address of //export Go functions so the scheduler    */
    /* can place them on the initial stack as the return address.            */

    .global demoTaskAAddr
demoTaskAAddr:
    leaq    demoTaskA(%rip), %rax
    ret

    .global demoTaskBAddr
demoTaskBAddr:
    leaq    demoTaskB(%rip), %rax
    ret

    .global demoTaskCAddr
demoTaskCAddr:
    leaq    demoTaskC(%rip), %rax
    ret

    .global taskReturnHaltAddr
taskReturnHaltAddr:
    leaq    taskReturnHalt(%rip), %rax
    ret

    /* ---- Channel test task address helpers ------------------------------- */

    .global chanProducerTaskAddr
chanProducerTaskAddr:
    leaq    chanProducerTask(%rip), %rax
    ret

    .global chanConsumerTaskAddr
chanConsumerTaskAddr:
    leaq    chanConsumerTask(%rip), %rax
    ret

    .global chanRendezvousAAddr
chanRendezvousAAddr:
    leaq    chanRendezvousA(%rip), %rax
    ret

    .global chanRendezvousBAddr
chanRendezvousBAddr:
    leaq    chanRendezvousB(%rip), %rax
    ret

    /* ---- Select test task address helpers ---------------------------------- */

    .global selectTestTaskAddr
selectTestTaskAddr:
    leaq    selectTestTask(%rip), %rax
    ret

    .global selectProducerAAddr
selectProducerAAddr:
    leaq    selectProducerA(%rip), %rax
    ret

    .global selectProducerBAddr
selectProducerBAddr:
    leaq    selectProducerB(%rip), %rax
    ret

    /* ---- Keyboard consumer task address helper ------------------------------ */

    .global keyboardConsumerTaskAddr
keyboardConsumerTaskAddr:
    leaq    keyboardConsumerTask(%rip), %rax
    ret

    /* ---- Serial task address helper ----------------------------------------- */

    .global serialTaskEntryAddr
serialTaskEntryAddr:
    leaq    serialTaskEntry(%rip), %rax
    ret

    /* ---- FS task address helpers ------------------------------------------- */

    .global fsTaskEntryAddr
fsTaskEntryAddr:
    leaq    fsTaskEntry(%rip), %rax
    ret

    .global fsDemoTaskAddr
fsDemoTaskAddr:
    leaq    fsDemoTask(%rip), %rax
    ret

    /* ---- User print task address helper ---------------------------------------- */

    .global userPrintTaskAddr
userPrintTaskAddr:
    leaq    userPrintTask(%rip), %rax
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
