// src/goroutine_irq.go — ISR depth counter bridged into the TinyGo
// runtime's interrupt package via //go:linkname (see
// scripts/patch_tinygo_runtime.sh and
// /home/ryo/.local/tinygo/src/runtime/interrupt/interrupt_gooos.go).
//
// inInterruptDepth is incremented by the common ISR prologue in
// src/isr.S and decremented by the epilogue; interrupt.In() returns
// (inInterruptDepth != 0). Required so TinyGo's task.Pause() can
// refuse to park a goroutine from ISR context.

package main

// inInterruptDepth is 0 in normal (non-ISR) execution. src/isr.S
// increments it in the common prologue and decrements in the epilogue.
// Linked from runtime/interrupt/interrupt_gooos.go via //go:linkname.
var inInterruptDepth uint32
