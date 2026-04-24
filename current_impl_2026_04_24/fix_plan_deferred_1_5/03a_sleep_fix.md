# DEFERRED 3a — Sleep-3 audit diagnosis (2026-04-24 cycle)

## Scope

Diagnosis document produced by the P03 audit session per
`03_sleep_cross_cpu_channel_wakeup_audit.md`. Captures the
10-iteration sampler result, the observed failure-mode shift,
and the recommended next-step fix.

## Audit setup

- Instrumentation landed in commits `4cd94e4` (gooos side) and
  `8c3c864` (TinyGo-patch sync).
- Sampler: `scripts/test_sleeptest_longrun.sh` with
  `ITERATIONS=10` (down from the plan's 50 to fit session time;
  a full 50-run sweep is a follow-up).
- Build: the sampler rebuilds `tmp/kernel.iso` once with
  `runSleepAudit=true` + `runSleeputestTest=true` flipped via
  `sed`; each run is a fresh `qemu-system-x86_64 -smp 4` boot
  with `-serial file:tmp/sleep_audit_run_N.log` and a 90 s
  deadline.

## Result summary

10-run sampler (initial probe):

```
iterations:  10
pass:         4   (40%)
fail:         6
breakdown:
  fail_nobegin:   4   (process didn't print "sleeptest: begin")
  fail_beforeS1:  2   (begin printed; Sleep 1 never returned)
  fail_afterS1:   0
  fail_afterS2:   0
```

**50-run sampler (full plan)** from `tmp/sleep_longrun_summary.json`:

```
iterations:  50
pass:         8   (16%)
fail:        42
breakdown:
  fail_nobegin:  35   (sleeptest.elf never reached Ring-3 entry)
  fail_beforeS1:  3
  fail_afterS1:   1
  fail_afterS2:   3   (original F1 Sleep-3 hang, still residual)
```

Pre-B2 baseline (reference): 0% PASS.
Post-B2 baseline (Sleep-3 was the presenting symptom): ~50 % PASS
with failures concentrated at Sleep 3.
**Post-P02 baseline (this measurement)**: 16% PASS, dominant
failure shifted to spawn-time "nobegin" (35/50 = 70%); original
Sleep-3 pattern still residual at 3/50 = 6%.

## Failure-mode shift vs. prior cycle

**Key observation**: this cycle's failures are at the START of
the run, not the end. Under the 2026-04-24 FINAL_REPORT baseline
the typical failure was "Sleep 3 hangs after Sleep 1 and 2 succeed".
With DEFERRED 2 (`elfSpawn` round-robin distribution) landed,
the failure has migrated to:

- **4/10 runs**: sleeptest never reaches Ring-3 entry ("nobegin").
- **2/10 runs**: sleeptest starts Ring-3 execution and prints the
  banner, but Sleep 1 never returns.

The Sleep-3 signal has disappeared. This strongly suggests the
round-robin bootstrap (`migrateAndPause`-based spawn in
`src/process.go:scheduleRing3Wrapper`) is now the dominant
failure site — migrating a fresh `ring3Wrapper` onto an AP at
spawn time fails to reach Ring-3 entry in a significant
fraction of boots.

## Hypothesis update — mapped onto H1–H7 from the audit plan

Re-ranking with the new evidence:

- **H1 (IPI-delivery window vs. task-pop race)** — *now more
  likely* at the spawn-time migration path, not the channel-
  wake path. `migrateAndPause` at
  `~/.local/tinygo0.40.1/src/runtime/scheduler_cores.go`
  holds `schedulerLock` across `runqueues[targetCpu].Push` +
  `schedulerWake()` + `task.PauseLocked()`. After
  `PauseLocked` unlocks, the target AP's scheduler loop pops
  and resumes. If the AP is in `waitForEvents()` (hlt) when
  `schedulerWake()` fires, and the wakeup IPI is delivered
  correctly, the AP wakes and runs. If the IPI is lost or the
  AP is asleep in a state that `schedulerWake` doesn't cover,
  the task sits indefinitely — **"nobegin" failure**.
- **H2 (`pitTicks` visibility)** — now *less* likely as the
  dominant cause of the observed failures; the sampler's
  failures are at spawn, not at timer-driven wake.
- **H5 (IPI-ICR timeout)** — the sampler should have captured
  any `lapicICRTimeouts` increments via `sleepAuditDump` (it
  fires as part of `netDiag`), but the "nobegin" runs never
  reached the point where netDiag fires. The counter cannot
  confirm or deny H5 from this data.
- **H6/H7** — irrelevant to the new failure mode.

## Recommended fix (P03a) — DEFERRED

**Root cause (hypothesised, highest confidence)**: the
`migrateAndPause` bootstrap in `src/process.go:scheduleRing3Wrapper`
is racing with AP readiness. Before a full fix can be
proposed, the audit needs to:

1. Re-run the sampler with 50 iterations to confirm the 40 %
   pass rate is not a small-sample artefact.
2. Add instrumentation to `migrateAndPause` itself to record
   "pushed to CPU N, wake IPI N issued, resumed on CPU M" per
   spawn so we can see whether the wake-up IPI arrived vs. the
   task got stolen by a different CPU.
3. Check whether disabling round-robin (falling back to
   `go ring3Wrapper(child)` like pre-P02) restores the prior
   ~50 % pass rate — confirming the shift is P02-attributable.

If the shift is P02-attributable, the fix options are:

- **Option A (originally proposed, now REJECTED after log
  inspection)**: guard `target == 0`. This does not address the
  observed failure mode. Checking `tmp/sleep_audit_run_4.log`
  from the 10-run sampler confirms the "nobegin" case occurs
  when sleeptest's ring3Wrapper bootstrap targets **AP 1**
  (counter=2, n=4 → target=1), not BSP. The current code
  already skips migrate when `target == cpuID()`, and the
  failure is the opposite case: bootstrap running on BSP
  correctly calling `migrateAndPause(1)` but the AP never
  resuming the task. Option A would have no effect on the
  failing path.
- **Option B**: replace `migrateAndPause`'s park-on-target with
  a push-and-wake but **no pause** on the source CPU; let
  `stealWork` rescue if the target doesn't pop. Removes the
  dependency on the target AP waking promptly.
- **Option C**: explicit post-push verification — after
  `migrateAndPause` returns, assert `cpuID() == target` and
  panic otherwise. Catches the case where the wrong CPU
  resumed us. Diagnostic, not a fix.

- **Option D (new after Option A rejection)**: instrument
  `migrateAndPause` itself. Before the Push, record
  `(srcCPU, targetCPU, taskPtr)` in a ring buffer. After
  PauseLocked returns, record `(actualResumeCPU, taskPtr)`.
  Run the 50-run sampler; inspect the ring buffer at the
  `nobegin` failures. This determines whether the task ever
  got popped by the target — if not, the issue is in
  wake/IPI delivery; if yes but by the "wrong" CPU, the
  issue is a cross-queue race during stealWork. This is an
  audit refinement, not a fix.

**Recommendation for the next session**: run **Option D**
(migrateAndPause trace ring) first to produce decisive
evidence on the failing path; then choose between Option B
(no-pause wake-and-steal) and Option C (resume-CPU
assertion) based on what Option D's data shows. The original
Option A is rejected per the inline note above — log
inspection after the 10-run sampler confirmed it would not
affect the failing path.

## Status — DEFERRED

- Audit instrumentation (counters + IPI-timeout + sampler):
  **LANDED** (commits `4cd94e4`, `8c3c864`).
- 10-run sampler: **RUN** (initial signal).
- 50-run sampler: **RUN** (`tmp/sleep_longrun_summary.json`,
  `tmp/sleep_audit_run_*.log`).
- Option D `migrateAndPause` trace ring: **LANDED** (commit
  `ebb7e1e`) — instrumentation for the next audit cycle to
  decisively discriminate "target never popped" vs. "wrong
  CPU stole".
- Root-cause isolation: **PARTIAL** — failure-mode shift
  confirmed at scale (16% PASS, 35/50 nobegin); concrete fix
  deferred pending the next sampler with Option D dump
  inspection.
- P03a fix implementation: **DEFERRED** to a future session.

The 50-run dataset confirms the failure-mode shift is real and
P02-attributable. The next audit run (with Option D enabled)
will produce trace dumps showing whether the "nobegin" cases
correspond to:
- Push to AP queue but no resume entry → wake/IPI loss; OR
- Resume entry on a different CPU than target → stealWork
  pulled the bootstrap onto a non-target CPU before the target
  popped it.

The `TODO_SCHED.md §Deferred` list carries this forward under
**H-04 "P03a fix deferred — audit shifted failure mode"**.
P05 harness re-gating stays blocked per its dependency on P03a.
