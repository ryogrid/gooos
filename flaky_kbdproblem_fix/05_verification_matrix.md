# Verification matrix

## Primary acceptance criteria

1. Startup scheduling behavior is stable across repeated SMP boots.
2. `smpprobe` runs through shell command path and completes.
3. Shell remains interactive after `smpprobe`.

## Verification tiers

### Tier 0 — deterministic primary gates (required)

1. **SMP startup stability loop** (new/updated harness):
   1. boot `-smp 4` repeatedly (minimum 10 runs),
   2. require successful reach to shell-ready markers each run,
   3. require no preempt-phase regression marker.
2. **Deterministic shell `smpprobe` harness**:
   1. enable dedicated autorun probe gate,
   2. verify markers:
      1. `smpprobe: spawning`
      2. `worker-*: cpuID=*` (>= 1 line)
      3. `smpprobe: done`
      4. post-command shell marker (for example `POST_SMPPROBE_OK`)
3. **Post-command shell liveness check**:
   1. verify shell regained foreground ownership (`foregroundProc == shell process`) in probe diagnostics,
   2. execute one follow-up command in same session (for example `echo AFTER`),
   3. verify output marker `AFTER` and no stdin EOF symptom for shell reads.

### Tier 1 — existing regression subset (required)

Run and keep behavior compatible:

1. `scripts/test_smp_basic.sh`
2. `scripts/test_smp_shell_distribution.sh`
3. `scripts/test_smp_shell_preempt.sh`
4. `scripts/test_preempt_kernel.sh`
5. `scripts/test_preempt_user.sh`
6. `scripts/test_ps.sh`
7. `scripts/test_shell_background.sh`

### Tier 2 — supplemental interactive path (non-blocking)

1. Optional sendkey-based spot check under SMP.
2. Used as supplemental evidence only (not release gate), because sendkey timing under SMP is known flaky.

## Required instrumentation outcomes

1. Preempt phase transitions appear in strictly increasing order.
2. Preempt target snapshot is non-empty and stable in operational phase.
3. No sustained condition where only BSP preempt path is active while AP targets remain permanently ineligible.

## Exit criteria

Change set is acceptable when:

1. Tier 0 passes fully.
2. Tier 1 has no new regressions attributable to this change.
3. Review findings from `07_review_log.md` are resolved.
