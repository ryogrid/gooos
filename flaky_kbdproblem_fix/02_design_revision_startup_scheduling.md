# Design revision: startup scheduling and preempt stabilization

## Design goals

1. Remove boot-phase ambiguity from preempt routing.
2. Keep preempt behavior deterministic when APICID/AP readiness is transient.
3. Avoid probe-specific behavior contaminating common runtime paths.
4. Maintain compatibility with current deferred AP timer model.

## Core revision: explicit startup/preempt phase machine

Introduce a small global phase state (new file suggested: `src/preempt_phase.go`):

1. `phaseBootInit` — BSP/AP bring-up in progress, no preempt fanout.
2. `phaseSchedReady` — AP schedulers entered; collect/stabilize preempt target set.
3. `phaseOperational` — stable preempt fanout enabled.

Rules:

1. Phase transitions are monotonic (only forward).
2. `handleLAPICTimer()` may call `broadcastPreemptIPI()` only in `phaseOperational`.
3. Probe warmup logic (for test-only gates) must not mutate core dispatch policy.

## Stable target-set strategy

Current send path (`broadcastPreemptIPI`) mixes dynamic checks and fallback broadcast each tick. Replace with two-tier behavior:

1. **Operational target snapshot:** BSP periodically recomputes `preemptTargets[]` from `numCoresOnline` and valid APICIDs.
2. **Send path reads snapshot only:** timer ISR sends to snapshot targets; no ad-hoc per-tick fallback broadcasts in unstable phases.
3. **Fallback policy:** shorthand broadcast only if explicitly enabled by a diagnostic flag, not as default behavior.

Suggested touch points:

1. `src/ipi.go`
   1. add snapshot representation and guarded refresh helper,
   2. revise `broadcastPreemptIPI()` to consume snapshot,
   3. keep `lapicSendIPI` and `lapicBroadcastIPI` primitives unchanged.
2. `src/lapic_timer.go`
   1. gate preempt fanout on phase state,
   2. move probe-only warmup/counter code behind dedicated probe wrapper.
3. `src/smp.go` / `src/main.go`
   1. transition to `phaseSchedReady` when `bspBootDone` is set on BSP,
   2. add AP scheduler-entry accounting (`apSchedEnteredCount`),
   3. transition to `phaseOperational` when `bspBootDone && apSchedEnteredCount >= numCoresOnline-1`.

## AP readiness and APICID latching policy

Keep existing re-latch pattern but make it explicit in readiness checks:

1. AP is **preempt-eligible** only if:
   1. AP online (`cpuIdx < numCoresOnline`),
   2. APICID valid (`cpuIdx==0` or `APICID != 0`),
   3. AP scheduler started.
2. Eligibility is evaluated in snapshot refresh, not in hot ISR loops.

## Safe-point behavior remains in ISR

`handlePreemptIPI()` policy in `src/goroutine_irq.go` remains the safety gate (interrupt/syscall depth, preempt-disable). Revision focuses on making delivery deterministic, not weakening safe-point checks.

## Rejected alternatives

1. **Enable AP LAPIC timers immediately:** rejected for this batch because current code documents unresolved hang risk in AP timer dispatch.
2. **Always shorthand-broadcast preempt IPI:** rejected due low observability and higher risk of interrupt storms in unstable boot windows.
3. **Single-shot “wait N ticks then enable” heuristic only:** rejected as insufficiently causal; use readiness/state predicates instead.
