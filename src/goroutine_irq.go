// src/goroutine_irq.go — ISR-depth + syscall-depth accessors.
//
// The legacy global gooos_in_interrupt_depth was retired in M2
// (impldoc/smp_m2_ap_lapic_timer.md). The per-CPU counter at %gs:4
// now provides SMP-safe ISR-depth accounting; a second per-CPU
// counter at %gs:44 tracks syscall-dispatch depth so runtime
// interrupt.In() can return false during syscall handlers (letting
// task.Pause() proceed on a blocking syscall).

package main

import "unsafe"

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

	// Recover the trap frame captured by isr.S. `lastFramePtrs` is
	// the per-CPU slot populated by go_interrupt_handler before
	// dispatching to this handler. Interpreting the bytes as a
	// SyscallFrame lets us read the interrupted RIP/CS/RSP/SS and,
	// for Ring 3 preemption (feature 2.2), rewrite them in place
	// before the ISR epilogue's iretq.
	cpu := cpuID()
	framePtr := lastFramePtrs[cpu]
	if framePtr != 0 {
		frame := (*SyscallFrame)(unsafe.Pointer(framePtr))
		// Low 2 bits of CS = RPL. RPL==3 → interrupted Ring 3.
		if frame.CS&3 == 3 {
			// Ring 3: deliver SIGALRM if a handler is registered.
			// maybeDeliverSignal rewrites frame.RIP / frame.RSP in
			// place; on iretq the user process jumps to its
			// SIGALRM handler instead of the interrupted RIP.
			maybeDeliverSignal(frame)
			// Do NOT Gosched — returning from this handler lets the
			// ISR epilogue iretq directly back to Ring 3, either
			// at the handler (if we rewrote) or at the interrupted
			// RIP (if we didn't).
			return
		}
	}

	// Ring 0: kernel goroutine preemption via cooperative swap.
	if taskCurrent() == 0 {
		return
	}
	perCPUBlocks[cpu].WantReschedule = 0
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
