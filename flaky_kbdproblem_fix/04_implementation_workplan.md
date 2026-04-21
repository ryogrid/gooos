# Implementation work plan (file/symbol level)

## Workstream A — startup/preempt phase control

### A1. Add phase-state primitives

Touch points:

1. `src/preempt_phase.go` (new)
   1. phase constants,
   2. getter/setter with monotonic transition rule,
   3. optional probe marker helper.

Acceptance:

1. No call site can regress phase state.

### A2. Wire phase transitions in boot flow

Touch points:

1. `src/main.go`
   1. set `phaseSchedReady` after BSP boot completion + AP scheduler readiness criteria.
2. `src/smp.go`
   1. add `apSchedEnteredCount` update at AP scheduler handoff,
   2. mark AP scheduler-entered readiness in `apEntry`,
   3. transition to `phaseOperational` when `bspBootDone && apSchedEnteredCount >= numCoresOnline-1`.

Acceptance:

1. Serial markers show deterministic order: boot init -> sched ready -> operational.

### A3. Gate timer-driven preempt fanout by phase

Touch points:

1. `src/lapic_timer.go`
   1. preempt fanout only in `phaseOperational`,
   2. isolate probe counters/warmup from common policy.

Acceptance:

1. No preempt fanout during unstable phases.

## Workstream B — deterministic preempt targeting

### B1. Implement stable target snapshot

Touch points:

1. `src/ipi.go`
   1. target snapshot data,
   2. periodic refresh helper based on AP online/APICID validity/readiness,
   3. `broadcastPreemptIPI()` consumes snapshot.

Acceptance:

1. No ad-hoc fallback broadcast in unstable path by default.

### B2. Keep safe-point policy unchanged

Touch points:

1. `src/goroutine_irq.go` (minimal/no semantic change unless diagnostics needed).

Acceptance:

1. Existing safe-point gates preserved.

## Workstream C — `smpprobe` through real shell path

### C1. Add shell autorun command source for test mode

Touch points:

1. `src/preempt_config.go`
   1. add dedicated gate (for example `runSMPProbeShellTest`).
2. `src/main.go`
   1. create autorun file content when gate is on.
3. `user/cmd/sh/main.go`
   1. `runAutorunIfPresent()` before prompt loop,
   2. parse and execute each command line via existing functions.

Acceptance:

1. Probe run executes `smpprobe` without sendkey dependency.

### C2. Shell continuity hardening (if required by evidence)

Touch points:

1. `src/process.go`
   1. foreground restore behavior in `processWait`,
   2. probe-mode ownership diagnostics,
   3. verify foreground owner PID before/after autorun command execution.

Acceptance:

1. Prompt remains interactive and follow-up command executes after `smpprobe`.

## Workstream D — harness and regressions

### D1. Add deterministic SMP shell-`smpprobe` harness

Touch points:

1. `scripts/test_smp_shell_smpprobe.sh` (new)
   1. flips dedicated probe gate,
   2. boots `-smp 4`,
   3. checks `smpprobe` start/worker/done and post-command shell marker.

### D2. Keep existing suites green

Touch points:

1. Existing scripts:
   1. `scripts/test_smp_basic.sh`
   2. `scripts/test_smp_shell_distribution.sh`
   3. `scripts/test_smp_shell_preempt.sh`
   4. `scripts/test_ps.sh`
   5. `scripts/test_shell_background.sh`
   6. `scripts/test_preempt_kernel.sh`
   7. `scripts/test_preempt_user.sh`

Acceptance:

1. New deterministic harness passes.
2. Existing regression subset shows no new failures caused by this change set.

## Dependency order

1. A1 -> A2 -> A3
2. B1 -> B2
3. C1 -> C2
4. D1 after C1
5. D2 after A/B/C integration
