// src/kthread_sched.go -- Route C per-CPU kernel-thread scheduler.
//
// Minimal M0 scope: per-CPU ready queue, sticky placement with
// work-stealing, a bootstrap anchor per CPU, and a round-robin
// push counter for spawns. Later milestones bolt on quantum
// accounting, STW hooks, and the TinyGo-scheduler replacement.
//
// Design: no_goroutine_kernel_design/02_kernel_thread_runtime.md,
//         no_goroutine_kernel_design/09_incremental_migration_plan.md (M0).

package main

// kschedReadyQueue is a per-CPU FIFO of runnable kernel threads.
// Queue intrusion uses KernelThread.WakeLink (§02); a thread is on
// at most one list at a time.
type kschedReadyQueue struct {
	head, tail *KernelThread
	lock       Spinlock
	_pad       [64 - 24]byte // cache-line pad
}

// kschedQueues is the per-CPU scheduler state. Indexed 0..maxCPUs-1.
var kschedQueues [maxCPUs]kschedReadyQueue

// kschedBootstrap is the anchor KernelThread representing "the
// scheduler loop itself" on each CPU. It is never enqueued; it is
// the sink kschedSwitch writes SavedRSP into when a thread parks,
// and the source it loads from on the next dispatch iteration.
var kschedBootstrap [maxCPUs]KernelThread

// kschedRunning[cpu] is the thread currently executing on cpu. Set
// by kschedLoop before each kschedSwitch-in; reset to the idle
// thread when the queue empties.
var kschedRunning [maxCPUs]*KernelThread

// kschedIdle[cpu] is the idle thread body for cpu. The idle body
// performs sti; hlt; cli in a loop — same shape as the existing
// TinyGo waitForEvents hook (§05 keeps the file).
var kschedIdle [maxCPUs]KernelThread

// kschedInitialized gates kschedInit so repeated calls are safe.
var kschedInitialized uint32

// kschedSpawnRRCounter is the round-robin counter for new-thread
// placement. Racey increment is acceptable; kschedSpawn takes
// kschedQueues[target].lock around the push so the resulting push
// is well-ordered (§02 rule 2).
var kschedSpawnRRCounter uint32

// kschedDefaultQuantum is the per-thread starting quantum in LAPIC
// ticks. M0 reserves the field; preempt accounting lands in M4.
const kschedDefaultQuantum = 10

// kschedSmokeAllDone is set by the M0 smoke-test exit path; when
// kschedLoop observes it, the loop returns back to its caller
// (normal kschedLoop is non-returning). M0-only; removed in M1.
var kschedSmokeAllDone uint32

// ---- Queue ops ----

//go:nosplit
func kschedPushLocked(q *kschedReadyQueue, t *KernelThread) {
	t.WakeLink = nil
	if q.tail == nil {
		q.head = t
	} else {
		q.tail.WakeLink = t
	}
	q.tail = t
}

//go:nosplit
func kschedPopLocked(q *kschedReadyQueue) *KernelThread {
	t := q.head
	if t == nil {
		return nil
	}
	q.head = t.WakeLink
	if q.head == nil {
		q.tail = nil
	}
	t.WakeLink = nil
	return t
}

// kschedPush enqueues t onto cpu's ready queue. Sets t.State and
// t.OwnerCPU. Safe from any context.
//
//go:nosplit
func kschedPush(t *KernelThread, cpu uint32) {
	if cpu >= maxCPUs {
		cpu = 0
	}
	q := &kschedQueues[cpu]
	flags := q.lock.Acquire()
	t.State = uint32(KStateRunnable)
	t.OwnerCPU = cpu
	kschedPushLocked(q, t)
	q.lock.Release(flags)
}

// kschedPop dequeues one thread from cpu's local queue. nil on empty.
//
//go:nosplit
func kschedPop(cpu uint32) *KernelThread {
	if cpu >= maxCPUs {
		return nil
	}
	q := &kschedQueues[cpu]
	flags := q.lock.Acquire()
	t := kschedPopLocked(q)
	q.lock.Release(flags)
	return t
}

// kschedSteal dequeues one thread from `from`'s queue for `to`.
// nil on empty. Caller (idle path) holds no other lock.
//
//go:nosplit
func kschedSteal(from, to uint32) *KernelThread {
	if from >= maxCPUs || from == to {
		return nil
	}
	q := &kschedQueues[from]
	flags := q.lock.Acquire()
	t := kschedPopLocked(q)
	q.lock.Release(flags)
	return t
}

// ---- Init ----

// kschedInit initialises the scheduler substrate. Called once from
// main() on BSP before any kschedSpawn / kschedLoop.
func kschedInit() {
	if kschedInitialized != 0 {
		return
	}
	checkKernelThreadOffset()
	// Zero is the default-initialized queue state; explicit
	// assignment here is defensive and documents the invariant.
	for i := uint32(0); i < maxCPUs; i++ {
		kschedQueues[i].head = nil
		kschedQueues[i].tail = nil
		kschedBootstrap[i].Slot = -1
		kschedIdle[i].Slot = -1
		kschedIdle[i].Name[0] = 'i'
		kschedIdle[i].Name[1] = 'd'
		kschedIdle[i].Name[2] = 'l'
		kschedIdle[i].Name[3] = 'e'
		kschedIdle[i].Stack.Canary = kernelStackCanary
	}
	kschedInitialized = 1
}

// ---- Scheduler loop ----

// kschedLoop drives the local CPU's scheduler: pick a thread, switch
// into it, come back when it parks/exits/is preempted, repeat.
//
// Normally non-returning. During the M0 smoke test it returns when
// kschedSmokeAllDone is observed, so the rest of boot can continue
// under TinyGo. Later milestones remove that early-exit.
//
//go:nosplit
func kschedLoop() {
	cpu := cpuID()
	for {
		if kschedSmokeAllDone != 0 {
			return
		}
		t := kschedPop(cpu)
		if t == nil {
			// Try to steal from a peer.
			for i := uint32(1); i < numCoresOnline; i++ {
				t = kschedSteal((cpu+i)%numCoresOnline, cpu)
				if t != nil {
					break
				}
			}
		}
		if t == nil {
			// No runnable work. In M0 we spin briefly and re-check so
			// the smoke test can make progress while TinyGo is
			// time-sharing with us; later milestones substitute the
			// real idle thread (sti; hlt; cli).
			gooosPause()
			continue
		}
		// Skip exiting threads; reclaim their slot.
		if KState(t.State) == KStateExiting {
			kthreadPoolFree(t)
			continue
		}
		kschedRunning[cpu] = t
		t.State = uint32(KStateRunning)
		t.OwnerCPU = cpu
		t.Quantum = kschedDefaultQuantum
		kschedSwitch(t, &kschedBootstrap[cpu])
		// Return point: t has parked, exited, or been preempted. The
		// thread-side code has already set t.State appropriately.
		kschedRunning[cpu] = nil
	}
}

// kschedLoopOnce drives one iteration of the scheduler: pop a
// runnable kernel thread from this CPU's queue (steal from peers
// if empty), switch into it, return when it parks / yields /
// exits. Returns immediately if there is no runnable thread on
// any CPU.
//
// Called from the TinyGo runtime's waitForEvents hook so kernel
// threads share CPU with the TinyGo scheduler during M1..M3
// co-existence. M4 removes the co-existence path in favour of
// a pure kschedLoop.
//
//export kschedLoopOnce
//go:nosplit
func kschedLoopOnce() {
	if kschedInitialized == 0 {
		return
	}
	cpu := cpuID()
	if cpu >= maxCPUs {
		return
	}
	t := kschedPop(cpu)
	if t == nil {
		for i := uint32(1); i < numCoresOnline; i++ {
			t = kschedSteal((cpu+i)%numCoresOnline, cpu)
			if t != nil {
				break
			}
		}
	}
	if t == nil {
		return
	}
	if KState(t.State) == KStateExiting {
		kthreadPoolFree(t)
		return
	}
	// Hold IF=0 while committing the dispatch so a preempt IPI
	// cannot observe kschedRunning[cpu]=t before kschedSwitch has
	// actually made t the running thread. kschedSwitch's popfq
	// restores IF to whatever t's saved RFLAGS had (= IF=1 for a
	// freshly-spawned thread; same for a previously-parked one as
	// long as it was parked from an IF=1 context).
	flags := readFlags()
	cli()
	kschedRunning[cpu] = t
	t.State = uint32(KStateRunning)
	t.OwnerCPU = cpu
	t.Quantum = kschedDefaultQuantum
	kschedSwitch(t, &kschedBootstrap[cpu])
	// Returned: thread parked / yielded / exited. Symmetric cli
	// while we tear down the dispatch record.
	cli()
	if t.State == uint32(KStateExiting) {
		kthreadPoolFree(t)
	}
	kschedRunning[cpu] = nil
	restoreFlags(flags)
}

// kschedYield voluntarily hands the current CPU to the scheduler
// loop. Callable from a running kernel thread; returns when the
// thread is scheduled again.
//
//go:nosplit
func kschedYield() {
	cpu := cpuID()
	t := kschedRunning[cpu]
	if t == nil {
		return
	}
	// Put self back on the ready queue, then switch to the
	// bootstrap context (= kschedLoop).
	kschedPush(t, cpu)
	kschedSwitch(&kschedBootstrap[cpu], t)
}

// ---- Asm-side linkage ----

// kschedSwitch is implemented in src/kthread_switch.S. Saves
// callee-saved regs + RFLAGS on the current stack, stores RSP into
// old.SavedRSP, loads new.SavedRSP into RSP, restores regs + RFLAGS,
// returns into the new context.
//
//go:linkname kschedSwitch kschedSwitch
//go:nosplit
func kschedSwitch(new_, old *KernelThread)

// kschedEnterAddr returns the address of the asm kschedEnter
// trampoline, used by kschedSpawn to build a new thread's initial
// frame so the first switch-in lands in the trampoline.
//
//go:linkname kschedEnterAddr kschedEnterAddr
//go:nosplit
func kschedEnterAddr() uintptr
