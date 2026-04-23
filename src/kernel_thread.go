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

// KernelThread represents a Ring 0 service thread bound to a specific CPU.
type KernelThread struct {
	cpuID     uint32
	entryFn   func()
	state     ThreadState
	nextReady *KernelThread
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

// kernelYield is a placeholder for kernel thread yielding.
// Phase 4.2+: Will switch to next kernel thread on current CPU.
// For now: just returns (threads run sequentially or get preempted by TinyGo).
func kernelYield() {
	// TODO: Phase 4.4 - implement context switch to next ready kernel thread
}
