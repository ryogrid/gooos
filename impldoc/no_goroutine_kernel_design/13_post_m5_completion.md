# 13 — Post-M5 completion plan

This doc captures the work remaining after the M5.2
`scheduler=none` flip (commit `5314e1f`) and the M5-fix follow-ups
(`8824d07`, `b6d337c`) to declare Route C complete. It is a
sibling to `12_implementation_notes.md` (the rolling progress
log) and to the per-commit checklist in `TODO_NOGOTIN.md`.

The architectural milestone is intact: zero `go ` sites in
`src/*.go`, scheduler=none compiles + boots, and smoke + ps + net
all PASS. What remains is one load-bearing fix (cross-CPU
kthread wake) plus housekeeping.

## Status snapshot at start of post-M5 work

| Gate | Result under scheduler=none | Notes |
|---|---|---|
| `make build` (lint + verify-globals) | clean | — |
| `test_kthread_smoke.sh` | PASS (A=5 B=5) | — |
| `test_ps.sh` | PASS (header=1 row=1) | — |
| `test_net.sh` | PASS (UDP echo + netDiag) | — |
| `test_preempt_kernel.sh` | FAIL (1/5 markers) | kpMarker stops after marker=0 |
| `test_sleeptest_postrevert.sh` | FAIL (panic) | sleeptest panics after first sys_sleep |
| `test_smp_basic.sh` 50-iter | 86 % (below 95 % threshold) | re-measure after Phase 1 |
| `test_smp_shell_distribution.sh` 50-iter | 74 % (below 95 % threshold) | re-measure after Phase 1 |

## Root cause of the two regressions

Both `kpMarker` and `sleeptest` failures share one bug, verified
by code inspection:

- **`src/kthread_sched.go:86-100` `kschedPush(t, cpu)`** issues
  no inter-processor interrupt (IPI) when `cpu` is a different
  core. After pushing to `kschedQueues[cpu]` it just returns.
- **`src/kthread_sched.go:182-188` `kschedLoop` idle path** uses
  `gooosPause()` — a `pause` x86 spin-wait hint
  (`src/stubs.S:509-512`), NOT `hlt` — so the target AP never
  halts and never receives the cross-CPU wake.

Pre-M5 (under `scheduler=cores`) this was masked because APs ran
the TinyGo scheduler loop which had its own IPI-aware wake path
(`gooosWakeupCPU`, `src/ipi.go:109-123`). M5.2 replaced
`apSchedulerEntry` (was `//go:linkname runtime.apScheduler`) with
a direct call to `kschedLoop()`, removing the IPI-aware path
without replacing it.

The fix is two-pronged: `kschedPush` sends an IPI on cross-CPU
push, and `kschedLoop` actually halts on idle so the IPI is
received.

## Phases

### Phase 0 — This document
Create this `§13` plan doc in English markdown alongside `§12`.
Single commit:
`no-goroutine kernel: post-M5 completion plan (§13)`.

### Phase 1 — Cross-CPU kthread wake (load-bearing)

#### 1A. `kschedPush` sends an IPI on cross-CPU push

`src/kthread_sched.go` `kschedPush(t, cpu)`: after the queue-lock
release, if `cpu != cpuID()`, send a wake IPI via the existing
`gooosWakeupCPU(cpuIdx uint32)` (src/ipi.go:109-123). The IPI
vector is registered as a wake-only no-op handler; arrival just
exits the target AP's `hlt`.

#### 1B. `kschedLoop` idle path uses `sti; hlt; cli`

`src/kthread_sched.go:182-188`: replace `gooosPause()` with
`sti(); hlt(); cli()` so the AP actually halts on empty queue
and wakes on the IPI from `kschedPush` (or the next 100 Hz
LAPIC timer tick, whichever is earlier).

Note: `kschedLoopOnce` (src/kthread_sched.go:219-264) keeps its
return-immediately-on-empty behavior because it's called from
the BSP `elf.go` pump and from TinyGo's `waitForEvents` hook
(both of which handle their own halt semantics).

Single commit:
`no-goroutine kernel/M5-fix-3: cross-CPU wake IPI + AP halt-idle`.

### Phase 2 — M4 gate verification

Re-run in order, STOP and re-plan at first FAIL:

1. `make build` clean.
2. `scripts/test_kthread_smoke.sh` PASS.
3. `scripts/test_ps.sh` PASS.
4. `scripts/test_net.sh` PASS.
5. `scripts/test_preempt_kernel.sh` PASS (markers ≥ 5).
6. `scripts/test_sleeptest_postrevert.sh` 50-iter ≥ 70 % (M4.3
   baseline; M4.2.b-g result was 98 % under `scheduler=cores`
   so this re-establishes baseline under `scheduler=none`).

If gate verification surfaces only a verification commit (no
code change), fold into Phase 1's commit.

### Phase 3 — SMP scheduling fairness

Re-run after Phase 1:

- `scripts/test_smp_basic.sh` 50-iter (target ≥ 95 %).
- `scripts/test_smp_shell_distribution.sh` 50-iter (target ≥ 95 %).

If still below threshold, investigate:

- kthread placement clustering (boot shell + fsTask all on CPU 0;
  CPU 0 over-subscribed, APs under-utilized).
- Steal policy (currently only steals when local queue empty).
- Round-robin counter shared across all spawn sites.

Apply interventions one at a time and re-measure. Possible
interventions:

- Drop `fsTask` CPU-0 pin (was needed because BSP pump
  dispatched it; under IPI-aware wake, AP idle dispatch works).
- Change `kschedSpawnRing3Wrapper` to least-loaded-core
  placement using a per-CPU queue-length metric.
- Make `kschedPop` prefer stealing when local queue length
  exceeds a threshold.

After 95 % achieved, run the heavyweight
`scripts/test_smp_release_gate.sh` 8 × 50-iter (~1.5–3 hr) for
the formal release-gate sign-off.

Each intervention commits as
`no-goroutine kernel/M5-fix-N: <intervention>`.

### Phase 4 — Reviewer sub-agent pass (P1)

Launch a `general-purpose` reviewer agent with the 7-check brief:

1. K1..K5 invariants upheld (kthread struct layouts, scheduler
   dispatch invariants, kschedRunning per-CPU semantics).
2. L1 lock-ordering rank discipline across new primitives
   (`udpDgramQueue` rank 13, `serialLock`, `kthreadHostedProc`
   access ordering).
3. entry-1 (every kschedSpawn entry function meets the
   no-args, no-return signature contract).
4. syscall-1 (every syscall handler tolerates kthread-context
   `currentProc()` returning the right Process via the per-CPU
   pool-slot path).
5. Gates pass (paste latest gate result table).
6. No `go ` / `chan ` / `select` in `src/*.go` (M5.2 invariant).
7. STW deadlock-freedom: trace `serialLock` + `kschedQueues` +
   `timerListLock` orderings; confirm no acquire-while-held
   inversion.

BLOCKING findings: fix in place + commit before P2.
MINOR findings: append to `12_implementation_notes.md` § Open
issues + risks with one-line citations.

Reviewer-pass commit (or per-fix split if BLOCKING > 2):
`no-goroutine kernel/P1: reviewer pass + BLOCKING fixes`.

### Phase 5 — README + impldoc refresh (P2)

`README.md`:

- Apply the §11 diff per `11_acceptance_and_release.md` (replace
  pre-Route-C scheduler description with the kthread scheduler
  + scheduler=none statement).

`impldoc/` and `current_impl_*/`:

- Sweep for stale refs: `scheduler=cores`, "kernel goroutines",
  `gooosOnResume`, `gInfoByTask`, `task.Pause`.
- Update or mark-legacy each.

Successor doc:

- NEW `current_impl_<today>/route_c_kernel.md` describing the
  as-built Route C kernel: kthread scheduler + udpDgramQueue +
  KEvent + timer wheel + cross-CPU IPI wake + scheduler=none
  build flow.

Single commit: `no-goroutine kernel/P2: README + impldoc refresh`.

### Phase 6 — Final sweep + report (P3)

1. `grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ user/ scripts/` —
   resolve anything new in this cycle.
2. Verify every `TODO_NOGOTIN.md` checkbox is ticked.
3. `make -C user all` clean.
4. Re-run the full gate matrix one more time:
   smoke, ps, net, preempt_kernel, sleeptest 50-iter,
   tcp_longidle 60, smp_release_gate (full 8×50).
5. In-chat final report with commit range, per-harness PASS
   rate, deferred items, doc pointers.

Closing commit (if any cleanups land):
`no-goroutine kernel/P3: final sweep + Route C declared complete`.

## Out of scope

- Aggressive TinyGo patch trim — was M5.1 stretch; deferred as
  hygiene because dead-under-scheduler-none patches are inert.
- `KQueue[T]` generics conversion — single follow-up after
  `fsReqQueue` + `udpDgramQueue` stabilize together.
- New user-space programs / kernel features beyond the existing
  test surface.

## Resumability

Each phase commits independently and ticks its corresponding
`TODO_NOGOTIN.md` checkbox in the same commit. Future sessions
read `TODO_NOGOTIN.md` and `git log b6d337c..HEAD` to resume
from the next unticked phase.
