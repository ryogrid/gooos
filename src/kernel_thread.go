// src/kernel_thread.go — Kernel thread abstraction for Ring 0 services.
//
// This module provides explicit thread management for kernel services
// without relying on TinyGo's goroutine scheduler.
//
// Key design:
// - KernelThread: Ring 0 service thread pinned to specific CPU
// - Ready queues: FIFO per-CPU, independent of TinyGo scheduler
// - Lazy stack allocation: Avoids large static arrays that slow TinyGo compiler
//
// Motivation: TinyGo's work-stealing scheduler moves goroutines between CPUs
// unpredictably. For time-critical services like timerDispatcher, we need
// deterministic CPU assignment: each timer dispatch runs independently on its CPU.
//
// See impldoc/smp_m4_kernel_threads.md for full design.

package main

// SavedContext holds saved CPU register state for context switching.
type SavedContext struct {
	rax, rbx, rcx, rdx uintptr
	rsi, rdi, rbp      uintptr
	r8, r9, r10, r11   uintptr
	r12, r13, r14, r15 uintptr
	rip, rsp           uintptr
	// Note: We don't save segment registers (CS, DS, SS, etc) - they're static in kernel
}

// KernelThread represents a Ring 0 service thread bound to a specific CPU.
type KernelThread struct {
	cpuID    uint32
	entryFn  func()
	state    ThreadState
	nextReady *KernelThread
	context  SavedContext // CPU state for context switching
}

// ThreadState enum
type ThreadState uint8

const (
	ThreadReady ThreadState = iota
	ThreadRunning
	ThreadBlocked
	ThreadTerminated
)

// Lazy-allocated stacks (per CPU)
// var kernelStacks [maxCPUs]uintptr // TODO: implement if context switching needed

// Ready queues: one FIFO queue per CPU for kernel threads
var kernelReadyQueues [maxCPUs]*KernelThread

const kernelStackSize = 16 * pageSize // 64 KiB per CPU (if allocated)

// kernelThreadInit initializes the kernel thread system.
// Called during boot before APs are started.
func kernelThreadInit() {
	// No explicit initialization needed - arrays are zero-initialized
	// This is a no-op, but kept for future reference (Phase 4.4+)
}

// kernelThreadSpawn queues fn to run on the specified CPU.
// The function is not immediately executed; it will run when the CPU
// scheduler decides (Phase 4.2+).
//
// Safe to call from interrupt context (acquires no locks).
func kernelThreadSpawn(cpuID uint32, fn func()) {
	if cpuID >= uint32(maxCPUs) {
		serialPrintln("kernelThreadSpawn: invalid cpuID " + utoa(uint64(cpuID)))
		return
	}

	if fn == nil {
		serialPrintln("kernelThreadSpawn: nil function")
		return
	}

	kt := &KernelThread{
		cpuID:     cpuID,
		entryFn:   fn,
		state:     ThreadReady,
		nextReady: nil,
	}

	// Add to ready queue for this CPU (FIFO)
	if kernelReadyQueues[cpuID] == nil {
		kernelReadyQueues[cpuID] = kt
	} else {
		// Find tail
		tail := kernelReadyQueues[cpuID]
		for tail.nextReady != nil {
			tail = tail.nextReady
		}
		tail.nextReady = kt
	}
}

// kernelThreadGetReady returns the next ready kernel thread for the current CPU.
// Returns nil if no threads are ready.
func kernelThreadGetReady() *KernelThread {
	cpu := cpuID()
	if cpu >= uint32(maxCPUs) {
		return nil
	}
	return kernelReadyQueues[cpu]
}

// kernelThreadPopReady removes and returns the next ready kernel thread
// for the current CPU. Updates state to Running.
func kernelThreadPopReady() *KernelThread {
	cpu := cpuID()
	if cpu >= uint32(maxCPUs) {
		return nil
	}
	kt := kernelReadyQueues[cpu]
	if kt != nil {
		kernelReadyQueues[cpu] = kt.nextReady
		kt.nextReady = nil
		kt.state = ThreadRunning
	}
	return kt
}

// Per-CPU current kernel thread tracking
var currentKernelThread [maxCPUs]*KernelThread

// kernelThreadSwitch switches from current thread to 'next' thread.
// Phase 4.3: Direct invocation (no context switching - will be Phase 4.4).
// 
// Executes next thread's entry function directly until it yields.
func kernelThreadSwitch(next *KernelThread) {
	if next == nil || next.entryFn == nil {
		return
	}
	
	// Direct invocation - thread runs until it yields or completes
	next.entryFn()
}

// kernelYield yields the current kernel thread to the next ready thread on this CPU.
// Phase 4.3: Direct sequential invocation of queued functions.
func kernelYield() {
	cpu := cpuID()
	if cpu >= uint32(maxCPUs) {
		return
	}
	
	// Get next ready kernel thread on this CPU
	next := kernelThreadPopReady()
	if next != nil {
		// Track current thread
		currentKernelThread[cpu] = next
		// Execute next thread's function
		kernelThreadSwitch(next)
		// After thread completes or yields, we continue here
	}
}
