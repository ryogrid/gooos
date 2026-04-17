// src/goroutine_irq.go — ISR-depth counter accessors.
//
// SMP v2: the ISR prologue/epilogue in src/isr.S increments BOTH
// the global gooos_in_interrupt_depth (read by TinyGo's
// interrupt.In() via linkname) and the per-CPU %gs:4 counter
// (used by gooos code for SMP-safe depth checks).
//
// The global variable bridge must be kept alive so TinyGo's
// interrupt_gooos.go can resolve its linkname.

package main

// Declared here so Go callers have a handle; defined in src/isr.S.
//
//go:linkname gooosInInterruptDepth gooos_in_interrupt_depth
var gooosInInterruptDepth uint32

// Reference prevents the Go toolchain from dropping the linkname on
// the var, which in turn keeps the cross-unit reference resolvable.
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
