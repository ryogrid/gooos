/*
 * src/switch.S -- Phase B leftovers.
 *
 * The hand-written scheduler was retired in Phase B; switchContext is
 * gone (TinyGo's tinygo_swapTask does context switching under
 * scheduler=tasks). Only taskReturnHalt (safety net) and
 * elfExecTrampolineAddr (referenced by src/process.go's legacy stub)
 * survive. Both may be deleted entirely in a future cleanup pass if
 * ring3Wrapper is proven to never bottom out into either path.
 */

    .text

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
