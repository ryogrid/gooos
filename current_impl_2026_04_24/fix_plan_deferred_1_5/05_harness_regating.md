# DEFERRED 5 — Harness re-gating: `test_smp_shell_preempt.sh` + `test_sleeptest_shell.sh` (G1, G2)

## Scope & goal

**Scope (verbatim from `FINAL_REPORT.md §Deferred` item 5)**:
*Re-gate `test_smp_shell_preempt.sh` and `test_sleeptest_shell.sh`
from "diagnostic / reproducer" to release-blocking regression
harnesses.*

**Goal**: once the underlying flakes close (B1 / DEFERRED 2 for
preempt distribution, F1 follow-up / DEFERRED 3 for the Sleep
flake), flip both harnesses' header text to "regression — release
blocking" and add them to `scripts/test_smp_stability_sample.sh`'s
sampler matrix with a ≥ 95 % pass-rate gate. Remove the
"expected fail" / "diagnostic only" notes in the delta docs.

## Root-cause analysis (why they are diagnostic today)

### `test_smp_shell_preempt.sh`

- Pass criterion: ≥ 5 `^marker [0-9]+ cpu=` lines in the serial
  log (`scripts/test_smp_shell_preempt.sh`).
- Today's behaviour: `markers_observed` ranges 0..5 across runs
  with no deterministic predictor. `cpuhog.elf` and
  `markerprint.elf` both auto-launched via
  `runSMPShellPreemptProbe`, but `markerprint`'s output stalls
  because both processes get scheduled on BSP — `cpuhog` then
  hogs BSP and `markerprint` starves until preempt fires.
- DEFERRED 2 (B1) — round-robin distribution of `ring3Wrapper`
  goroutines — pushes `cpuhog` to one CPU and `markerprint`
  to another at spawn, so the preempt machinery isn't even
  needed for forward progress.

### `test_sleeptest_shell.sh`

- Pass criterion: all three `Sleep N OK` lines + `ALL SLEEPS PASS`.
- Today's behaviour: ~50 % pass rate (Sleep N=1 always works,
  Sleep N=2 usually, Sleep N=3 ~50 % of the time).
- DEFERRED 3 (F1 follow-up) — the channel-wakeup audit and the
  fix it produces — closes the residual.

## Design approach

This plan describes the work the implementer does **after** B1
and F1-follow-up land:

1. Re-run each harness 50 times via the sampler. Record the
   pass rate.
2. If pass rate ≥ 95 %, flip the header text and the sampler
   inclusion. If 50–95 %, file a follow-up note under
   `current_impl_2026_04_24/fix_plan_deferred_1_5/05a_residual_flake.md`
   and keep "diagnostic" status.
3. Update the relevant delta-doc Open Questions sections
   (`10_test_harnesses_delta.md`, `09_user_programs_sleep_vs_yield.md`,
   `03_smp_preempt_phase_gating.md`) to reflect the new status.

### Concrete header replacements

Current `scripts/test_smp_shell_preempt.sh:1–18`:

```
# scripts/test_smp_shell_preempt.sh — feature 2.3 sub-gate (b).
# ...
# PASS = ≥ 5 `marker <N>` serial lines within 15 s.
```

Replace with (after re-gating):

```
# scripts/test_smp_shell_preempt.sh — feature 2.3 sub-gate (b).
#
# RELEASE-BLOCKING regression harness as of <commit-sha>. Boots
# -smp 4 and auto-launches cpuhog.elf + markerprint.elf at
# bspBootDone via runSMPShellPreemptProbe. With round-robin
# ring3Wrapper distribution (B1) the two processes land on
# distinct APs; with kernel-goroutine preemption (feature 2.1)
# markerprint always makes progress.
#
# PASS = ≥ 5 `marker <N>` serial lines within 15 s, ≥ 95 % across
# the 50-run sampler at scripts/test_smp_stability_sample.sh.
```

Current `scripts/test_sleeptest_shell.sh:1–8`:

```
# scripts/test_sleeptest_shell.sh — deterministic sleeptest validation
```

Replace with:

```
# scripts/test_sleeptest_shell.sh — RELEASE-BLOCKING sleeptest
# regression. Validates user-process sys_sleep wakeup across CPUs
# under -smp 4. Ring-3 process calls gooos.Sleep(10) three times;
# all three must complete and the program must print
# "ALL SLEEPS PASS". ≥ 95 % over the 50-run sampler is required.
```

### Build a 50-run release-gate sampler around the existing single-sample harness

**Important factual correction**: `scripts/test_smp_stability_sample.sh`
in the current tree (verified at HEAD `0a840d4`, ~187 lines) is
**not** an outer-loop harness sampler — it is a **single-shot**
sampler that boots the kernel once, runs `gochan` and `smpprobe`
interactively, and classifies the one boot. There is no
`HARNESSES=(...)` array and no 50-run loop. The
`current_impl_2026_04_24/10_test_harnesses_delta.md` description
("Multi-run sampler across `gochan`, `smpprobe`") refers to
internal sampling within the one boot, not to an outer matrix.

The implementer therefore writes a **new** wrapper script,
suggested name `scripts/test_smp_release_gate.sh`, that:

```bash
#!/usr/bin/env bash
# scripts/test_smp_release_gate.sh — release-blocking sampler.
# Runs each harness 50 times; exits 0 only if every harness
# achieves ≥ 95 % PASS.

set -u

HARNESSES=(
    "scripts/test_smp_basic.sh"
    "scripts/test_smp_shell_distribution.sh"
    "scripts/test_smp_shell_smpprobe.sh"
    "scripts/test_smp_shell_preempt.sh"      # added by DEFERRED 5
    "scripts/test_sleeptest_shell.sh"         # added by DEFERRED 5
    "scripts/test_goprobe_shell.sh"
    "scripts/test_preempt_kernel.sh"
    "scripts/test_preempt_user.sh"
)

ITERATIONS=50
THRESHOLD_PERCENT=95

# … per-harness loop, count PASS/FAIL, write JSON summary,
#     exit non-zero if any harness < THRESHOLD_PERCENT %.
```

Reset of `tmp/preempt_config_*.go.bak` between runs is already
handled by `scripts/harness_lib.sh:harness_recover_stale_backup`
sourced inside each harness — no extra wrapper logic needed.

The existing `scripts/test_smp_stability_sample.sh` stays as-is
and is added to `HARNESSES` if useful (its single-sample
pass/fail still has signal value).

JSON output format suggestion (consumed by no other tooling
yet, so keep it simple):

```json
{
  "iterations": 50,
  "threshold_percent": 95,
  "results": {
    "scripts/test_smp_shell_preempt.sh": {"pass": 49, "fail": 1, "rate": 98.0},
    "scripts/test_sleeptest_shell.sh": {"pass": 50, "fail": 0, "rate": 100.0},
    ...
  },
  "overall": "PASS"
}
```

### Delta-doc Open Question closures

`current_impl_2026_04_24/10_test_harnesses_delta.md §Open
Questions` currently flags G1 and G2 as deferred. Replace those
two bullets with:

```
- **Closed (G1)**: `test_smp_shell_preempt.sh` is now a
  release-blocking regression. Pass rate <NN.N>% across the
  50-run sampler (commit <sha>). Underlying B1 fix landed in
  commit <sha>.
- **Closed (G2)**: `test_sleeptest_shell.sh` is now a
  release-blocking regression. Pass rate <NN.N>% across the
  50-run sampler (commit <sha>). Underlying F1-follow-up fix
  landed in commit <sha>.
```

`current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md
§Open Questions` — remove the F1 follow-up bullet (already
closed) and the F2 bullet (moot once F1 closes).

`current_impl_2026_04_24/03_smp_preempt_phase_gating.md §Open
Questions` — remove the smpprobe distribution bullet (closed by
B1).

## File / symbol touch-points

| File | Status | Purpose |
|---|---|---|
| `scripts/test_smp_shell_preempt.sh` | Modify | Header text update; no logic change. |
| `scripts/test_sleeptest_shell.sh` | Modify | Header text update; no logic change. |
| `scripts/test_smp_release_gate.sh` | **New** | 50-iteration outer-loop sampler around the existing per-harness scripts; emits JSON summary; non-zero exit on any harness < 95 %. |
| `scripts/test_smp_stability_sample.sh` | Optional touch | Leave its single-shot semantics intact; reference from the new release-gate sampler if useful. |
| `current_impl_2026_04_24/10_test_harnesses_delta.md` | Doc update | Close G1 + G2 Open Questions. |
| `current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md` | Doc update | Close F1-follow-up Open Question. |
| `current_impl_2026_04_24/03_smp_preempt_phase_gating.md` | Doc update | Close smpprobe distribution Open Question. |
| `current_impl_2026_04_24/FINAL_REPORT.md` | Doc update | Remove DEFERRED 5. |
| `current_impl_2026_04_24/fix_plan_deferred_1_5/05a_residual_flake.md` | **New (only if pass rate < 95 %)** | Captures the residual flake follow-up. |

## TinyGo runtime patch changes

**None.** Pure harness + doc work.

## Acceptance criteria

1. Both harnesses' header text declares "RELEASE-BLOCKING".
2. `scripts/test_smp_stability_sample.sh` includes both
   harnesses in its matrix.
3. Per-harness pass rate ≥ 95 % across 50 runs.
4. The sampler exits with status 0.
5. Delta-doc Open Questions are updated.

## Verification plan

```
make iso
bash scripts/test_smp_release_gate.sh
```

Inspect `tmp/release_gate.json` for per-harness pass rates.
All ≥ 95 %.

Spot-check single runs:

```
bash scripts/test_smp_shell_preempt.sh
bash scripts/test_sleeptest_shell.sh
```

Both should PASS individually with high reliability.

## Risk & rollback

| Risk | Impact | Mitigation |
|---|---|---|
| Pass rate is 90–95 % — tantalisingly close but not over the gate | Residual flake remains visible | Write `05a_residual_flake.md` with a focused next-step audit; do NOT flip header text. |
| New entries in the sampler significantly slow the matrix | Sampler runtime explodes | Keep the matrix to ≤ 10 harnesses; budget ≤ 30 s per harness × 50 runs ≈ 4 hours total. Run sampler in CI overnight, not pre-commit. |
| Delta-doc close-out misses an open question that was actually still open | False sense of completeness | Cross-check against `FINAL_REPORT.md §Deferred` final state; reviewer subagent must verify. |

**Rollback**: revert the header changes; pull the two harnesses
back out of the sampler matrix.

## Dependencies

- **Depends on DEFERRED 2 (B1) and DEFERRED 3 (F1 follow-up)**.
  Cannot land without those PASS rates being ≥ 95 % first.
- No dependency on DEFERRED 1 or 4 directly (though both may
  influence Sleep flake closure).

## Estimated effort

**Small.** Mostly text changes plus a sampler-matrix edit and a
single 4-hour overnight run. One short session once the
prerequisites land.
