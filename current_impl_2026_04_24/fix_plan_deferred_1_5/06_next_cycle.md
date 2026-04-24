# 06 — Next cycle after DEFERRED 1–5 implementation

## Scope & goal

This plan covers the three concrete follow-up items that surfaced
from the previous DEFERRED 1–5 implementation cycle (branch
`smp-take6-with-cc`, commits `2a54c68..60fd136`, pushed). It is
focused and bounded — it does **not** attempt to resolve
**H-01** (Plan-01 kernel-thread service-migration hazard), which
remains a larger-scope design question.

## Prior-cycle inputs

- `current_impl_2026_04_24/FINAL_REPORT.md` (with 2026-04-24
  follow-up callout).
- `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
  (audit diagnosis).
- `TODO_SCHED.md §Deferred H-01 / H-03 / H-04`.
- Key datapoint: 50-run `test_sleeptest_shell.sh` under `-smp 4`:
  8 PASS (16 %), 35 nobegin, 3 Sleep-3, 3 Sleep-1, 1 Sleep-2.

## Issues

### I-1 — Run Option D audit + diagnose P02 "nobegin" root cause

**Status going in**: `migrateAndPause` trace ring is in-tree
(commit `ebb7e1e`) but the audit sampler has not yet been run
with `runSleepAudit=true`. Without the trace dumps we still
cannot tell whether the 70 % nobegin failure is "target AP never
pops" (H1, wake/IPI loss) or "stealWork pulls bootstrap to a
non-target CPU" (new cross-queue race).

**Method**:

1. Flip `runSleepAudit = true` and `runSleeputestTest = true` in
   `src/preempt_config.go` (the sampler does this via `sed`).
2. `scripts/test_sleeptest_longrun.sh ITERATIONS=50` — rebuild
   once, run 50 QEMU boots, collect `tmp/sleep_audit_run_*.log`.
3. For each run, extract the `=== Sleep Audit Dump ===` block
   (if it fires — it runs inside `netDiag` at ~5 s post-boot,
   so nobegin runs may not reach it).
4. Classify trace entries:
   - **(a) target never popped**: for every migrateAndPause
     push to target `T`, the matching `migrateTraceResume` never
     fired or `resumeCPU == 0xFFFFFFFF` — wake/IPI delivery issue.
   - **(b) wrong CPU resumed**: `resumeCPU != targetCPU` —
     stealWork race.
   - **(c) clean**: `resumeCPU == targetCPU` for all pushes in
     a successful run.

**Acceptance**: at least one nobegin run's trace dump is
captured and classified as (a), (b), or (c). If (a) dominates,
Option B (remove PauseLocked, let stealWork rescue) is the
likely fix. If (b) dominates, Option C (post-migrate assertion +
retry) is the likely fix. If trace dumps cannot be obtained
(nobegin kills before netDiag fires), document and propose
a non-netDiag dump trigger (e.g., dump every N pushes
unconditionally when `runSleepAudit=true`).

**If a clean fix is identifiable this session**, implement and
re-sample. If not, write `03b_sleep_fix_v2.md` and defer.

### I-2 — Statistical answer: is P02 regression sleeptest-specific?

**Status going in**: user asked this exact question; only 1
hand-smoke test of `smpprobe` exists. The sleeptest regression
dropped PASS rate from ~50 % to 16 % under `-smp 4`; whether
other programs suffer similarly is unknown.

**Method**: wrap `scripts/test_goprobe_shell.sh` in a 50-iter
sampler identical in structure to
`scripts/test_sleeptest_longrun.sh`, or re-use the existing
harness inside a simple outer loop. Collect pass/fail counts
per run. Emit a JSON summary at `tmp/goprobe_50run_summary.json`.

**Choice rationale**: `goprobe` is chosen over `smpprobe`
because it exercises a richer set of spawn patterns (go +
chan + select + yield-loop) and produces clearer per-subtest
output. `smpprobe` specifically tests worker distribution
which is P02's target, so its pass rate is biased upward for
the purpose of answering "is the regression sleeptest-specific".

**Acceptance**: a summary rate (PASS/FAIL over 50 runs) for
`goprobe` under `-smp 4`. Update `03a_sleep_fix.md` or a new
`06a_cross_program_sampling.md` with the comparison.

### I-3 — `make run-smp` / `make clean; make run-smp` slow build

**Status going in**: user reports a multi-minute stall. Session
inspection showed one `[tinygo] <defunct>` zombie (CPU time
25 min) after `tmp/kernel.iso` was already produced at the
4-minute mark. Candidate causes:

- (a) TinyGo `-interp-timeout=10m` phase is abnormally slow
  post-patch (my additions touch `scheduler_cores.go` — any
  `interp` walks the go code at build time).
- (b) The Makefile's run-smp target depends on ISO creation
  but a zombie-reap issue leaves `make` waiting on a dead
  child.
- (c) Some other slowdown unique to the user's environment
  (RAM pressure, concurrent process).

**Method**:

1. `time make build` on a clean tree; record wall-clock.
2. `time make iso` separately; record.
3. `time make run-smp` (kill after ~30 s if it reaches QEMU
   banner).
4. Inspect `Makefile` for `.PHONY`, dependency order, any
   `run-smp` → `iso` → `build` chain.
5. If elapsed time > 3 min, check whether removing my patch
   additions (notePush/Pop hooks, migrateTrace) restores
   baseline — a single revert commit locally, re-time, revert-
   the-revert.

**Acceptance**: either a fix is identified and landed, or the
root cause is documented with enough evidence that the user
knows what to expect (e.g., "first clean build takes ~4 min
due to TinyGo interp; subsequent builds are 60 s").

## Execution order

1. Write this doc + extend `TODO_SCHED.md §Next cycle`. Commit.
2. I-3 first (cheap + resolves user's immediate friction). Time
   a clean build, document.
3. I-1 audit run (75 min background).
4. While I-1 runs, also start I-2 can't share qemu — sequence
   them. Do I-2 (likely 75 min) only after I-1 finishes, or
   defer I-2 to end if I-1 produces a fix.
5. Reviewer pass.
6. TODO_SCHED final-verification update.

## Commit cadence and conventions

Same as prior cycle. Subject prefix: `TODO_SCHED/I-N:`. One
commit per checklist item. No push without user instruction.

## File touchpoints (anticipated)

- `current_impl_2026_04_24/fix_plan_deferred_1_5/06_next_cycle.md`
  (this file, new)
- `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
  (append I-1 findings; existing)
- `TODO_SCHED.md` (extend; existing)
- Possibly `src/preempt_config.go`, `src/net.go`,
  `src/percpu.go`, and TinyGo patch for I-1 fix (if identified)
- `scripts/test_goprobe_longrun.sh` (new, I-2 sampler)
- Possibly `Makefile` (for I-3 if actionable)

## Out of scope

- H-01 (Plan-01 service-migration design). Separate design
  session.
- Full `test_smp_release_gate.sh` 8×50 run. I-2 covers one
  cross-program datapoint.
- Fixing the Sleep-3 residual F1 hang — separate from I-1; the
  current audit targets the nobegin pattern, not Sleep-3.
- `git push`.
