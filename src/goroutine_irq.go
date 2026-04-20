// src/goroutine_irq.go — ISR-depth + syscall-depth accessors.
//
// The legacy global gooos_in_interrupt_depth was retired in M2
// (impldoc/smp_m2_ap_lapic_timer.md). The per-CPU counter at %gs:4
// now provides SMP-safe ISR-depth accounting; a second per-CPU
// counter at %gs:44 tracks syscall-dispatch depth so runtime
// interrupt.In() can return false during syscall handlers (letting
// task.Pause() proceed on a blocking syscall).

package main

// readInterruptDepth reads the per-CPU ISR-depth counter from %gs:4.
// Implemented in src/stubs.S.
//
//go:nosplit
//go:linkname readInterruptDepth readInterruptDepth
func readInterruptDepth() uint32

// readSyscallDepth reads the per-CPU syscall-dispatch depth counter
// from %gs:44. Implemented in src/stubs.S.
//
//go:nosplit
//go:linkname readSyscallDepth readSyscallDepth
func readSyscallDepth() uint32

// interruptIn returns true if the current CPU is inside an ISR but
// NOT inside a syscall dispatch. Mirrors the policy enforced by the
// patched TinyGo runtime interrupt.In() (interrupt_gooos.go).
//
//go:nosplit
func interruptIn() bool {
	return readInterruptDepth() != 0 && readSyscallDepth() == 0
}

// readPreemptDisable reads the per-CPU preempt-nesting counter from
// %gs:48. Bumped by spinlockAcquire, dropped by spinlockRelease
// (feature 2.1). The preempt ISR (handlePreemptIPI) early-returns
// when this counter is > 0 so a kernel goroutine holding a spinlock
// is not preempted mid-critical-section. Implemented in src/stubs.S.
//
//go:nosplit
//go:linkname readPreemptDisable readPreemptDisable
func readPreemptDisable() uint32

// handlePreemptIPI is the vector-0xFB handler invoked on every
// online core on each BSP LAPIC timer tick when preemptEnabled.
//
// isr.S treats vector 0xFB like vector 0x80 for depth-counter
// purposes: both InterruptDepth (%gs:4) and SyscallDepth (%gs:44)
// are bumped on entry. This lets interrupt.In() return false during
// the handler (since SyscallDepth > 0), which in turn lets
// runtime.Gosched() run from here without tripping the runtime's
// "blocked inside interrupt" panic.
//
// Safe-point policy (per impldoc/preempt_kernel_goroutines.md §2.3):
//
//   - InterruptDepth > 1 → we're nested inside another ISR; bail
//     (the outer ISR's epilogue will iretq back to its caller).
//   - PreemptDisable > 0 → a spinlock is held; bail and set
//     WantReschedule so the critical-section exit path can observe
//     it later.
//   - SyscallDepth > 1 → a real int 0x80 syscall handler is active
//     above us (ours bumped to 1, so a second bump means nesting);
//     bail to keep the kernel syscall path nosplit-safe.
//
// Otherwise call runtime.Gosched() to force a cooperative yield.
// The handler returns normally; isr.S epilogue restores registers
// and iretq-s to the preempted instruction.
//
//go:nosplit
func handlePreemptIPI(vector uint64) {
	lapicSendEOI()

	if readInterruptDepth() > 1 {
		return
	}
	if readPreemptDisable() > 0 {
		perCPUBlocks[cpuID()].WantReschedule = 1
		return
	}
	if readSyscallDepth() > 1 {
		return
	}
	// If the CPU is currently in its scheduler loop (between tasks
	// or hlt-idling), taskCurrent() is 0 and task.Pause() would
	// dereference nil. Bail in that case — preemption only applies
	// to a running task.
	if taskCurrent() == 0 {
		return
	}

	// Reset the reschedule hint; we're about to act on it.
	perCPUBlocks[cpuID()].WantReschedule = 0

	// task.Pause() inside Gosched() swaps to the scheduler stack,
	// picks another task, and eventually returns here when our task
	// is resumed. The 15 GPRs and hardware-iretq-frame pushed by
	// isr.S sit untouched at the top of our kernel stack — they are
	// correctly popped + iretq-ed by the normal ISR epilogue when we
	// return.
	gooosSchedulerYield()
}

// gooosSchedulerYield is a //go:linkname wrapper around
// runtime.Gosched(). Calling runtime.Gosched directly from this file
// would require importing "runtime", which would pull in the
// non-nosplit wrappers; the linkname keeps the ISR handler's call
// chain minimal.
//
//go:linkname gooosSchedulerYield runtime.Gosched
func gooosSchedulerYield()
