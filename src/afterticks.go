// src/afterticks.go — gooos-local replacement for time.After.
//
// The TinyGo `time` package does not link in the gooos kernel
// build because reflect.Value.Complex requires SSE registers we
// keep disabled. afterTicks gives us the same "channel that
// becomes readable after N PIT ticks" primitive without the
// time-package dependency.
//
// Each tick is 10 ms (PIT runs at 100 Hz; see src/pit.go).
//
// The spawned goroutine yields via runtime.Gosched between
// pitTicks checks. We deliberately do NOT call runtime.sleepTicks
// here — the gooos-patched sleepTicks is a busy sti/hlt/cli loop
// (it's the scheduler's idle path, not a parking primitive), so
// calling it from a goroutine body would hold the CPU and
// deadlock any cooperative-yield consumer.
//
// See impldoc/deferred_hygiene.md §5.

package main

import "runtime"

// afterTicksCalls counts every invocation of afterTicks. Plain
// uint64 (no lock) — single-writer-per-goroutine racey increment
// is acceptable for a diagnostic counter; the order-of-magnitude
// signal is what matters. netDiag reads it to correlate
// goroutine-spawn churn with the late-timing RX stall
// (tcp_problem_review2/summary.md).
var afterTicksCalls uint64

// afterTicks returns a channel that becomes readable after `d`
// PIT ticks (10 ms each). Replacement for time.After.
func afterTicks(d uint64) <-chan struct{} {
	afterTicksCalls++
	ch := make(chan struct{}, 1)
	go func() {
		deadline := pitTicks + d
		for pitTicks < deadline {
			runtime.Gosched()
		}
		ch <- struct{}{}
	}()
	return ch
}
