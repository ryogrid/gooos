# DEFERRED 3 — F1 follow-up: Sleep-3 intermittent hang audit

## Scope & goal

**Scope (verbatim from `FINAL_REPORT.md §Deferred` item 3)**:
*Sleep-3 intermittent hang under `-smp 4` (suspected TinyGo
channel-wakeup cross-CPU race).*

**Goal of this plan**: **isolate the root cause** of the residual
`scripts/test_sleeptest_shell.sh` flake via a ranked set of
instrumentation audits. This document deliberately stops at
diagnosis; a *follow-up* plan (written by the session that runs
the audits) will convert the winning hypothesis into a code fix.

## Root-cause analysis — baseline observations

After the F1 dominant fix in commit `6a45e74`
(`kernelThreadSpawn(0, netRxLoop)` removed) and the B2 AP LAPIC
timer enable in commit `dd295e4`, `scripts/test_sleeptest_shell.sh`
under `-smp 4` went 0 % → ~50 % PASS. Observed failure pattern
across 5 trial runs:

```
Run 1: begin=0 s1=0 s2=0 s3=0 pass=0   (no begin print — rare)
Run 2: begin=1 s1=0 s2=0 s3=0 pass=0   (hang at Sleep 1)
Run 3: begin=1 s1=1 s2=1 s3=1 pass=1   (full PASS)
Run 4: begin=1 s1=1 s2=1 s3=0 pass=0   (hang at Sleep 3)
Run 5: begin=1 s1=1 s2=0 s3=0 pass=0   (hang at Sleep 2)
```

The hang can occur at any of the three calls. It is not "Sleep 3
specifically" — the `-3` label in the headline is shorthand for
"the pattern sometimes manages two sleeps before hanging, so it
often presents as a hang at the third".

Known-working: `yieldtest.elf` (three `gooos.Yield()` calls) PASSes
100 %, so `sys_yield` dispatch is clean. Sleep's path differs only
in taking `<-afterTicks(ticks)` before return.

Chain under audit (each suspected hypothesis numbered below):

```
Ring-3 sleeptest
  -> int 0x80, lands on cpu=X (X ∈ {0..3})
  -> sysSleepHandler (src/userspace.go:451)
       -> <-afterTicks(1)                               [H6, H7]
            -> afterTicks allocates buffered chan, registers timerList entry
            -> receiver parks on chan recv             [H3 RunState window]
PIT ISR on BSP every 10 ms
  -> pitTicks++ (src/pit.go)
     timerDispatcher goroutine
       -> reads pitTicks                               [H2 visibility]
       -> select { case ch <- struct{}{} : default }
            -> TinyGo channel send wakes receiver
                 -> scheduleTask(waiter) pushes onto BSP queue
                 -> schedulerWake() broadcasts wakeup IPI [H1, H5]
Some CPU's scheduler
  -> pop waiter from runqueue
  -> task.Resume → gooosOnResume (TSS/CR3)             [H4 race]
  -> iretq back to Ring 3
```

## Cross-reference to prior-art hypotheses

`smp_preempt_problem/README.md §Working Hypotheses` (in-tree
since 2026-04-23) lists five lettered hypotheses A–E for the
broader post-shell SMP runtime instability. Mapping to the
hypotheses in this audit plan:

| Prior-art (smp_preempt_problem) | This plan's hypothesis | Notes |
|---|---|---|
| A — keyboard input first visible failure surface | — | Keyboard now mitigated; out of audit scope. |
| B — shell-ready transition is critical boundary | — | Closed by phase-gating + DEFERRED 4. |
| C — AP wake/scheduling incomplete after boot | H1 (IPI delivery / queue race) | Most likely overlap. |
| D — `gochan` hang as process-lifecycle symptom | H4 (gooosOnResume gInfo race) | D explicitly suspects `processWait`/`processExit` synchronisation; H4 covers the closest gooos-side surface. |
| E — bare `0x...` console noise as logging issue | — | Cosmetic; out of audit scope. |

H2 (`pitTicks` visibility), H5 (IPI loss), H6 (TinyGo upstream
chan), H7 (afterTicks lifecycle) are net-new to this audit; they
do not have prior-art counterparts. Treat all seven as the
canonical list for the Sleep audit; if `gochan` itself starts
failing during the audit, escalate D to a separate plan.

## Ranked hypotheses (highest probability first)

Probabilities are the Phase-1 survey estimates, not measurements.

### H1 — IPI delivery window vs. task pop race *(65 %)*

**Claim**: `scheduleTask(waiter)` pushes the waiter onto the
**waker's** CPU queue (`runqueues[gooosCpuID()].Push(t)` at
`scripts/tinygo_runtime.patch:1004`). `schedulerWake` broadcasts
wakeup IPIs to all cores, but APs wake up, check their *local*
queue (empty), attempt `stealWork`, and under some scheduling
order fail to pick up the waiter that lives on BSP's queue. BSP
may already be busy, so the waiter sits until the next
`stealWork` hit.

**Evidence**: the push is unambiguously to the waker's queue
(patch line 1004); `stealWork` scans `(me + i) % n` but only if
the caller reaches `stealWork` when its local queue is empty
(`scripts/tinygo_runtime.patch:1088–1091`); APs in `hlt` wake
from IPI and go through `scheduler(false)` which checks local
first.

**Audit AUDIT-1** (instrumentation):

- Add a per-CPU counter `perCPUBlocks[i].SchedPopNil` incremented
  each time `runqueues[i].Pop()` returns nil in the patched
  `scheduler()` loop.
- Add `perCPUBlocks[i].SchedPopOk` for non-nil pops.
- Add a `netDiag` line printing all 8 counters.
- In `scheduleTask`, record `perCPUBlocks[cpuID()].SchedTasksPushed++`.
- Run `test_sleeptest_shell.sh` 20 times. On hang, kill QEMU
  after 30 s and dump netDiag.
- **PASS signal**: SchedTasksPushed on BSP keeps climbing,
  but no corresponding SchedPopOk on any CPU for the last
  ~1 s before timeout → task stranded.

**Expected fix** (not in this plan): either make
`scheduleTask` for channel-wakeup push to the *receiver's*
previous CPU (tracked in a new `task.lastCpu` field), or add an
AP-side "try-steal on every tick" via the new AP LAPIC timer.

### H2 — `pitTicks` visibility under SMP *(20 %)*

**Claim**: `pitTicks uint64` is a plain read/write (`src/pit.go`).
On x86-TSO a PIT-ISR increment is eventually visible on all
cores, but a `timerDispatcher` running on an AP could read a
stale value and skip the deadline-due branch for one iteration.
Under `-smp 4` the dispatcher may be stolen to an AP (now that
AP LAPIC timer is live post-B2).

**Evidence**: no atomic / fence around `pitTicks` reads. TSO
should in theory make this benign because the subsequent
`runtime.Gosched()` and per-iteration loop re-read.

**AUDIT-2**:

- Wrap reads in `atomic.LoadUint64(&pitTicks)` and writes in
  `atomic.StoreUint64`. If the flake disappears, H2 is the
  root cause.
- Add `timerDispatcherLastSeenTicks` per-iteration log.
- Expect the flake rate unchanged; if so, rule out H2 quickly.

### H3 — RunState transition window *(15 %)*

**Claim**: between `runqueue.Pop()` and `RunState = Running` the
task is briefly in an intermediate state; a concurrent
`scheduleTask(sameTask)` could be lost if it relies on
`RunState == Paused`.

**Evidence**: patched scheduler at
`scripts/tinygo_runtime.patch:1088–1099` does `Pop` then
`setCurrentTask` then `RunState = Running` — no lock across
those lines. However, the `task.Queue` is per-CPU spinlock-
protected (gooos patch adds queue-local spinlock
`scripts/tinygo_runtime.patch:18–24`).

**AUDIT-3**:

- Instrument push/pop ordering counters per CPU, plus RunState
  transition logs.
- Look for any task that is popped twice or pushed after being
  resumed.

### H4 — `gooosOnResume` gInfo race *(12 %)*

**Claim**: `gooosOnResume` (`src/goroutine_tss.go:195`) takes
`gInfoLock`, reads gi, releases lock, then dereferences
`gi.proc.pml4`. `unregisterRing3G` could delete the map entry
concurrently; `gi` remains GC-live but `gi.proc` could be mid-
teardown.

**Evidence**: sleeptest is a pure user process; its
`ring3Wrapper` doesn't `processExit` until after the three
sleeps. So the race window is narrow for *this* test. But the
autorun shell-exec path exercises many processes in sequence.

**AUDIT-4**:

- Add `gInfoByTaskStats.{inserts,deletes,reads}` counters and
  dump in `netDiag`.
- Add a `gi.proc.epoch` monotonic counter and log mismatches.

### H5 — Wakeup IPI lost on APs *(8 %)*

**Claim**: `lapicSendIPI` (`src/ipi.go:27`) writes ICR and
waits via `lapicWaitICR` (`src/smp.go:136`, bounded 65k
spins). On delivery-stalled platforms the IPI could be dropped,
leaving the target AP parked in `hlt`.

**Evidence**: `lapicWaitICR` explicitly accepts a timeout and
returns — the next PIT tick will retry via `schedulerWake` from
the next `scheduleTask`. Under sleeptest, `timerDispatcher`
fires every iteration; a dropped IPI should be recovered on the
very next iteration.

**AUDIT-5**:

- Instrument `lapicWaitICR` to count timeouts (new
  `lapicICRTimeouts uint64`) and report via `netDiag`.
- A non-zero count correlated with Sleep hangs supports H5.

### H6 — TinyGo upstream channel-wake bug *(5 %)*

**Claim**: `chanSend` in TinyGo 0.40.1 has a wake-edge race for
buffered channels under `scheduler=cores`. Not in the patch, not
in gooos's diff; pure upstream hypothesis.

**AUDIT-6**: synthetic standalone test — spawn N channel
receivers on distinct CPUs via the round-robin bootstrap from
DEFERRED 2, send from BSP, verify all wake. If any fail, H6 is
confirmed and escalates to upstream investigation.

### H7 — `afterTicks` creation-vs-park race *(3 %)*

**Claim**: `afterTicks` returns a buffered-cap-1 channel; if the
timer fires *before* the user process even parks on `<-ch`, the
buffered send succeeds, and the subsequent `<-ch` drains the
buffer. Benign — unless the timer also tries to wake a non-
existent waiter and leaves state corrupt.

**AUDIT-7**: trace send timestamps vs. park timestamps per
call. Expect park-before-send in all failing runs; a send-before-
park that correlates with PASS disproves H7.

## Design approach — audit protocol (not a fix)

**This plan delivers instrumentation**, not a code change that
closes F1. The agent running this plan will:

1. Land each audit behind a config flag in `src/preempt_config.go`
   (e.g. `runSleepAudit = false` by default; harness flips true).
2. Run each audit in turn (priority H1 → H7), producing
   `tmp/sleep_audit_<N>_<run>.log`.
3. Write a new follow-up plan under
   `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
   (or direct PR commit) documenting the winning hypothesis and
   the chosen fix.

### Instrumentation pattern

All audits share a single config flag `runSleepAudit bool` and
a shared `sleepAuditDump()` function added to `src/net.go`
alongside `netDiag`. The function prints a single block:

```
=== Sleep Audit Dump ===
SchedTasksPushed[0..3]     = ...
SchedPopNil[0..3]          = ...
SchedPopOk[0..3]           = ...
lapicICRTimeouts           = ...
gInfoByTaskStats.{ins,del,rd} = ...
pitTicks                    = ...
afterTicksCalls             = ...
timerListLivePending        = ...
=== end ===
```

The flag gate keeps all new counters out of the zero-overhead
path when audit is off. Each audit enables at most two
counter sites; the others cost one conditional load.

### Repro harness extension

`scripts/test_sleeptest_shell.sh` gets an optional environment
variable `SLEEP_AUDIT=1` that:

1. Flips `runSleepAudit` true in `src/preempt_config.go`.
2. Configures `QEMU_OPTIONS` to dump the serial log for longer
   than the current 60 s window (use 120 s).
3. On timeout or after 30 s of no progress, runs `info registers`
   via QEMU monitor and appends to the log.

## File / symbol touch-points

| File | Status | Purpose |
|---|---|---|
| `src/preempt_config.go` | Modify | Add `const runSleepAudit = false`. |
| `src/percpu.go` | Modify | Add per-CPU `SchedTasksPushed`, `SchedPopNil`, `SchedPopOk` counters. |
| `src/goroutine_tss.go` | Modify | Instrument `gInfoByTaskStats` under `runSleepAudit`. |
| `src/smp.go` | Modify | Bump `lapicICRTimeouts` in `lapicWaitICR` (defined at `src/smp.go:136`) on timeout. Declare the counter var alongside the function. |
| `src/afterticks.go` | Modify | Add `timerListLivePending` counter (existing slots used). |
| `src/net.go` | Modify | Add `sleepAuditDump()` called from `netDiag` when flag on. |
| `scripts/tinygo_runtime.patch` | Modify (optional, H1 and H3 only) | Add `gooos_sched*` linknames so the per-CPU scheduler counters can be bumped from inside the patched `scheduler()` loop. |
| `scripts/test_sleeptest_shell.sh` | Modify | Accept `SLEEP_AUDIT=1`, longer timeout, serial-log dump. |
| `scripts/test_sleeptest_longrun.sh` | **New** | 50-iteration sampler that records pass/fail + audit dump per run. |
| `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md` | **New (written later by fixer session)** | The actual fix plan, derived from the winning audit. |
| `current_impl_2026_04_24/FINAL_REPORT.md` | Doc update | After fix lands, remove DEFERRED 3. |

## TinyGo runtime patch changes

**Optional and scoped to H1 / H3**: to count scheduler-loop pop
events accurately, add three linkname bridges in
`scripts/tinygo_runtime.patch` that let gooos see `scheduleTask`
and `scheduler` pop/push events. Suggested hunk:

- File: `src/runtime/scheduler_cores.go` (inside the existing
  `gooos SMP v2` block near line 1080).
- Add:
  ```go
  //go:linkname gooosNotePush gooosNotePush
  func gooosNotePush(cpuIdx uint32)

  //go:linkname gooosNotePop gooosNotePop
  func gooosNotePop(cpuIdx uint32, ok bool)
  ```
- Call `gooosNotePush(gooosCpuID())` after the `runqueues[...].Push(t)`
  line; call `gooosNotePop(gooosCpuID(), runnable != nil)` after
  `runqueues[gooosCpuID()].Pop()`.

gooos-side `gooosNotePush` / `gooosNotePop` bodies live in
`src/percpu.go` under the `runSleepAudit` gate.

Keep the patch additions minimal and gated so a zero-overhead
release build is still possible.

## Acceptance criteria

1. All seven audit counter sets can be enabled by a single
   `runSleepAudit = true` flip.
2. `scripts/test_sleeptest_longrun.sh` produces a per-run log
   containing the full `sleepAuditDump()` block.
3. Each hypothesis H1..H7 has a textual "signal" rule the
   implementer can evaluate from the log (the §Ranked
   hypotheses section above lists them explicitly).
4. The follow-up plan `03a_sleep_fix.md` is written by the
   session that runs the audit.
5. After `03a_sleep_fix.md` is implemented,
   `scripts/test_sleeptest_shell.sh` PASS rate ≥ 95 % over 20
   consecutive boots.

## Verification plan

Initial (instrumentation only):

```
make build
make lint
make verify-globals
bash scripts/test_sleeptest_shell.sh SLEEP_AUDIT=1
bash scripts/test_sleeptest_longrun.sh      # collects 50 runs worth of dumps
```

Expected: instrumentation-off builds behave identically to HEAD;
instrumentation-on builds produce the audit block.

After fix lands (separate session):

```
bash scripts/test_smp_stability_sample.sh   # ≥ 95 % PASS
```

## Risk & rollback

| Risk | Impact | Mitigation |
|---|---|---|
| Gated counters add measurable overhead | Perf regression | Default flag false; counters gated at each site. |
| Patching TinyGo runtime adds merge risk at next TinyGo upgrade | Patch conflict | Additions are tiny and isolated in the existing gooos block; revertible hunk-by-hunk. |
| Audit reveals no winning hypothesis | Scope drift | Accept; move Sleep flake to a second-tier flake list and ship without it blocking release. |

**Rollback**: revert the audit commits. The `runSleepAudit` flag
stays false everywhere; no user-visible behaviour change.

## Dependencies

- **Does not depend on other DEFERRED items** for the audit
  phase. The final fix (in `03a_sleep_fix.md`) may depend on
  DEFERRED 1 if the winning hypothesis points to the TinyGo
  scheduler (some classes of fix are easier once gooos owns the
  scheduling policy).
- DEFERRED 5 (`05_harness_regating.md`) depends on the Sleep
  flake actually closing.

## Estimated effort

**Medium for the audit**, unknown for the fix.

- Audit: ~1 focused session to land gated counters + harness
  extension + first batch of 50 runs.
- Fix: depends on the winning hypothesis. H1 is likely medium
  effort (scheduler push-policy change in the TinyGo patch).
  H2/H5 are small (atomic wrap / retry). H6 escalates to
  upstream and is out of the gooos repo.
