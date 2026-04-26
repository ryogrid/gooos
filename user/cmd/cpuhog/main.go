// cpuhog — CPU-bound workload for feature 2.3 anti-starvation testing.
//
// Runs a tight compute loop with no syscalls. Bounded at 60 seconds of
// wall time so the test harness does not hang if `&` semantics regress
// (the harness kills QEMU well before then anyway). Each 2-second
// window it prints a heartbeat so the serial log captures evidence
// that the hog is actively running.
//
// Use from the shell as `cpuhog &` — anti-starvation tests spawn a
// foreground `markerprint` alongside to verify the scheduler rotates.

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const maxHeartbeats = 100000 // effectively long-lived for test windows

//go:noinline
func burnStep(v uint64, i int) uint64 {
	// LCG-like update with a per-iteration term to keep the dependency
	// chain opaque to aggressive loop-collapsing optimizations.
	return v*2862933555777941757 + 3037000493 + uint64(i)
}

func main() {
	gooos.Println("cpuhog: started on cpu=" + strconv.Itoa(gooos.GetCpuID()))
	var ticks uint64 = 1
	for beat := 0; beat < maxHeartbeats; beat++ {
		// Inner loop: CPU-bound arithmetic with no syscalls.
		for i := 0; i < 20_000_000; i++ {
			ticks = burnStep(ticks, i)
		}
		if beat%100 == 0 {
			gooos.Println("cpuhog: heartbeat " + strconv.Itoa(beat) +
				" cpu=" + strconv.Itoa(gooos.GetCpuID()) +
				" ticks=" + strconv.FormatUint(ticks, 10))
		}
	}
	gooos.Println("cpuhog: exit")
}
