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

// kschedQueuesRing3 is the per-CPU Ring-3 host ready queue.
// §15_userspace_smp_on_aps.md §3 (M7): Ring-3 hosts
// (KernelThreads with kthreadHostedProc[t.Slot] != nil) are
// enqueued on this sibling tier instead of kschedQueues.
// Service kthreads (timerDispatcher, fsTask, net/tcp services,
// boot probes) keep using kschedQueues per R1+R2.
//
// Indexed identically (cpu 0..maxCPUs-1). Under M6
// (userspaceSMP=false) this tier holds the boot shell at index 0
// and remains empty elsewhere; the BSP combined pump
// (src/elf.go) drives kschedLoopRing3OnlyOnce(0) so the boot
// shell still runs. Under M7 (userspaceSMP=true) APs run
// kschedLoopRing3Only(cpu) and consume from queue[cpu].
var kschedQueuesRing3 [maxCPUs]kschedReadyQueue

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
//
// §14 invariant U4: the counter is unread under uniprocessorKernel
// (kschedSpawn / kschedSpawnRing3Wrapper bypass the round-robin
// block). The variable is reserved for M7 (Ring-3 dispatch on APs)
// when AP-side kthread placement returns; do not delete.
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
// M5-fix-3: when target cpu is remote, send a wake IPI via
// gooosWakeupCPU so the AP leaves its hlt-idle in kschedLoop and
// dispatches the just-pushed thread. Without this, cross-CPU
// wakes from KEvent.Signal -> kschedWake -> kschedPush would
// queue work on the AP but the AP would never see it (it sleeps
// in sti;hlt;cli until the next 100 Hz LAPIC tick — which
// happens to wake it eventually but with up-to-10ms latency,
// breaking timer-driven workloads like sleeptest and kpMarker).
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
	if cpu != cpuID() {
		gooosWakeupCPU(cpu)
	}
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
// §14 invariant U3: under uniprocessorKernel, only BSP runs
// kthreads, so no AP queue ever has work to steal AND BSP never
// needs to steal from itself. Returning nil unconditionally is
// safe; the kschedLoop steal block (gated below) is bypassed.
//
//go:nosplit
func kschedSteal(from, to uint32) *KernelThread {
	if uniprocessorKernel {
		return nil
	}
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
		if t == nil && !uniprocessorKernel {
			// Try to steal from a peer.
			//
			// §14 invariant U3: under uniprocessorKernel only BSP
			// has a populated queue; stealing is wasted work (and
			// kschedSteal would return nil anyway). The block is
			// kept for one-revert M7 restoration.
			for i := uint32(1); i < numCoresOnline; i++ {
				t = kschedSteal((cpu+i)%numCoresOnline, cpu)
				if t != nil {
					break
				}
			}
		}
		if t == nil {
			// M5-fix-3: actually halt the CPU on empty queue. hlt
			// wakes on the next interrupt — either the cross-CPU
			// wake IPI from kschedPush (vector 0xFC) or the AP's
			// own 100 Hz LAPIC timer. gooosPause was a `pause`
			// spin-hint that did NOT halt; under scheduler=none
			// that left APs spinning without polling their queues
			// after a remote kschedPush.
			//
			// Smoke test: kschedSmokeAllDone short-circuit at
			// loop top still fires before this branch on empty,
			// so the smoke harness's kschedLoop returns cleanly.
			sti()
			hlt()
			cli()
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

// ---- Ring-3 tier (§15_userspace_smp_on_aps.md §3, M7) ----

// kschedPushRing3 enqueues a Ring-3 host onto the Ring-3 tier
// of cpu. Mirrors kschedPush but writes kschedQueuesRing3[cpu]
// instead of kschedQueues[cpu]. The cross-CPU wake IPI (vector
// 0xFC) is sent unconditionally on remote push so the target AP
// leaves its hlt-idle in kschedLoopRing3Only.
//
// §15 §2 R5: this is the M7 wake protocol.
//
//go:nosplit
func kschedPushRing3(t *KernelThread, cpu uint32) {
	if cpu >= maxCPUs {
		cpu = 0
	}
	q := &kschedQueuesRing3[cpu]
	flags := q.lock.Acquire()
	t.State = uint32(KStateRunnable)
	t.OwnerCPU = cpu
	kschedPushLocked(q, t)
	q.lock.Release(flags)
	if cpu != cpuID() {
		gooosWakeupCPU(cpu)
	}
}

// kschedPopRing3 dequeues one Ring-3 host from cpu's local
// Ring-3 queue. nil on empty.
//
//go:nosplit
func kschedPopRing3(cpu uint32) *KernelThread {
	if cpu >= maxCPUs {
		return nil
	}
	q := &kschedQueuesRing3[cpu]
	flags := q.lock.Acquire()
	t := kschedPopLocked(q)
	q.lock.Release(flags)
	return t
}

// kschedStealRing3 dequeues one Ring-3 host from `from`'s
// Ring-3 queue for `to`. nil on empty.
//
// §15 §2 R6: AP↔AP stealing only. BSP (cpu 0) is never a steal
// source — the boot shell lives there and must keep its
// foreground-keyboard owner role on BSP. A would-be steal that
// names cpu 0 returns nil.
//
//go:nosplit
func kschedStealRing3(from, to uint32) *KernelThread {
	if from >= maxCPUs || from == to {
		return nil
	}
	if from == 0 {
		return nil // R6: BSP shell never steal-victim
	}
	q := &kschedQueuesRing3[from]
	flags := q.lock.Acquire()
	t := kschedPopLocked(q)
	q.lock.Release(flags)
	return t
}

// kschedLoopRing3Only drives the Ring-3 tier on cpu. Mirrors
// kschedLoop but pops from kschedQueuesRing3 and steals via
// kschedStealRing3. Used by AP entry under userspaceSMP=true.
// Non-returning.
//
//go:nosplit
func kschedLoopRing3Only(cpu uint32) {
	for {
		t := kschedPopRing3(cpu)
		if t == nil {
			// AP↔AP steal (R6: never steal from BSP).
			for i := uint32(1); i < numCoresOnline; i++ {
				src := (cpu + i) % numCoresOnline
				if src == 0 {
					continue
				}
				t = kschedStealRing3(src, cpu)
				if t != nil {
					break
				}
			}
		}
		if t == nil {
			sti()
			hlt()
			cli()
			continue
		}
		if KState(t.State) == KStateExiting {
			kthreadPoolFree(t)
			continue
		}
		kschedRunning[cpu] = t
		t.State = uint32(KStateRunning)
		t.OwnerCPU = cpu
		t.Quantum = kschedDefaultQuantum
		kschedSwitch(t, &kschedBootstrap[cpu])
		kschedRunning[cpu] = nil
	}
}

// kschedLoopRing3OnlyOnce drives one iteration of the Ring-3
// tier on cpu. Returns immediately if no runnable Ring-3 host
// is available (no steal). Used by the BSP combined pump in
// src/elf.go to interleave service-tier (kschedLoopOnce) and
// Ring-3-tier dispatch on BSP without entering a non-returning
// loop.
//
//export kschedLoopRing3OnlyOnce
//go:nosplit
func kschedLoopRing3OnlyOnce(cpu uint32) {
	if kschedInitialized == 0 {
		return
	}
	if cpu >= maxCPUs {
		return
	}
	t := kschedPopRing3(cpu)
	if t == nil {
		return
	}
	if KState(t.State) == KStateExiting {
		kthreadPoolFree(t)
		return
	}
	flags := readFlags()
	cli()
	kschedRunning[cpu] = t
	t.State = uint32(KStateRunning)
	t.OwnerCPU = cpu
	t.Quantum = kschedDefaultQuantum
	kschedSwitch(t, &kschedBootstrap[cpu])
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
// On resume (post-kschedSwitch), kthreadResumeRing3Ctx re-installs
// CR3 + TSS.RSP0 + per-CPU pool slot. This handles the cross-CPU
// case where a kthread parks on one CPU and resumes on another
// (work-stealing or wake-on-different-CPU). For Ring-3-hosting
// kthreads only — non-host kthreads (fsTask etc.) get a no-op.
//
// §15 §3.3 / §16 Step 4 (M7) reviewer-pass BLOCKING-1 fix:
// Route Ring-3 hosts (kthreads with kthreadHostedProc[t.Slot] != nil)
// back to the Ring-3 tier (kschedQueuesRing3) so AP dispatchers
// (kschedLoopRing3Only) can pick them up. Without this, a CPU-bound
// Ring-3 host that is preempted via the LAPIC-timer safe-point
// (src/goroutine_irq.go:150,186 → kschedYield) lands on the
// service-tier kschedQueues[ap], where kschedLoopRing3Only never
// pops from — the kthread is permanently orphaned.
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
	if t.Slot >= 0 && int(t.Slot) < kthreadPoolCap &&
		kthreadHostedProc[t.Slot] != nil {
		kschedPushRing3(t, cpu)
	} else {
		kschedPush(t, cpu)
	}
	kschedSwitch(&kschedBootstrap[cpu], t)
	// Resumed (possibly on a different CPU) — re-install CR3+TSS.
	kthreadResumeRing3Ctx()
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
