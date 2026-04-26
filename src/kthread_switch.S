/*
 * src/kthread_switch.S -- kernel-thread context switch + trampoline.
 *
 * kschedSwitch(new *KernelThread, old *KernelThread)
 *   %rdi = new, %rsi = old
 *   Saves callee-saved regs + RFLAGS onto the current stack, writes
 *   the resulting RSP into *(old+0) (= old.SavedRSP), loads new.SavedRSP
 *   into RSP, pops regs + RFLAGS, returns into the new context.
 *
 * kschedEnter
 *   Trampoline used on the first switch-into a newly-spawned thread.
 *   The initial frame pre-loads %r12 = entry func pointer and %r13 =
 *   arg. kschedEnter marshals them into System V calling convention
 *   (rdi = arg) and tail-calls the entry. If the entry ever returns,
 *   kschedExitNoreturn halts the thread cleanly.
 *
 * kschedEnterAddr
 *   Returns the address of kschedEnter in %rax. Used by kschedSpawn
 *   (src/kthread_lifecycle.go) to build the initial frame.
 *
 * Design: no_goroutine_kernel_design/02_kernel_thread_runtime.md
 *         §"Context-switch register contract".
 */

    .text

    /* ---- kschedSwitch ---- */
    .global kschedSwitch
kschedSwitch:
    .cfi_startproc

    /*
     * Disable interrupts for the duration of the swap so no ISR
     * fires between storing old RSP and loading new RSP. popfq at
     * the bottom restores whatever IF state the NEW thread had.
     */
    pushfq
    cli

    /* Save callee-saved registers. Order (low to high on stack):
     *   RBX, RBP, R12, R13, R14, R15, RFLAGS, RIP. */
    pushq %r15
    pushq %r14
    pushq %r13
    pushq %r12
    pushq %rbp
    pushq %rbx

    /* Store the post-push RSP into old.SavedRSP (offset 0). */
    movq %rsp, (%rsi)

    /* Load new.SavedRSP into RSP. */
    movq (%rdi), %rsp

    /* Restore callee-saved registers from the new thread's stack. */
    popq %rbx
    popq %rbp
    popq %r12
    popq %r13
    popq %r14
    popq %r15

    /* Restore RFLAGS (possibly re-enabling IF). */
    popfq

    /* Return into the new context (pop RIP). */
    ret
    .cfi_endproc

    /* ---- kschedEnter (initial-frame trampoline) ---- */
    .global kschedEnter
kschedEnter:
    .cfi_startproc
    /* Root frame: unwinder stops here. */
    .cfi_undefined rip

    /* %r13 = *KernelThread, baked into the initial frame by
     * kschedSpawn. Hand it to a Go helper that performs the actual
     * call; that keeps the TinyGo func-value ABI on the Go side. */
    movq %r13, %rdi
    callq kschedRunEntry

    /* If the entry ever returns, clean up via a Go-side exit. */
    jmp kschedExitNoreturn

    .cfi_endproc

    /* ---- kschedEnterAddr ---- */
    /* Go helper: returns &kschedEnter in %rax. */
    .global kschedEnterAddr
kschedEnterAddr:
    leaq    kschedEnter(%rip), %rax
    ret

    /* Mark the stack as non-executable. */
    .section .note.GNU-stack,"",@progbits
