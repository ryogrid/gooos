// src/ring3_pool.go — fixed-size pool of kernel stacks for Ring-3
// processes.
//
// Each Ring-3 process runs on a goroutine spawned by ring3Wrapper.
// Without the pool, processExit parks the goroutine via taskPause()
// and the TinyGo runtime never reclaims its ~8 KiB stack. Long
// shell sessions exhaust the 4 MiB heap.
//
// The pool gives each ring3Wrapper an 8 KiB kernel stack drawn
// from the page allocator, returned to the pool on processExit.
// The hosting goroutine still parks (the TinyGo runtime gives us
// no goroutine-reap primitive in scheduler=tasks), but its own
// goroutine stack stays minimal because the heavy lifting runs on
// the pool stack via TSS.RSP0.
//
// See impldoc/deferred_stack_reclaim.md.

package main

const maxRing3Procs = 32

type ring3StackSlot struct {
	base uintptr // page-aligned base of the 2-page kernel stack
}

var (
	ring3StackPool [maxRing3Procs]ring3StackSlot

	// §M6.fix-1: ring3StackPoolCh was `chan int` (cap=maxRing3Procs).
	// Under scheduler=none the chan send in ring3StackRelease panics
	// with "scheduler is disabled" (TinyGo chansend path calls
	// task.Pause). Replaced with a spinlock-protected free bitmap
	// mirroring the kthreadPool pattern (src/kthread_pool.go). The
	// chan declaration is left as a comment for revert ergonomics:
	//   ring3StackPoolCh chan int
	ring3StackInUse   [maxRing3Procs]uint32
	ring3StackPoolLk  Spinlock
	ring3StackPoolHnt uint32 // round-robin scan hint

	// procByPoolSlot is the inverse of Process.poolIdx — indexed by
	// ring3 pool slot, pointing at the Process that owns it. Used by
	// feature 2.2's maybeSignalUserPreempt ISR path: direct array
	// lookup avoids range-over-map (which is not nosplit-safe).
	// Populated in process.go ring3Wrapper; cleared on processExit.
	procByPoolSlot [maxRing3Procs]*Process
)

// setProcByPoolSlot records proc as the owner of ring3 pool slot idx.
//
//go:nosplit
func setProcByPoolSlot(idx int, proc *Process) {
	if idx >= 0 && idx < maxRing3Procs {
		procByPoolSlot[idx] = proc
	}
}

// clearProcByPoolSlot clears the slot-to-Process binding. Called from
// processExit before releasing the pool slot.
//
//go:nosplit
func clearProcByPoolSlot(idx int) {
	if idx >= 0 && idx < maxRing3Procs {
		procByPoolSlot[idx] = nil
	}
}

// ring3StackPoolInit allocates every slot's kernel stack. Called
// once after vmInit() during boot. §M6.fix-1: free-bitmap variant
// (no chan); every slot is implicitly free at boot because
// ring3StackInUse is BSS-zero.
func ring3StackPoolInit() {
	for i := range ring3StackPool {
		ring3StackPool[i].base = allocPagesContig(2)
	}
}

// ring3StackAcquire returns the slot index and the stack-top virtual
// address (high end of the 2-page allocation, suitable for TSS.RSP0).
// §M6.fix-1: scan the free bitmap under ring3StackPoolLk instead of
// the legacy `<-ring3StackPoolCh`. Returns (-1, 0) when the pool is
// exhausted; callers (only elfSpawn) treat that as a fatal user-side
// error path. Pre-§M6.fix-1 the chan recv would block; under M6 we
// surface exhaustion synchronously because chan recv from kthread
// context is not safe under scheduler=none.
func ring3StackAcquire() (int, uintptr) {
	flags := ring3StackPoolLk.Acquire()
	defer ring3StackPoolLk.Release(flags)
	start := ring3StackPoolHnt
	for i := uint32(0); i < maxRing3Procs; i++ {
		idx := (start + i) % maxRing3Procs
		if ring3StackInUse[idx] == 0 {
			ring3StackInUse[idx] = 1
			ring3StackPoolHnt = (idx + 1) % maxRing3Procs
			return int(idx), ring3StackPool[idx].base + 2*pageSize
		}
	}
	return -1, 0
}

// ring3StackRelease returns a slot to the pool. Caller is
// responsible for ensuring the goroutine that was using the stack
// no longer references it. §M6.fix-1: clear the in-use bit instead
// of `ring3StackPoolCh <- idx`. The chan send is the M6 root cause:
// under scheduler=none TinyGo's chansend path calls task.Pause and
// panics with "scheduler is disabled".
func ring3StackRelease(idx int) {
	if idx < 0 || idx >= maxRing3Procs {
		return
	}
	flags := ring3StackPoolLk.Acquire()
	ring3StackInUse[idx] = 0
	if uint32(idx) < ring3StackPoolHnt {
		ring3StackPoolHnt = uint32(idx)
	}
	ring3StackPoolLk.Release(flags)
}
