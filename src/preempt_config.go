// src/preempt_config.go — feature 2.1 preempt-enable compile-time gate.
//
// When true, `handleLAPICTimer` broadcasts a preempt IPI (vector 0xFB)
// across online cores on every 100 Hz BSP tick. Each AP's preempt ISR
// checks safe-point conditions and, if safe, calls runtime.Gosched()
// to hand the CPU to the next runnable task — giving a hostile
// compute-bound goroutine a 10 ms quantum.
//
// Rollback: flip this const to false, rebuild, re-run. The IPI is no
// longer broadcast; the ISR is still registered but never fires; the
// PreemptDisable counter continues to increment/decrement but has no
// consumer. System degrades gracefully to the pre-preempt baseline
// (IPI-wake on cooperative yield points only, via schedulerWake).
//
// Design: impldoc/preempt_kernel_goroutines.md §2.5 (Rollback flag).

package main

const preemptEnabled = false

// runPreemptProbe gates the kpHog + kpMarker goroutines spawned in
// src/main.go. Off in release builds; flip to true alongside
// preemptEnabled when running scripts/test_preempt_kernel.sh.
const runPreemptProbe = false
