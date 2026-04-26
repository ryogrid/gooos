// src/kthread_event.go -- KEvent: single-shot edge-triggered event.
//
// Replaces Go `chan struct{}` used as a completion signal. Zero
// value is unsignalled; Signal transitions to signalled (idempotent
// once signalled). Waiters parked before Signal wake; waiters that
// call Wait after Signal return immediately.
//
// Lock-ordering: KEvent.lock sits at rank 14 (above all pre-Route-C
// ranks); callers must not acquire any lower-ranked lock while
// holding it. The only nested call out of the critical section is
// kschedWake(waiter), which acquires kschedQueues[waiter.OwnerCPU].lock
// — documented as rank 15 per §03 (reviewer finding).
//
// Design: no_goroutine_kernel_design/03_sync_primitives.md.

package main

// KEvent is a single-shot event. Not safe for concurrent Reset
// while waiters are parked; callers that need re-armable semantics
// must synchronise Reset externally.
type KEvent struct {
	lock    Spinlock
	flag    uint32
	waiters *KernelThread
}

// Wait blocks until Signal has been called at least once on this
// event. Safe to call from any kernel-thread context; not safe to
// call from ISR context (no scheduler in that case).
//
//go:nosplit
func (e *KEvent) Wait() {
	for {
		flags := e.lock.Acquire()
		if e.flag != 0 {
			e.lock.Release(flags)
			return
		}
		cpu := cpuID()
		me := kschedRunning[cpu]
		if me == nil {
			// Not called from a kernel thread (e.g. TinyGo-goroutine
			// caller during boot before the scheduler has dispatched
			// a kthread on this CPU). Pump kschedLoopOnce so the
			// fsTask kernel thread can make progress on the same
			// CPU; required for -smp 1 where there's no peer AP to
			// steal from.
			e.lock.Release(flags)
			for e.flag == 0 {
				kschedLoopOnce()
				gooosPause()
			}
			return
		}
		// Link self into the waiter list.
		me.WakeLink = e.waiters
		e.waiters = me
		me.State = uint32(KStateParked)
		me.ParkLock = &e.lock
		e.lock.Release(flags)
		// Switch to the bootstrap context. When resumed, re-check
		// the flag under the lock (classic re-check-then-return).
		kschedSwitch(&kschedBootstrap[cpu], me)
		// Resumed (possibly on a different CPU). Re-install
		// CR3+TSS for Ring-3-hosting kthreads (M4.1.b). No-op
		// for fsTask and other non-host kthreads.
		kthreadResumeRing3Ctx()
		// Loop back; on resume me.WakeLink has been cleared by
		// Signal's snapshot-and-wake path.
	}
}

// Signal transitions the event to signalled and wakes every
// currently-parked waiter. Idempotent. Safe from any context
// except the wake-IPI ISR (which could recurse). Callers inside
// Signal must not hold a lower-ranked lock when invoking — take
// lowest-rank lock first, drop it, then Signal.
//
//go:nosplit
func (e *KEvent) Signal() {
	flags := e.lock.Acquire()
	e.flag = 1
	waiters := e.waiters
	e.waiters = nil
	e.lock.Release(flags)
	// Wake outside the lock to honour the §03 release-then-wake
	// rule (avoids a nested kschedQueues lock acquisition while
	// holding e.lock).
	for w := waiters; w != nil; {
		next := w.WakeLink
		w.WakeLink = nil
		w.ParkLock = nil
		kschedWake(w)
		w = next
	}
}

// Reset clears the signalled state. Undefined behaviour if any
// waiter is currently parked on the event. Callers that need
// re-armable single-shot semantics (e.g. M1 smoke `kschedSmokeAllDone`)
// are responsible for enforcing that invariant.
//
//go:nosplit
func (e *KEvent) Reset() {
	flags := e.lock.Acquire()
	e.flag = 0
	e.lock.Release(flags)
}

// Fired reports whether the event is currently signalled. Racey by
// design (diagnostic). Callers that must act on the result should
// call Wait or protect with external synchronisation.
//
//go:nosplit
func (e *KEvent) Fired() bool {
	return e.flag != 0
}
