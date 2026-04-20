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

const maxHeartbeats = 30 // 30 × 2 s = 60 s hard cap

func main() {
	gooos.Println("cpuhog: started on cpu=" + strconv.Itoa(gooos.GetCpuID()))
	// A counter-driven loop is sufficient; TinyGo will NOT optimize
	// it away because the counter is consumed by the heartbeat print.
	var ticks uint64
	for beat := 0; beat < maxHeartbeats; beat++ {
		// Inner loop: ~200M iters / beat (tuned so we emit ~every 2 s
		// on QEMU TCG). No syscall inside the inner loop.
		for i := 0; i < 200_000_000; i++ {
			ticks++
		}
		gooos.Println("cpuhog: heartbeat " + strconv.Itoa(beat) +
			" cpu=" + strconv.Itoa(gooos.GetCpuID()) +
			" ticks=" + strconv.FormatUint(ticks, 10))
	}
	gooos.Println("cpuhog: exit")
}
