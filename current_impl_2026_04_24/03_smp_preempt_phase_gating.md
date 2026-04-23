# SMP Preempt Phase Gating

**Scope:** supersedes `current_impl_0421_night/03_smp_lapic_timer_ipi.md` **§LAPIC Timer Flow** and **§IPI Paths** (preempt-fanout behavior only). All other sections of baseline 03 (SMP Boot, Per-CPU State Model, SMP Invariants, Known Unstable/Deferred SMP Surfaces) remain authoritative.

## Summary of Changes Since `a384b1a`

1. New file `src/preempt_phase.go` adds a three-state monotonic gate (`BootInit` → `SchedReady` → `Operational`) with its own spinlock (`preemptPhaseLock`). Commits: `8b75550`, `74d8eed`.
2. `handleLAPICTimer` in `src/lapic_timer.go` now gates preempt fanout on `preemptPhaseIsOperational()` in addition to the existing `preemptEnabled && idx == 0` predicate. Commit `8b75550`.
3. `broadcastPreemptIPI` in `src/ipi.go` now records a deterministic target snapshot (`preemptTargetSnapshot[]`, `preemptTargetSnapshotN`) and unconditionally fires `lapicSendSelfIPI(ipiPreemptVector)` on BSP. Commit `1c99a72`.
4. `handleLAPICTimer` also delays the BSP preempt fanout by an additional 150-tick warmup (`preemptStartupWarmupTicks`) after phase operational — deterministic late-boot quiescence.
5. `apEntry` in `src/smp.go` calls `markAPSchedulerEntered()` once per AP after `sti()` (line 326).

## Current Design

### Phase state machine (`src/preempt_phase.go`)

```
const (
    preemptPhaseBootInit     uint32 = iota  // boot start; preempt fanout inhibited
    preemptPhaseSchedReady                  // shell asked to go live; still draining APs
    preemptPhaseOperational                 // preempt fanout active
)

var preemptPhase uint32
var apSchedEnteredCount uint32
var preemptPhaseLock Spinlock               // rank same as other post-IPI locks
```

### Transitions

- **`BootInit → SchedReady`** — only transition trigger is `preemptPhaseAdvance(preemptPhaseSchedReady)`, called from `bootActivatePostShellReady()` in `src/main.go:638` (reached via syscall `#38 sys_shell_ready`).
- **`SchedReady → Operational`** — automatic inside `maybeEnterOperational()` (`src/preempt_phase.go:22`), invoked from both `preemptPhaseAdvance` and `markAPSchedulerEntered`. Condition: `bspBootDone != 0 && apSchedEnteredCount >= numCoresOnline - 1`.
- **Monotonic:** `preemptPhaseAdvance(next)` only writes if `next > preemptPhase`; nothing moves the phase backward.

Because the write path is under `preemptPhaseLock` but the hot read path (`preemptPhaseIsOperational`) is lock-free, a stale read can only **delay** enablement by at most one LAPIC tick. Never enables preempt too early.

### `handleLAPICTimer` — revised fanout predicate (`src/lapic_timer.go:88`)

```
if preemptEnabled && !runSMPProbeShellTest && idx == 0 && preemptPhaseIsOperational() {
    if preemptStartupWarmupTicks < 150 { preemptStartupWarmupTicks++; return }
    if runSMPShellPreemptProbe && preemptProbeWarmupTicks < 100 { ... }
    for i := 0; i < numCoresOnline; i++ { maybeSignalUserPreempt(i) }
    broadcastPreemptIPI()
    // BSP Ring-3 fast-path frame rewrite ...
}
lapicSendEOI()
```

Key points:

1. Only BSP (`idx == 0`) drives preempt fanout — AP LAPIC timer remains deferred (unchanged from baseline).
2. `runSMPProbeShellTest` explicitly **disables** preempt fanout when the smpprobe autorun harness runs, because that harness validates raw cooperative distribution and its grep markers would be confused by preempt-driven migrations.
3. `preemptStartupWarmupTicks` adds ~1.5 s of deterministic quiescence once the phase goes Operational, giving shell bootstrap additional headroom. `preemptProbeWarmupTicks` (100 ticks) is an older, probe-only warmup that still runs when `runSMPShellPreemptProbe` is set.

### `broadcastPreemptIPI` — deterministic targets (`src/ipi.go:126`)

Each call builds a fresh snapshot: for every online CPU `i != me`, skips if `apicID == 0 && i != 0` (transient-zero guard from baseline §SMP Invariants), and fills `preemptTargetSnapshot[0..snapN]` with the valid APIC IDs. Then sends `lapicSendIPI(target, ipiPreemptVector)` in order, and finally always fires a BSP self IPI via `lapicSendSelfIPI(ipiPreemptVector)`. No fallback to `lapicBroadcastIPI` shorthand — every tick re-records the snapshot so targeting is fully determined by the *current* APIC-ID latch state.

`preemptTargetSnapshot` and `preemptTargetSnapshotN` are read by diagnostic counters in `src/process.go:505` during `processExit` when `runSMPShellPreemptProbe` is on; they are not consumed on the steady-state hot path.

## Current Implementation Details

- `src/preempt_phase.go:15` — `preemptPhase` (zero-init = `BootInit`).
- `src/preempt_phase.go:20` — `preemptPhaseLock Spinlock`. Held only over phase transitions and the AP-count increment; never during IPI send or timer ISR logic.
- `src/preempt_phase.go:36` — `preemptPhaseAdvance(next uint32)`.
- `src/preempt_phase.go:46` — `markAPSchedulerEntered()`.
- `src/preempt_phase.go:54` — `preemptPhaseIsOperational() bool` — `//go:nosplit`, lock-free, monotonic.
- `src/lapic_timer.go:91` — revised predicate in `handleLAPICTimer`.
- `src/lapic_timer.go:94` — `preemptStartupWarmupTicks` counter.
- `src/smp.go:326` — `markAPSchedulerEntered()` call site in `apEntry`.
- `src/main.go:638` — `preemptPhaseAdvance(preemptPhaseSchedReady)` call site in `bootActivatePostShellReady`.
- `src/ipi.go:126` — `broadcastPreemptIPI`.
- `src/ipi.go:77–78` — `preemptTargetSnapshot[maxCPUs] uint8`, `preemptTargetSnapshotN uint32`.

## Diff-from-Baseline Notes

Replaces the following bullets in `current_impl_0421_night/03_smp_lapic_timer_ipi.md` **§LAPIC Timer Flow**:

- Baseline bullet 2 ("Preempt features run only on BSP (`idx == 0`) after `bspBootDone != 0`") — now extended with the `preemptPhaseIsOperational()` + `!runSMPProbeShellTest` predicates; `bspBootDone != 0` is no longer sufficient.
- Baseline bullet 3 (probe warmup) — extended with the unconditional 150-tick `preemptStartupWarmupTicks` gate that applies to all operational-phase boots.

Replaces the following description in baseline **§IPI Paths**:

- Baseline "broadcastPreemptIPI(): iterates numCoresOnline, skips self, skips APs with APICID=0 (except BSP), sends targeted preempt IPI; falls back to shorthand broadcast if no target was sent." — the shorthand fallback is **gone**. The current implementation never broadcasts via shorthand; it builds a target snapshot each tick and sends targeted IPIs, plus an unconditional BSP self IPI (`lapicSendSelfIPI`).

## Open Questions / Known Gaps

- In observed SMP runs, `smpprobe` workers can still all report `cpuID=0` even with `preemptPhaseOperational` reached and the preempt IPI targets snapped correctly. See `smp_preempt_problem/README.md §3` — current hypothesis is that the failure is post-shell runtime/scheduling-side (work-stealing not effective) rather than IPI delivery. The AP-scheduler-entered counter confirms APs reach `apSchedulerEntry`, so `preemptPhase` is reaching `Operational`.
- The investigation snapshot `252a96b` added the `APIDSTAT` / `PRESTAT` diagnostics used during `processExit`; those survive today but the root cause they were introduced to find is still open.
- `preemptTargetSnapshotN` is per-call and overwritten every tick. A racing reader during a tick sees a torn snapshot; this is fine for the diagnostic path (`dumpPreemptCounters`), but if any future code tries to consume the snapshot for policy it will need explicit synchronization.
- AP LAPIC timer enablement (baseline's §Known Unstable/Deferred SMP Surfaces) still deferred; the hypothetical AP-preempt path remains unused by this phase gate.
