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

func maybeEnterOperationalLocked() {
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
	maybeEnterOperationalLocked()
	preemptPhaseLock.Release(flags)
}

// markAPSchedulerEntered records AP scheduler handoff.
func markAPSchedulerEntered() {
	flags := preemptPhaseLock.Acquire()
	apSchedEnteredCount++
	maybeEnterOperationalLocked()
	preemptPhaseLock.Release(flags)
}

//go:nosplit
func preemptPhaseIsOperational() bool {
	return preemptPhase >= preemptPhaseOperational
}

