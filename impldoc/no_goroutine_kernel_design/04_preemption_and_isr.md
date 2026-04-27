# 04 — Preemption and ISR integration

The current preempt substrate works: BSP's 100 Hz LAPIC timer
broadcasts preempt IPI vector 0xFB to every AP every tick
(`src/lapic_timer.go:88`), each AP's `handlePreemptIPI` safe-point-
checks at `src/goroutine_irq.go:89..173` and yields via
`runtime.Gosched()`. Ring-3 preemption is via kernel-delivered
SIGALRM on the iretq frame (`src/goroutine_irq.go:138`). Route C
replaces the "yield via runtime.Gosched" call with "yield via
kschedYield", keeps everything else byte-for-byte, and adds quantum
accounting to `KernelThread`.

## Principle

**Every preempt decision that today yields a TinyGo goroutine today
will tomorrow yield a kernel thread.** The ISR handler, the safe-
point check policy, the IPI vectors, and the per-CPU WantReschedule
flag all stay. What changes is the target of the yield.

## Current ISR integration (STATUS-QUO)

| Vector | Handler | File:line | Routes to |
|--------|---------|-----------|-----------|
| 0x20 (IRQ0 / PIT) | `handleTimer` | `src/pit.go:53` | BSP only. Increments `pitTicks`, polls keyboard fallback, broadcasts wakeup IPI (vector 0xFC) to APs, ISR-side sleep audit dump |
| 0xFE (LAPIC timer) | `handleLAPICTimer` | `src/lapic_timer.go:88` | Every online CPU. Sets `perCPUBlocks[cpu].WantReschedule = 1`, broadcasts preempt IPI (vector 0xFB) on BSP once warm-up ticks elapse, delivers SIGALRM to Ring-3 user processes on BSP |
| 0xFB (preempt IPI) | `handlePreemptIPI` | `src/goroutine_irq.go:89` | Every CPU (broadcast from BSP). Safe-point-checks, yields if safe. Ring-3: delivers SIGALRM via iretq-frame rewrite. Ring 0: calls `runtime.Gosched()` (→ TinyGo scheduler) |
| 0xFC (wakeup IPI) | minimal / scheduler wake | via `pitWakeAPs` | APs wake from `hlt` so they drain local queue |
| 0x80 (int n) | syscall dispatcher | (syscall dispatch path) | Route C does not touch |

## Route C integration (PROPOSED)

The handler bodies stay in place; only the yield call sites change.

### `handlePreemptIPI` (`src/goroutine_irq.go:89..173`)

The existing safe-point checks are preserved verbatim:

1. `readInterruptDepth() > 1` (nested ISR) → bail
   (`src/goroutine_irq.go:100..105`).
2. `readPreemptDisable() > 0` (spinlock held, K4) → set
   `WantReschedule = 1`, bail (`src/goroutine_irq.go:106..112`).
3. `readSyscallDepth() > 1` (real int 0x80 nested above us) → bail
   (`src/goroutine_irq.go:113..118`).
4. Ring-3 via iretq-frame rewrite (`src/goroutine_irq.go:130..152`)
   — **unchanged**. Still calls `maybeDeliverSignal`. Still bumps
   the per-CPU counters. This path is how user-space preemption
   works; Route C preserves it.
5. Ring 0 yield (`src/goroutine_irq.go:155..173`) — **changed**:
   replace `gooosSchedulerYield()` (which links to
   `runtime.Gosched`, `src/goroutine_irq.go:196`) with a call to
   `kschedYield()`. The immediate prior check
   `taskCurrent() == 0` becomes `kschedRunning[cpu] == &kschedIdle[cpu]`
   (do not preempt the idle thread — there is no useful other work
   or we wouldn't be in idle).

### `handleLAPICTimer` (`src/lapic_timer.go:88..131`)

- `perCPUBlocks[idx].WantReschedule = 1` — **unchanged**. The
  kernel-side polling / idle loop reads this same flag; Route C
  re-wires the consumer (below).
- BSP broadcast of preempt IPI (`src/lapic_timer.go:119`) —
  **unchanged**.
- BSP fast-path Ring-3 signal delivery (`src/lapic_timer.go:121..128`) —
  **unchanged**.
- Warm-up counters (`src/lapic_timer.go:91..103`) — **unchanged**.

### `handleTimer` (`src/pit.go:53..76`)

- pitTicks bump — **unchanged**.
- Keyboard fallback poll — **unchanged**.
- AP wakeup IPI broadcast — **unchanged** (the wakeup IPI is how
  parked APs get their `hlt` broken so they can run their idle loop
  safe-point; Route C idle threads also park on `hlt`, so the same
  broadcast wakes them).
- ISR-side sleep audit dump (`src/pit.go:73..75`) — stays in place
  as dormant instrumentation. §10 notes that once Route C lands and
  F1 is observed dead, the audit can be deleted.

## Quantum accounting (new)

Add a per-thread `Quantum uint32` field to `KernelThread` (§02).
Semantics:

- At spawn: `Quantum = defaultQuantum` (e.g. 10 ticks = 100 ms).
- In `handleLAPICTimer`: after setting `WantReschedule = 1`,
  decrement `kschedRunning[cpu].Quantum`. If it underflows to zero,
  this tick already triggers preempt even if the safe-point check
  would otherwise allow the thread to continue.
- In `kschedSwitch` (§02 Go wrapper): on switch-in, reset
  `Quantum = defaultQuantum`.

Rationale: today a hostile compute-bound goroutine gets yielded
roughly every 10 ms (one LAPIC timer tick) because `runtime.Gosched`
is called from the preempt ISR. Route C preserves this by wiring
the same 10 ms period through `Quantum` — the preempt ISR still
yields; `Quantum` is a secondary guarantee in case the safe-point
bail path fires repeatedly (e.g. a thread toggling a spinlock in a
hot loop).

**Open question**: should the quantum be per-thread-class (I/O-bound
threads get longer quanta)? §10 parks this; day-1 uses the flat
10 ms quantum.

## `WantReschedule` consumer

Today the safe-point polling of `WantReschedule` happens inside
TinyGo's scheduler — the runtime checks periodically and preempts
the current goroutine. Under Route C we need an explicit consumer.
Two candidate sites:

1. **Every `kschedPark` / `kschedWake` site.** After a wake that
   transitions a thread to Runnable on the current CPU, if
   `WantReschedule == 1`, call `kschedYield`. This is fine-grained
   but catches most hot paths.
2. **The preempt IPI handler itself.** If the safe-point checks all
   pass, the ISR epilogue returns and the IPI fires on the next
   LAPIC tick. This is good enough provided the IPI keeps firing at
   100 Hz, which it does (`src/lapic_timer.go:119` `broadcastPreemptIPI`).

Route C recommends **option 2 only**: rely on the IPI for
preemption fidelity, keep `kschedPark`/`kschedWake` allocation-free
on the hot path. Option 1 is a fallback we add in §10 if the IPI
path turns out not to cover some corner.

## STW and spinlock invariants

The critical preempt invariant (INVARIANT K4 in §01) is:

> **A kernel thread holding a spinlock is never preempted
> mid-critical-section.**

Implemented today by `Spinlock.Acquire` bumping `%gs:48`
(PreemptDisable) and `Spinlock.Release` dropping it
(`src/spinlock.go:55..68`). The preempt ISR reads `%gs:48` at
`src/goroutine_irq.go:106..112` and bails. Route C keeps this
mechanism verbatim. The per-CPU counter nests correctly: N
spinlocks held means `%gs:48 == N`; the bail path triggers whenever
N > 0.

STW interaction (full detail in §05):

- STW is initiated by the GC. The GC sends a broadcast **freeze
  IPI** (PROPOSED, new vector — see §05) to every CPU.
- The freeze-IPI handler does: "if I hold a spinlock (`%gs:48 > 0`),
  set a per-CPU `WantSTWFreeze` flag and return"; else enters a
  spin-loop on a global STW flag.
- `Spinlock.Release` checks `WantSTWFreeze` just before dropping
  `%gs:48`; if set, transitions into the freeze spin instead of
  returning.
- All threads either reach a safe point (enter freeze loop) or were
  already at a safe point (caught by the IPI directly). No thread
  can re-enter a spinlock between "release" and "freeze" because
  interrupts are disabled from acquire to release (already the
  current contract via `readFlags`/`restoreFlags` in
  `src/spinlock.go:56..67`).

The STW freeze is deadlock-free because:

1. No thread holds lock A while waiting for lock B when the freeze
   starts (the rank system at `src/spinlock.go:7..20` prevents
   cycles).
2. Every primitive in §03 releases its lock before waking.
3. The freeze flag check happens *after* release, so a thread that
   was mid-critical-section finishes the section and then freezes —
   no frozen thread can strand another thread's wake.

Reviewer gate (§Phase-B check 3) verifies the two-step release→freeze
pathway does not admit a race.

## Interaction with Ring-3 preemption

**Ring-3 preemption survives untouched.** The path is:

1. BSP 100 Hz LAPIC tick → `handleLAPICTimer` on BSP
   (`src/lapic_timer.go:88`).
2. `broadcastPreemptIPI` fires on every AP (and self on BSP).
3. `handlePreemptIPI` on the target CPU inspects the iretq frame at
   `lastFramePtrs[cpu]`.
4. If `frame.CS & 3 == 3` (Ring 3 interrupted),
   `maybeDeliverSignal(frame)` rewrites `frame.RIP` / `frame.RSP`
   (`src/goroutine_irq.go:138..144`).
5. iretq returns into the user-space SIGALRM handler.

None of these steps knows or cares whether the hosting kernel-side
context is a TinyGo goroutine or a kernel thread. The
`lastFramePtrs[cpu]` slot (populated by `go_interrupt_handler` in
isr.S) is per-CPU, not per-thread, so migrating the host does not
invalidate it.

**Invariant re-affirmed**: Ring-3 preemption does not regress. §09
adds a regression gate in M4 (`scripts/test_smp_shell_preempt.sh`).

## Interaction with the sleep-audit ISR dump

`src/pit.go:73..75` calls `sleepAuditISRDump` every 200 ticks when
`runSleepAudit == true`. The dump reads
`SchedTasksPushed[cpu] / SchedPopOk / SchedPopNil / lapicICRTimeouts /
afterTicksCalls` (`src/percpu.go:88..112`).

Under Route C:

- `SchedTasksPushed` / `SchedPopOk` / `SchedPopNil` lose their
  original meaning (they were TinyGo-scheduler counters
  patched in at `scripts/tinygo_runtime.patch:1004` and the pop
  site). Route C removes those patch hunks in M5 (§08) and the
  counters go unfilled.
- Rather than delete the dump entirely, **M5 repoints the counter
  feed** to the new `kschedPush` / `kschedPop` paths in
  `kthread_sched.go`. Counter names stay; semantics are analogous
  ("enqueued a Runnable onto this CPU's queue", "popped one to
  run"). ISR dump format stays readable.
- `afterTicksCalls` (`src/afterticks.go:62`) stays — the wheel is
  still in use (§03), now signaling `KEvent`s.
- `lapicICRTimeouts` stays.

This gives §10 a follow-up to delete `runSleepAudit` *after* Route C
has soaked, but keeps the ISR dump useful during migration.

## New ISR surface introduced by Route C

Only one: the **freeze IPI** used by §05 STW. Detailed in §05.

No other new vectors. Specifically:

- No "kernel-thread preempt IPI" — we reuse vector 0xFB.
- No "kernel-thread wake IPI" — we reuse vector 0xFC (the existing
  wakeup IPI broadcast from `pitWakeAPs`, `src/pit.go:83..100`).

## Summary of touch-points

| File | What changes | What stays |
|------|-------------|-----------|
| `src/pit.go` | Nothing (vector 0x20 handler untouched) | all |
| `src/lapic_timer.go` | Nothing | all |
| `src/goroutine_irq.go` | `gooosSchedulerYield` → `kschedYield` at line 172; `taskCurrent() == 0` check at line 156 → idle-thread check | counters, safe-point logic, Ring-3 iretq-frame path |
| `src/spinlock.go` | `Release` adds a STW-freeze check; otherwise unchanged | Acquire / Release ranks / body |
| `src/percpu.go` | Adds `WantSTWFreeze` flag (§05) | all existing fields |
| `src/kthread_sched.go` (NEW) | `kschedYield` body | — |

Patch hunks into `scripts/tinygo_runtime.patch` are described in
§08; at a high level, the per-CPU SchedTasksPushed counter bumps in
the patch get rewritten to point at the new kthread-scheduler bumps.

## Reviewer gates

- All preempt paths (timer, IPI, safe-point bail) named: **yes**.
- Spinlock → no-preempt invariant explicit: **yes** (INVARIANT K4,
  INVARIANT L1).
- STW + spinlock deadlock argument: **yes** — see §05 too.
- Ring-3 preemption preserved: **yes** — iretq-frame path untouched.
- Quantum accounting described: **yes**.
