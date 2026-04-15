# Phase B Overview — Goroutine Migration Execution Plan

This document organizes the ten deferred items (B1, B3–B11) from
`TODO.md` into an ordered implementation plan. Phase A already
delivered the Ring-0 goroutine/channel runtime (commits
`3cc2132..e89ac20`). Phase B retires the hand-written scheduler
(`src/scheduler.go`) and the custom channel API (`src/channel.go`)
piece by piece.

## 1. Coverage map

Every deferred item is answered in one of the five Phase-B design
documents.

| # | Item                                       | Home document                          | Section |
|---|--------------------------------------------|----------------------------------------|---------|
| B1 | `goroutine_stubs.go` (subsumed by Phase A) | This document                          | §5      |
| B3 | `serialChannel` → native `chan`            | `phase_b_channel_migrations.md`        | §2      |
| B4 | `fsRequestChannel` + per-request replies   | `phase_b_channel_migrations.md`        | §3      |
| B5 | Keyboard IRQ ring buffer + pump            | `phase_b_keyboard_irq.md`              | all     |
| B6 | Fatal handlers (`serialPanicPrint`)        | `phase_b_teardown.md`                  | §2      |
| B7 | `createTask` → `go` in `main.go`           | `phase_b_teardown.md`                  | §3      |
| B8 | Delete `src/scheduler.go` + stale stubs    | `phase_b_teardown.md`                  | §4      |
| B9 | `elfExec` → `ring3Wrapper` + `exitCh`      | `phase_b_ring3_and_exec.md`            | all     |
| B10 | Delete `src/channel.go`, strip `switch.S` | `phase_b_teardown.md`                  | §5      |
| B11 | `src/smp.go` AP idle loop                  | `phase_b_teardown.md`                  | §6      |

## 2. Dependency DAG

```
        +---------+
        |   B5    |   (keyboard IRQ ring buffer + pump)
        |  (indep) |
        +----+----+
             |
             | enables                                 B1 (closed — no work)
             v
        +---------+                                    |
        |   B4    |  fs channels                       |
        +----+----+                                    |
             |                                         |
             +-----+                                   |
                   v                                   v
             +---------+        +---------+       +---------+
             |   B3    |-------→|   B7    |------→|   B8    |
             |  serial |        | main go |       | rm sch. |
             +---------+        +---------+       +----+----+
                                                       |
             +---------+        +---------+            v
             |   B6    |------→ |   B9    |------→+---------+
             | fatal   |        |  exec   |       |   B10   |
             +---------+        +----+----+       |  rm chn |
                                     |            +----+----+
                                     v                 |
                                +---------+            |
                                |   B11   |←-----------+
                                |SMP idle |
                                +---------+
```

Interpretation:

- **B5 is independent** and can land first. The ring-buffer + pump
  design is self-contained; no other item touches the keyboard
  path.
- **B3 and B4** each migrate a specific custom channel; they
  depend on nothing else but must finish before B7.
- **B7** replaces the two `createTask` calls in `src/main.go:334,338`.
  It reaches green only after B3 + B4 migrate the bodies of
  `serialTask` and `fsTask` (or B3 removes `serialTask` outright —
  see `phase_b_channel_migrations.md §2`).
- **B8** deletes `src/scheduler.go`. Green only after every caller
  of `createTask`, `schedule`, `yield`, `taskSleep`,
  `waitQueueSleep` has migrated — i.e., after B7 + B9.
- **B9** is the trickiest migration. It owns
  `src/process.go:elfExec` and `processExit`, which currently
  manipulate `tasks[].State` directly. B9 lands after B6 because
  a fatal during exec is less disruptive once fatal handlers are
  allocation-free.
- **B10** deletes `src/channel.go`. Green only after B3, B4, B5
  have all stopped using custom channels and `selectWait`/`SelectCase`
  have no live callers.
- **B11** is a single-file cosmetic change to `src/smp.go`.
  Independent but conventionally paired with B9 so both scheduler-
  ownership changes land together.

## 3. Recommended implementation ordering

One commit per item. Each commit must pass the sendkey gate
(`tmp/test_sendkey.sh` 10 trials, pf=0/exit=3/cat=1) before
moving on. Proposed sequence:

1. **B1** close-out (documentation only — see §5).
2. **B3** — `serialChannel` retirement (mostly dead-code removal).
3. **B4** — `fsRequestChannel` migration.
4. **B6** — fatal handlers hardened.
5. **B5** — keyboard IRQ ring buffer + pump.
6. **B7** — `createTask` → `go` in `main.go`.
7. **B9** — `elfExec` → `ring3Wrapper`.
8. **B11** — SMP AP idle loop.
9. **B8** — delete `src/scheduler.go` + dead stubs.
10. **B10** — delete `src/channel.go` + strip `src/switch.S`.

Rationale: migrations (B3–B7, B9, B11) land first to drain the
legacy APIs of callers. Only then do the two large deletions
(B8, B10) happen, so the code never has a half-migrated state
where some callers point at dead code.

## 4. Updated risk register

Phase-A risks that have been **retired** by the landed
implementation (reference:
`impldoc/goroutine_design_gc_and_smp.md §8`):

- `R-runtime-collision` — resolved by
  `scripts/patch_tinygo_runtime.sh` + `~/.local/tinygo/` copy.
- `R-link-spike` — resolved: `scheduler=tasks` links and boots.
- `R-interrupt-in` — resolved:
  `gooos_in_interrupt_depth` in `src/isr.S`.
- `R-main-stack` — resolved:
  `"automatic-stack-size": true` in `src/target.json`.

Phase-A risks that **persist** into Phase B:

- `R-sleep-granularity` (10 ms PIT floor) — documented,
  unchanged.
- `R-goroutine-stack-size` — automatic sizing will re-compute
  when new `go` call sites land in B5 and B7; regression if
  estimator underestimates for `keyboardPump` / `serialTask`
  bodies.
- `R-isr-safety-enforcement` — review-time only; relevant for
  every Phase-B step that changes ISR-reachable code (B5, B6).
- `R-keyboard-latency` — subject of B5 itself.
- `R-task-stack-top-unknown` — subject of B9 (TSS.RSP0 per
  Ring-3 goroutine).
- `R-fatal-detail-loss` — subject of B6.
- `R-global-layout` — needs re-verification after every `go`
  insertion because TinyGo may emit new runtime globals
  (`runqueue`, `sleepQueue`, `timerQueue`) that must still land
  inside `_globals_start..end`.

Phase-B-specific risks added here:

- `R-b5-atomic-ring` — the keyboard ring buffer's head/tail
  semantics on x86-TSO + single-CPU v1. Full analysis in
  `phase_b_keyboard_irq.md §3`.
- `R-b9-savedparent-race` — `savedParent` global is shared
  across every `elfExec` call; under goroutines, a bug could
  interleave two execs. Single-CPU + single-exec-at-a-time
  invariant currently holds (no kernel goroutine calls
  `elfExec`); B9 must preserve it.
- `R-b7-boot-sequence` — `main()` today performs synchronous
  hardware init then creates tasks. After B7 it spawns
  goroutines; the boot-sequence timing may shift by enough that
  the GC demo or the SMP probe changes behavior. Low-probability
  but worth a single-trial smoke check.

## 5. Close-out of B1

The original design (`impldoc/goroutine_design_scheduler.md §5`)
called for a `src/goroutine_stubs.go` file hosting the six runtime
hook bodies. Phase A delivered these differently:

- `sleepTicks`, `ticks`, `ticksToNanoseconds`,
  `nanosecondsToTicks`, `deadlock`, `putchar`, `preinit`, `main`,
  `exit`, `abort` all live in
  `~/.local/tinygo/src/runtime/runtime_gooos.go`, installed by
  `scripts/patch_tinygo_runtime.sh`.
- `interrupt.Disable`/`Restore`/`In` live in
  `~/.local/tinygo/src/runtime/interrupt/interrupt_gooos.go`.
- The one remaining kernel-side artifact is
  `src/goroutine_irq.go`, which declares the Go-side handle for
  `gooos_in_interrupt_depth` (whose `.bss` home is
  `src/isr.S:159-163`).

No `src/goroutine_stubs.go` is needed. B1 is **closed — no
further work**.

## 6. Verification strategy

Shared across all Phase-B items:

- **Primary**: `tmp/test_sendkey.sh` for 10 trials per item.
  Pass = pf=0, exit=3, cat=1 on every trial.
- **Stress**: `tmp/stress_test.sh` once after each item. Pass =
  pf=0, exit=6, cat=1.
- **SMP**: `make run-smp` after B11 specifically; optional
  per-item.
- **Goroutine smoke tests** per
  `impldoc/goroutine_design_gc_and_smp.md §6.2`: add a
  development-only kernel function `goroutineSmokeTests()`
  guarded by `const runGoroutineSmoke = false` as the design
  recommends. Enable during development, disable before commit.

Each Phase-B design doc's **Verification** section names the
item-specific tests.

## 7. Pre-implementation exploration checklist

The implementer should run these greps before writing code for
each item, to catch any caller missed by these design docs:

```
grep -rn "serialPrintln\|serialPrint\b" src/       # ~104 hits
grep -rn "serialChannel\|serialSend\b" src/         # B3 scope
grep -rn "fsRequestChannel\|fsSendRead\|fsSendWrite\|fsSendCreate\|fsSendList\|fsSendDelete" src/
grep -rn "chanCreate\|chanSend\|chanRecv\|chanTrySend\|selectWait" src/
grep -rn "createTask\|schedule()\|waitQueueSleep\|taskSleep\|yield()" src/
grep -rn "\*TaskAddr\b" src/                        # dead entry-point stubs
grep -rn "tasks\[" src/                             # custom scheduler state
```

Any hit outside the files these design docs enumerate is a
surprise and should stop implementation.

## 8. Open questions

- **Should B3 delete the `serialChannel` infrastructure entirely,
  or leave a hollow `chan string` for future use?** The design
  in `phase_b_channel_migrations.md §2` recommends outright
  deletion; that can be revised during review.
- **Is a single-pass or multi-pass reviewer loop appropriate
  after the landing commits?** The overview recommends one
  reviewer pass per commit; if that proves heavy, batch by
  group (migrations / teardown / exec) as a fallback.

## 9. Documents in this set

1. `phase_b_overview.md` — this document.
2. `phase_b_channel_migrations.md` — B3 + B4.
3. `phase_b_keyboard_irq.md` — B5.
4. `phase_b_ring3_and_exec.md` — B9.
5. `phase_b_teardown.md` — B6, B7, B8, B10, B11.

All five files together define every implementation step needed
to retire the hand-written scheduler and channel APIs. After
execution, the only Ring-0 concurrency primitives are the ones
TinyGo provides: `go`, `chan`, `select`.
