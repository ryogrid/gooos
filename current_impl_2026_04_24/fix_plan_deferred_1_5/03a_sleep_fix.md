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

From `tmp/sleep_longrun_summary.json`:

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

Pre-B2 baseline (reference): 0% PASS.
Post-B2 baseline (Sleep-3 was the presenting symptom): ~50 % PASS
with failures concentrated at Sleep 3.

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

- **Option A (quickest)**: remove the `migrateAndPause` call
  when `target == 0` (BSP) OR when `numCoresOnline == 1`.
  Currently `scheduleRing3Wrapper` already skips migration when
  `target == cpuID()`, so this just extends that to favour BSP
  for the first few spawns.
- **Option B**: replace `migrateAndPause`'s park-on-target with
  a push-and-wake but **no pause** on the source CPU; let
  `stealWork` rescue if the target doesn't pop. Removes the
  dependency on the target AP waking promptly.
- **Option C**: explicit post-push verification — after
  `migrateAndPause` returns, assert `cpuID() == target` and
  panic otherwise. Catches the case where the wrong CPU
  resumed us. Diagnostic, not a fix.

**Recommendation for the next session**: land Option A as a
minimum-risk guard (one `if target == 0 { skip } else { ... }`
line), then re-sample. If the pass rate recovers to the 2026-
04-24 baseline ~50 %, we at least don't regress. Then
separately resume the Sleep-3 investigation via the original H1
audit, which is now the *residual* concern.

## Status — DEFERRED

- Audit instrumentation: **LANDED** (commits `4cd94e4`,
  `8c3c864`).
- 10-run sampler: **RUN** (`tmp/sleep_longrun_summary.json`).
- 50-run sampler: **DEFERRED** (session time).
- Root-cause isolation: **PARTIAL** — hypothesis ranking
  shifted; concrete fix deferred pending follow-up
  instrumentation on `migrateAndPause`.
- P03a fix implementation: **DEFERRED** to a future session.

The `TODO_SCHED.md §Deferred` list carries this forward under
the **H-04 "P03a fix deferred — audit shifted failure mode"**
entry. P05 harness re-gating stays blocked per its
dependency on P03a.
