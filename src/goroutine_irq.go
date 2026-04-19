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
