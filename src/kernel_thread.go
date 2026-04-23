// src/kernel_thread.go — Kernel thread abstraction for Ring 0 services.
//
// Phase 4.4 target: gooos owns per-CPU kernel-thread scheduling for
// long-lived kernel services (timerDispatcher, netRxLoop, fsTask,
// tcpRTOScannerLoop) so scheduling is deterministic per CPU and
// independent of TinyGo's task scheduler migrations. Context
// switching uses SavedContext + a dedicated per-CPU stack; entry
// is via the ISR-safe ready-queue pop + direct invocation path
// landed in Phase 4.3, extended with a real context swap.
//
// Design invariants:
//
//  1. kernelThreadSpawn is ISR-safe: allocation-free (slot pool),
//     no lock acquired, no channel op. The per-CPU ready-queue
//     head is only mutated on its own CPU (pop) or at init (spawn
//     onto a different CPU's queue, which is safe because the
//     target CPU is not running when spawned at init).
//
//  2. kernelYield pops the head for the current CPU and direct-
//     invokes or context-swaps into it. Re-entrancy is controlled
//     by `currentKernelThread[cpu] != nil`; nested yields are
//     no-ops.
//
//  3. Stacks are lazy-allocated on first use via allocPagesContig;
//     `kernelStackSize` is 16 pages (64 KiB) matching the Ring-3
//     pool size.

package main

// SavedContext holds callee-saved CPU state for context switching.
// Layout must match kernel_thread_swap.S expectations.
type SavedContext struct {
	rbx, rbp      uintptr
	r12, r13, r14 uintptr
	r15           uintptr
	rsp           uintptr
	// rip is implicit via the return address on the saved stack.
	// CS/DS/SS are static in kernel, not saved.
}

// KernelThread represents a Ring 0 service thread bound to a
// specific CPU.
type KernelThread struct {
	cpuID     uint32
	entryFn   func()
	state     ThreadState
	nextReady *KernelThread
	context   SavedContext
	stackBase uintptr // page-aligned low address of this thread's stack
	stackTop  uintptr // high address (initial RSP)
	inUse     uint32  // 1 if slot is occupied in ktPool
}

// ThreadState enum
type ThreadState uint8

const (
	ThreadFree ThreadState = iota
	ThreadReady
	ThreadRunning
	ThreadBlocked
	ThreadTerminated
)

// ktPoolSize — worst case today: one thread per long-lived kernel
// service per CPU. With ~6 services planned (timerDispatcher,
// netRxLoop, fsTask, tcpRTOScanner, udpEchoServer, tcpEchoServer)
// and 17 CPUs, a pool of 128 is generous. Update if the migration
// adds more services.
const ktPoolSize = 128

var ktPool [ktPoolSize]KernelThread

// kernelThreadSpawnDrops counts pool-exhaustion drops; reported by
// netDiag so a bug is visible without breaking ISR-safety.
var kernelThreadSpawnDrops uint32

// Ready queues: one FIFO per CPU.
var kernelReadyQueues [maxCPUs]*KernelThread

// currentKernelThread[cpu] is the KT executing on cpu, or nil.
var currentKernelThread [maxCPUs]*KernelThread

const kernelStackSize = 16 * pageSize // 64 KiB per kernel thread

// kernelThreadInit initializes the kernel thread system. Must be
// called during boot before APs are started.
func kernelThreadInit() {
	for i := 0; i < ktPoolSize; i++ {
		ktPool[i].state = ThreadFree
		ktPool[i].inUse = 0
	}
}

// ktPoolAlloc pops the first free slot from the pool. Returns nil
// if the pool is exhausted. No lock: this is called from spawn-time
// which is single-threaded before APs come online, or from ISR
// context with interrupts already disabled by the ISR prologue.
//
//go:nosplit
func ktPoolAlloc() *KernelThread {
	for i := 0; i < ktPoolSize; i++ {
		if ktPool[i].inUse == 0 {
			ktPool[i].inUse = 1
			ktPool[i].state = ThreadReady
			ktPool[i].nextReady = nil
			return &ktPool[i]
		}
	}
	return nil
}

// kernelThreadSpawn queues fn to run on the specified CPU. The
// function is not immediately executed; it runs when kernelYield
// picks it up. Safe to call from interrupt context: no allocation,
// no lock.
//
//go:nosplit
func kernelThreadSpawn(cpuID uint32, fn func()) {
	if cpuID >= uint32(maxCPUs) {
		return
	}
	if fn == nil {
		return
	}

	kt := ktPoolAlloc()
	if kt == nil {
		// Pool exhausted. Drop silently to keep nosplit; a bug that
		// hits this branch will be visible via kernelThreadSpawnDrops.
		kernelThreadSpawnDrops++
		return
	}
	kt.cpuID = cpuID
	kt.entryFn = fn

	// Append to ready queue for this CPU (FIFO).
	if kernelReadyQueues[cpuID] == nil {
		kernelReadyQueues[cpuID] = kt
	} else {
		tail := kernelReadyQueues[cpuID]
		for tail.nextReady != nil {
			tail = tail.nextReady
		}
		tail.nextReady = kt
	}
}

// kernelThreadGetReady returns the next ready kernel thread for
// the current CPU without dequeueing. Returns nil if none.
//
//go:nosplit
func kernelThreadGetReady() *KernelThread {
	cpu := cpuID()
	if cpu >= uint32(maxCPUs) {
		return nil
	}
	return kernelReadyQueues[cpu]
}

// kernelThreadPopReady dequeues the next ready kernel thread for
// the current CPU. Returns nil if none. Updates state to Running.
//
//go:nosplit
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

// kernelThreadSwitch switches from the current execution to the
// specified kernel thread. Phase 4.3: direct invocation (no
// context switching). Phase 4.4 extends this to a real swap once
// per-CPU stacks are allocated and a swap stub is in place.
func kernelThreadSwitch(next *KernelThread) {
	if next == nil || next.entryFn == nil {
		return
	}
	next.entryFn()
	// Thread completed its entry; mark it terminated and free the
	// pool slot so long-lived re-spawns cannot leak the pool.
	next.state = ThreadTerminated
	next.inUse = 0
}

// kernelYield yields the current kernel thread to the next ready
// thread on this CPU. Phase 4.3 semantics: direct sequential
// invocation of queued functions; returns when the invoked function
// returns. Re-entrant yields from within the invoked function are
// no-ops (currentKernelThread is set).
//
//go:nosplit
func kernelYield() {
	cpu := cpuID()
	if cpu >= uint32(maxCPUs) {
		return
	}
	if currentKernelThread[cpu] != nil {
		// Already running one; don't recurse.
		return
	}
	next := kernelThreadPopReady()
	if next == nil {
		return
	}
	currentKernelThread[cpu] = next
	kernelThreadSwitch(next)
	currentKernelThread[cpu] = nil
}
