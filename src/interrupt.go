// src/interrupt.go -- Go-side interrupt dispatcher (table-driven).
//
// Assembly ISR stubs (isr.S) save registers and call go_interrupt_handler
// with the vector number, error code, and register frame pointer. This file
// maintains a handler table indexed by vector and dispatches accordingly.
// Vector 0x80 (int 0x80) is special-cased for syscall dispatch.

package main

import "unsafe"

// InterruptHandler is a function that handles a specific interrupt vector.
type InterruptHandler func(vector uint64)

// handlers is the table of registered interrupt handlers, indexed by vector.
var handlers [256]InterruptHandler

// Per-CPU last error code and frame pointer. Under SMP, each CPU
// has its own ISR context; using globals would race between CPUs.
var lastErrorCodes [maxCPUs]uint64
var lastFramePtrs  [maxCPUs]uintptr

// registerHandler registers a Go function for a given interrupt vector.
func registerHandler(vector int, handler InterruptHandler) {
	handlers[vector] = handler
}

// go_interrupt_handler is the assembly-to-Go entry point for all interrupts.
// Called from isr_common (isr.S) with vector in %rdi, error code in %rsi,
// and register frame pointer in %rdx.
//
//export go_interrupt_handler
func go_interrupt_handler(vector uint64, errorCode uint64, framePtr uintptr) {
	idx := cpuID()
	lastErrorCodes[idx] = errorCode
	lastFramePtrs[idx] = framePtr
	if vector == 0x80 {
		syscallDispatch((*SyscallFrame)(unsafe.Pointer(framePtr)))
		return
	}
	if vector < 256 && handlers[vector] != nil {
		handlers[vector](vector)
	}
}
