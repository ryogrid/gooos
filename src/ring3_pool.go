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
	ring3StackPool   [maxRing3Procs]ring3StackSlot
	ring3StackPoolCh chan int // free slot indices

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

// ring3StackPoolInit allocates every slot's kernel stack and seeds
// the free channel. Called once after vmInit() during boot.
func ring3StackPoolInit() {
	if ring3StackPoolCh == nil {
		ring3StackPoolCh = make(chan int, maxRing3Procs)
	}
	for i := range ring3StackPool {
		ring3StackPool[i].base = allocPagesContig(2)
		ring3StackPoolCh <- i
	}
}

// ring3StackAcquire blocks until a slot is free, then returns the
// slot index and the stack-top virtual address (high end of the
// 2-page allocation, suitable for TSS.RSP0).
func ring3StackAcquire() (int, uintptr) {
	idx := <-ring3StackPoolCh
	return idx, ring3StackPool[idx].base + 2*pageSize
}

// ring3StackRelease returns a slot to the pool. Caller is
// responsible for ensuring the goroutine that was using the stack
// no longer references it.
func ring3StackRelease(idx int) {
	ring3StackPoolCh <- idx
}
