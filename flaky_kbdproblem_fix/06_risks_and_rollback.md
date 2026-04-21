# Risks and rollback strategy

## Key risks

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Over-gating preempt fanout delays fairness | Apparent starvation until phase opens | Keep phase transition criteria explicit and low-latency; emit markers for phase timing |
| Target snapshot drift vs real online CPUs | Missed preempt delivery to valid APs | Refresh snapshot periodically and on known topology/APICID events |
| Autorun path changes shell behavior unexpectedly | Regression in normal interactive shell | Strict one-time, probe-gated behavior when autorun file is present; no-op otherwise |
| Foreground ownership changes break background semantics | Shell loses stdin or jobs behavior regresses | Keep `sys_waitpid` no-foreground-transfer invariant; add focused regression checks (`test_shell_background.sh`, `test_ps.sh`) |
| Probe diagnostics pollute timing | False instability under load | Probe instrumentation behind explicit gates and kept minimal in ISR paths |

## Rollback controls

1. Keep new behavior behind explicit compile-time gates where possible.
2. If startup instability increases:
   1. disable new phase-dependent fanout changes first,
   2. preserve existing BSP-only preempt baseline.
3. If shell behavior regresses:
   1. disable autorun probe gate,
   2. revert shell autorun hook while retaining startup stabilization changes,
   3. verify autorun file cleanup semantics (success and error paths),
   4. confirm no autorun file persists after boot with gate disabled.
4. If preempt targeting regresses:
   1. switch back to previous `broadcastPreemptIPI` implementation,
   2. keep only non-functional diagnostics until root cause is isolated.

## Rollback validation

After any rollback step:

1. re-run Tier 0 deterministic gates,
2. run Tier 1 regression subset,
3. confirm system returns to known baseline behavior.
