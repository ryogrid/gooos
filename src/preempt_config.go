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

// runGoprobeTest writes a one-shot shell autorun script at boot
// (`.autorun.sh`) so userspace `goprobe` validation can execute through the
// real shell parser/exec/wait path without HMP sendkey injection.
const runGoprobeTest = false

// runSleeputestTest writes a one-shot shell autorun script at boot
// (`.autorun.sh`) so userspace `sleeptest` validation can execute through the
// real shell parser/exec/wait path without HMP sendkey injection.
const runSleeputestTest = false

// runYieldtestTest writes a one-shot shell autorun script at boot
// (`.autorun.sh`) so userspace `yieldtest` validation can execute through the
// real shell parser/exec/wait path without HMP sendkey injection.
const runYieldtestTest = false

// runSleepAudit enables per-CPU push/pop counters + periodic
// sleepAuditDump() in netDiag, used by
// current_impl_2026_04_24/fix_plan_deferred_1_5/03_sleep_cross_cpu_channel_wakeup_audit.md
// to isolate the residual Sleep-3 flake. OFF by default; the
// audit harness scripts/test_sleeptest_longrun.sh flips it true
// via sed + rebuild, same pattern as the other autorun gates.
const runSleepAudit = false

// runKthreadSmoke gates the M0 kernel-thread smoke test
// (src/kthread_smoke.go). When true, main() briefly enters the
// gooos kernel-thread scheduler after init, runs two demo threads
// that round-robin print A/B, then returns to the normal TinyGo
// boot path. scripts/test_kthread_smoke.sh flips this via sed +
// rebuild.
const runKthreadSmoke = false

// runMinimalKthreads, when true, skips spawning every non-essential
// kthread so we can bisect the post-Route-C `make run-smp` keyboard
// race (see no_goroutine_kernel_design/12_implementation_notes.md
// § Open issues + risks). With this flag set, only these kthreads
// are spawned at boot:
//   - timerDispatcher       (afterticks.go — required for kschedTimedPark)
//   - fsTask                (CPU-0 pinned — required for in-mem FS)
//   - ring3WrapperKT        (the boot shell on CPU 0)
//   - smpBasicProbe         (gated by runSMPBasicProbe; orthogonal)
//   - kpHog/kpMarker        (gated by runPreemptProbe; orthogonal)
// Skipped (gated below by `if !runMinimalKthreads { ... }`):
//   - netRxLoop             (e1000 RX dispatch)
//   - udpEchoServer         (UDP port 7 echo)
//   - tcpRTOScanner         (TCP retransmit scanner)
//   - tcpEchoServer         (TCP port 8080 echo)
//   - netDiagLoop           (periodic netDiag dump)
// With this set true the requested workload still works:
// keyboard input (PS/2 IRQ1 + ring), serial output (COM1),
// in-memory FS (fsTask), GC (TinyGo runtime), smpprobe (gated
// elsewhere), `ls` (uses FS only), and multicore preemption
// (LAPIC timer + handlePreemptIPI are independent of these
// kthreads). Networking, including ICMP and TCP/UDP echo
// servers, is silently disabled.
//
// If the SMP keyboard race disappears with this flag set, the
// race is in one of the disabled net kthreads' interaction with
// the scheduler. If the race persists, it is in the timerDispatcher
// / fsTask / ring3Wrapper / preempt-IPI core loop.
const runMinimalKthreads = true
