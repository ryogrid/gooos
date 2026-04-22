// src/preempt_phase.go -- startup-phase gating for timer preempt fanout.
//
// During early SMP bring-up, AP scheduler readiness and APIC-ID latching
// can be transient. This file provides a monotonic phase state so BSP
// timer preempt fanout starts only after startup reaches a stable point.

package main

const (
	preemptPhaseBootInit uint32 = iota
	preemptPhaseSchedReady
	preemptPhaseOperational
)

var preemptPhase uint32 = preemptPhaseBootInit

// apSchedEnteredCount counts APs that reached scheduler handoff.
var apSchedEnteredCount uint32

var preemptPhaseLock Spinlock

func maybeEnterOperational() {
	if preemptPhase < preemptPhaseSchedReady {
		return
	}
	needed := uint32(0)
	if numCoresOnline > 0 {
		needed = numCoresOnline - 1 // AP count (exclude BSP)
	}
	if bspBootDone != 0 && apSchedEnteredCount >= needed {
		preemptPhase = preemptPhaseOperational
	}
}

// preemptPhaseAdvance monotonically advances preempt phase.
func preemptPhaseAdvance(next uint32) {
	flags := preemptPhaseLock.Acquire()
	if next > preemptPhase {
		preemptPhase = next
	}
	maybeEnterOperational()
	preemptPhaseLock.Release(flags)
}

// markAPSchedulerEntered records AP scheduler handoff.
func markAPSchedulerEntered() {
	flags := preemptPhaseLock.Acquire()
	apSchedEnteredCount++
	maybeEnterOperational()
	preemptPhaseLock.Release(flags)
}

//go:nosplit
func preemptPhaseIsOperational() bool {
	// Interrupt-path gate check: stay lock-free so LAPIC timer ISR
	// never spins on a lock that could be held by interrupted code.
	// preemptPhase is monotonic (BootInit -> SchedReady -> Operational),
	// so a stale read can only delay enablement by at most one tick.
	return preemptPhase >= preemptPhaseOperational
}
