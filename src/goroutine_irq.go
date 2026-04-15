// src/goroutine_irq.go — Go-side reference to the ISR-depth counter
// defined as a .bss symbol in src/isr.S (`gooos_in_interrupt_depth`).
//
// The TinyGo runtime's interrupt package (patched via
// scripts/patch_tinygo_runtime.sh) reads this counter to implement
// interrupt.In(). src/isr.S increments it in the common ISR prologue
// and decrements in the epilogue. The symbol must be defined in
// assembly because TinyGo's dead-code eliminator strips Go-defined
// variables that are only referenced from assembly and across the
// //go:linkname boundary.

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
