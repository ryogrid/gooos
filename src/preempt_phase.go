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

func maybeEnterOperational() {
	if preemptPhase < preemptPhaseSchedReady {
		return
	}
	// Keep startup gating simple and deterministic: once BSP boot is
	// complete, promote to operational preempt fanout.
	if bspBootDone != 0 {
		preemptPhase = preemptPhaseOperational
	}
}

// preemptPhaseAdvance monotonically advances preempt phase.
func preemptPhaseAdvance(next uint32) {
	if next > preemptPhase {
		preemptPhase = next
	}
	maybeEnterOperational()
}

// markAPSchedulerEntered records AP scheduler handoff.
func markAPSchedulerEntered() {
	apSchedEnteredCount++
	maybeEnterOperational()
}

//go:nosplit
func preemptPhaseIsOperational() bool {
	return preemptPhase >= preemptPhaseOperational
}
