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

const preemptEnabled = true

// runPreemptProbe gates the kpHog + kpMarker goroutines spawned in
// src/main.go. OFF by default because kpHog monopolizes BSP and
// makes the standard regression harnesses (test_net.sh, test_ps.sh,
// etc.) fail. scripts/test_preempt_kernel.sh flips this to true via
// sed before building, runs the harness, and reverts the flip.
const runPreemptProbe = false

// runUserPreemptProbe gates auto-loading of userpreempt.elf from
// bspBootDone — bypasses the shell so scripts/test_preempt_user.sh
// doesn't need HMP sendkey (which is flaky under -smp > 1). Flipped
// by that harness via the same sed pattern as runPreemptProbe.
const runUserPreemptProbe = false

// runSMPShellPreemptProbe gates auto-loading of cpuhog.elf and
// markerprint.elf from bspBootDone. This bypasses shell-driving via
// HMP sendkey, which is flaky under -smp > 1 and can cause false
// negatives in scripts/test_smp_shell_preempt.sh.
const runSMPShellPreemptProbe = false

// runSMPBasicProbe gates the boot-time kernel goroutine distribution probe.
// Off by default so normal SMP boots do not flood serial output or perturb
// early shell/input bring-up. SMP distribution harnesses temporarily flip it.
const runSMPBasicProbe = false

// runSMPProbeShellTest writes a one-shot shell autorun script at boot
// (`.autorun.sh`) so SMP `smpprobe` validation can execute through the
// real shell parser/exec/wait path without HMP sendkey injection.
const runSMPProbeShellTest = false
