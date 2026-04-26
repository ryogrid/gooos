# TODO - SMP keyboard hang implementation (2026-04-21)

## Plan

- [x] 1. Confirm APICID/wakeup-IPI branch with code-level checks
- [x] 2. Patch AP APICID initialization timing after LAPIC enable
- [x] 3. Harden wakeup IPI target/self guards in src/ipi.go
- [x] 4. Build and run targeted SMP regressions
- [x] 5. Write review notes and remaining risks

## Review

- Build: `make build` PASS
- PASS: `scripts/test_smp_basic.sh`
- PASS: `scripts/test_smp_shell_distribution.sh`
- PASS: `scripts/test_ps.sh`
- PASS: `scripts/test_shell_background.sh`
- PASS: `scripts/test_preempt_kernel.sh`
- PASS: `scripts/test_preempt_user.sh`
- PASS: `scripts/test_net.sh`
- FAIL (2/2): `scripts/test_smp_shell_preempt.sh` showed `markers_observed=0`; serial tail indicates shell prompt appears but sendkey-driven command injection did not run before timeout (known flaky path under `-smp 4`).

## Continuation Notes (2026-04-21 follow-up)

- `scripts/test_smp_shell_preempt.sh` was refactored to bypass HMP sendkey and auto-launch `cpuhog.elf` + `markerprint.elf` by flipping `runSMPShellPreemptProbe` in `src/preempt_config.go` for the test build.
- Added `runSMPShellPreemptProbe` gate in `src/preempt_config.go` and boot-path hook in `src/main.go`.
- Launch sequencing was improved (AP-side launcher + markerprint-first ordering), reducing pure harness-input flakes.
- Observed behavior remains inconsistent with 2.3(b) PASS criteria:
	- `scripts/test_preempt_kernel.sh` still PASS (`markers_observed=7`), so kernel preempt path is alive.
	- `scripts/test_smp_shell_preempt.sh` remains often FAIL (`markers_observed=1`) even when `cpuhog` and `markerprint` start on the same AP.
	- Serial evidence repeatedly shows `markerprint` prints only `marker 0` then stalls while `cpuhog` continues until exit.
- Diagnostic code added:
	- Ring3 preempt fallback in `handlePreemptIPI` when `maybeDeliverSignal` does not deliver.
	- `maybeDeliverSignal` now returns `bool` to indicate frame rewrite happened.
	- `broadcastPreemptIPI` now allows BSP APICID=0 as a valid target and no longer self-skips by CPU index.
	- `handlePreemptIPI` includes first-seen marker instrumentation (`MARKER: M8 preempt:first-cpuN`) for delivery tracing.
- Current conclusion:
	- The original `sendkey` flake was real and is now bypassed.
	- A separate functional/design mismatch remains for 2.3(b): current Ring3 process behavior under preempt IPI does not reliably provide cross-process forward progress for `cpuhog` vs `markerprint` in this harness shape.

## Continuation Notes (2026-04-21 deeper follow-up)

- Added stronger preempt diagnostics:
	- `MARKER: M9 preempt:bcast-first`, `MARKER: M18 preempt:bcast-10` in LAPIC timer path.
	- `PRESTAT cpu=...` counters in `handlePreemptIPI` (calls/ring3/sig/nosig/skip_task0/yield).
	- `APIDSTAT cpu=... apicid=...` snapshot at `processExit` for probe runs.
- Reordered initialization so `ipiPreemptVector`/`ipiWakeupVector` handlers are registered before LAPIC timer starts (avoids early unhandled 0xFB windows).
- Preempt send-path now uses:
	- broadcast preempt IPI to APs via shorthand (exclude self), plus
	- explicit BSP self-IPI request (`lapicSendSelfIPI`) each tick.
- Empirical results from repeated `scripts/test_smp_shell_preempt.sh` runs:
	- `M9` and `M18` appear => LAPIC timer preempt block continues past first tick.
	- `M8/M15` appear only for `cpu0`; `PRESTAT` shows activity on `cpu0` only.
	- `PRESTAT ring3=0` on active runs => preempt handler is firing in Ring0 contexts, not while interrupted context is Ring3.
	- `APIDSTAT` and probe-time `preempt_probe: apicid cpu=N id=0` remain zero for all CPUs.
	- `markerprint` still stalls at `marker 0` (or occasionally `0,0`) under probe.
- Additional workload stabilization:
	- `user/cmd/cpuhog/main.go` changed to a noinline burn-step loop to prevent loop-collapse optimization and reduce premature process exit in fast environments.
	- `runSMPShellPreemptProbe` launcher now waits for AP execution with a timeout fallback and short post-boot delay before `elfSpawn`, reducing early startup PF noise.
- Latest harness snapshot after launcher stabilization:
	- `scripts/test_smp_shell_preempt.sh` still FAIL (`markers_observed=1`).
	- `markerprint` prints `marker 0` then stalls while `cpuhog` continues.
	- `M8/M15` still only observed for `cpu0`; latest branch marker hit is `M10 preempt:skip-intdepth-cpu0`.
	- APICID probe snapshot still reports `id=0` for cpu0..cpu3.
- Outstanding blockers:
	- APIC ID latching still effectively unusable (`APICID=0` for all CPUs in observed runs).
	- AP-target preempt delivery is still not evidenced; only BSP self-preempt path is visible.
	- Intermittent probe instability remains (occasional user PF at startup under concurrent auto-launch).

## Chronological Update (latest continuation)

Scope note:
- This section is the authoritative detailed timeline for the latest continuation phase.
- The two earlier "Continuation Notes" blocks remain as coarse snapshots; this 9-step log resolves their sequence and causal links.
- The current implementation history handled in this session is equal to the branch delta `smp-take5..smp-take5-cordex`.
- Commit sequence in that delta is:
  - `604be0d` smp preempt: refactor shell harness and add delivery diagnostics
  - `252a96b` smp: snapshot preempt/stack investigation changes
- Step-to-commit mapping:
	- `604be0d`: baseline launcher/diagnostic refactor that started this continuation.
	- Steps 1-8 below: iterative edits after `604be0d`.
	- `252a96b` contains the surviving end-state of those edits (files: `src/elf.go`, `src/process.go`, `src/percpu.go`, `src/smp.go`, `src/main.go`, `src/ipi.go`, `src/lapic_timer.go`, `src/goroutine_irq.go`).
	- Failed variants described in the steps (for example timer-ISR direct-yield experiments) were rolled back before `252a96b` and are documented for decision traceability only.
	- Step 9: explicit checkpoint creation (`252a96b`).

1. Change: Added APIC ID retry-latch in `percpuLatchAPICIDCurrent` and post-`bspBootDone` re-latch in `apEntry`.
Goal: Resolve repeated `APICID=0` snapshots that prevented reliable targeted preempt IPI delivery.
Result: APIC IDs were non-zero in some runs, but remained unstable across repeated boots.
Next action: Move 2.3 launcher and IPI delivery logic to tolerate transient APIC ID states.

2. Change: Reworked 2.3 launcher from a detached wait loop into one-shot launch control in `smpBasicProbe` guarded by `smpShellProbeLaunched`.
Goal: Avoid duplicate launches and reduce startup race between shell setup and probe process spawn.
Result: `cpuhog.elf` and `markerprint.elf` launch became more deterministic.
Relation to previous step: This implements Step 1 next action to make launch flow tolerant to transient APIC ID states.
Next action: Add clearer preempt-path diagnostics so post-launch stalls can be separated from launch failure.

3. Change: Added probe diagnostics in timer/preempt paths (`M9`, `M18`, `M19`, `M20`, `PRESTAT`) and delivery markers in preempt ISR.
Goal: Distinguish launch failures from preempt-delivery failures after Step 2 launch stabilization.
Result: Logs showed launch often succeeded, but marker progression still stalled at `marker 0` and preempt visibility was noisy/inconsistent.
Next action: Iterate on IPI routing policy and timer-side preempt behavior.

4. Change: Switched preempt broadcast policy between shorthand broadcast and per-APIC targeted send (including probe-specific paths).
Goal: Confirm whether AP-targeted delivery improves AP-side preempt evidence.
Result: Evidence shifted between runs (sometimes AP-side markers appeared, sometimes only BSP), and stability degraded under some variants.
Next action: keep regression checks (`test_preempt_kernel.sh`) after each IPI-path change.

5. Change (parallel track to Steps 3-4): Investigated startup PF near user stack boundary (`0x7FFF2000` then `0x7FFF3000`) and expanded stack mapping policy.
Goal: Remove boundary-adjacent early user faults observed in `markerprint` startup.
Result: While Step 3 diagnostics were running, serial logs repeatedly exposed startup PF around `0x7FFF[23]000`; boot-shell stack and child stack were then moved from 2 pages to 4 mapped pages with effective top at `userStackBase + 3*pageSize - 8`.
Next action: Verify consistency in both boot and spawned process paths and retest 2.1/2.3 harnesses.

6. Change: Temporarily introduced aggressive timer-side preempt behavior (timer-ISR direct yield experiment and stronger local preempt pressure variants).
Goal: Force stronger preempt pressure on CPU-local hogging paths.
Result: Observed regressions including `blocked inside interrupt` panic in some runs.
Next action: Roll back panic-inducing variants and re-constrain timer preempt driving.

7. Diagnostic cleanup: Reduced ISR serial noise by removing first-seen/skip marker printing from wake and preempt handlers while keeping counters/flags.
Goal: Reduce serial interleaving corruption and avoid amplifying ISR-path overhead during diagnosis.
Result: Log readability improved, but harness outcomes still fluctuated and 2.3 remained failing.
Next action: Focus on functional preempt routing correctness rather than marker volume.

8. Change: Rolled back/contained Step 6 aggression by restricting timer-driven preempt fanout to BSP path (`idx == 0`) and removing BSP self-preempt send in latest attempt.
Goal: Prevent cross-CPU timer-driven IPI storms and interrupt-context scheduling cascades.
Result: Some instability modes decreased, but 2.1 and 2.3 both still exhibited non-deterministic FAIL runs.
Next action: stop ad-hoc changes and checkpoint current state for explicit re-planning.

9. Change: Created checkpoint commit `252a96b` with current code state before further refactor.
Goal: Freeze investigation state so next phase can start from a reproducible baseline.
Result: 8 files snapshot committed (`src/elf.go`, `src/process.go`, `src/percpu.go`, `src/smp.go`, `src/main.go`, `src/ipi.go`, `src/lapic_timer.go`, `src/goroutine_irq.go`).
Next action: perform structured redesign planning for 2.1 stability + 2.3 pass criteria, then implement in smaller verified increments.

Current status summary:
- 2.1 (`scripts/test_preempt_kernel.sh`) is flapping (PASS and FAIL both observed in close succession).
- 2.3 (`scripts/test_smp_shell_preempt.sh`) remains failing (typically 0 or 1 marker observed).
- Stack-boundary PF mitigation is in place in both boot and child stack setup paths, but overall preempt behavior still requires architectural cleanup.

## Plan - current_impl_0421_night docs (2026-04-21)

- [x] 1. Extract subsystem facts from current code (boot/irq/smp/scheduler/process/memory/fs/net/userland/tests)
- [x] 2. Create `current_impl_0421_night/` and write multi-file English Markdown doc set
- [x] 3. Ensure generated docs are self-contained (no references to external docs outside allowed scope)
- [x] 4. Run reviewer subagent against code and docs, collect findings by severity
- [x] 5. Fix reviewer findings (High/Medium) and run reviewer again
- [x] 6. Record final status summary (created files + review loop outcome)
