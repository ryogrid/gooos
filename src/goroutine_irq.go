// src/goroutine_irq.go — ISR-depth counter accessors.
//
// Dual-counter approach: the ISR prologue/epilogue in src/isr.S
// increments BOTH the global gooos_in_interrupt_depth (used by
// TinyGo's interrupt.In() for reliable early-boot checks) and
// the per-CPU %gs:4 counter (SMP-safe, used by kernel code via
// readInterruptDepth).

package main

// Declared here so Go callers have a handle; defined in src/isr.S.
//
//go:linkname gooosInInterruptDepth gooos_in_interrupt_depth
var gooosInInterruptDepth uint32

// Reference prevents the Go toolchain from dropping the linkname.
//
//go:noinline
func readInInterruptDepth() uint32 { return gooosInInterruptDepth }

var _ = readInInterruptDepth

// readInterruptDepth reads the per-CPU interrupt depth counter
// from %gs:4. Implemented in stubs.S.
//
//go:nosplit
//go:linkname readInterruptDepth readInterruptDepth
func readInterruptDepth() uint32

// interruptIn returns true if the current CPU is inside an ISR.
// Uses the per-CPU counter (SMP-safe).
//
//go:nosplit
func interruptIn() bool {
	return readInterruptDepth() != 0
}
