# DEFERRED 2 — `elfSpawn` round-robin distribution for `ring3Wrapper` goroutines (B1)

## Scope & goal

**Scope (verbatim from `FINAL_REPORT.md §Deferred` item 2)**:
*`elfSpawn` round-robin distribution for `ring3Wrapper` goroutines
(the smpprobe-workers-all-on-cpuID=0 symptom).*

**Goal**: when a user process spawns N children via `sys_spawn`,
each child's `ring3Wrapper` goroutine is placed on a different
target CPU's runqueue so under `-smp M` the workers are observed
across ≥`min(N, M)` distinct cpuIDs. After this lands,
`scripts/test_smp_shell_smpprobe.sh` and the distribution
portion of `scripts/test_smp_shell_preempt.sh` become
deterministic (today they are flaky because BSP finishes short
workers before `stealWork` visits BSP's queue).

## Root-cause analysis

Current spawn path:

1. `sys_spawn` handler in `src/userspace.go` calls `elfSpawn`
   (`src/process.go:elfSpawn`).
2. `elfSpawn` finishes with `go ring3Wrapper(child)`
   (`src/process.go:410`-ish).
3. `go` statement is lowered to TinyGo's
   `runtime.scheduleTask(t)`.
4. Post-patch `scheduleTask` for `scheduler=cores`
   (`scripts/tinygo_runtime.patch:997–1007`) does
   `runqueues[gooosCpuID()].Push(t)` followed by `schedulerWake()`.
5. "Current CPU" at spawn time is the caller — the shell's
   `ring3Wrapper` — usually BSP.

Consequence: all N worker wrappers land on BSP's runqueue. BSP's
scheduler pops them sequentially; each worker's first observable
action (the `cpuID=` print in `user/cmd/smpprobe/main.go`) runs
before any yield, so each worker prints the CPU it was first
popped on — BSP (`cpuID=0`) almost every time. `stealWork` from
an AP does happen, but by the time an AP wakes, BSP has already
run several workers to completion.

Evidence: traces in prior sessions recorded all four workers
printing `cpuID=0` despite `-smp 4` + `preemptPhaseOperational`
reached (see `smp_preempt_problem/README.md §Confirmed Current
Status`).

`stealWork` is live in `scheduler=cores` mode
(`scripts/tinygo_runtime.patch:1088–1091`) so distribution would
*eventually* happen; the problem is it's too late for short-lived
or observation-at-entry workloads.

## Design approach

**Push each new `ring3Wrapper` directly onto a target CPU's
runqueue using a round-robin counter**, bypassing the implicit
"push onto current CPU" behaviour of `scheduleTask`.

### Building block: add `runqueuePushTo` to scheduler_cores AND expose it

`runtime.runqueuePushTo(t *task.Task, cpuIdx uint32)` is defined
in the patch at `scripts/tinygo_runtime.patch:821–828` **only
inside the `scheduler_cooperative.go` hunk**. The kernel's
`src/target.json:9` sets `"scheduler": "cores"`, so only the
`scheduler_cores.go` file is compiled in. The cores hunk
(starting at patch line ~975) has **no `runqueuePushTo`
definition** today. A linkname targeting the symbol would fail
at link time.

Two patch additions are therefore required:

1. **Add a mirror definition inside the `scheduler_cores.go`
   hunk** of `scripts/tinygo_runtime.patch`. Place it right
   after the existing `stealWork` block (~patch line 1044). The
   body should call `schedulerWake()` itself so a single
   responsibility lives in one place — see the design-decision
   note below.
2. **No `//go:linkname` in the runtime patch is needed** for
   the symbol; the gooos side links to it via a fresh
   `//go:linkname` declaration in `src/goroutine_tss.go`.

Add a new linkname bridge in `src/goroutine_tss.go` alongside
the existing `taskCurrent` / `taskPause` bridges:

```go
//go:linkname runqueuePushTo runtime.runqueuePushTo
func runqueuePushTo(t uintptr, cpuIdx uint32)
```

**Design decision on wake-up**: the new `runqueuePushTo` body
inside the `scheduler_cores.go` hunk **calls `schedulerWake()`
itself** (mirroring how `scheduleTask` at patch line 1007 wakes
after a push). Single responsibility lives inside the runtime;
the gooos caller does NOT need an explicit
`gooosWakeupCPU(target)` call afterwards. (If a future TinyGo
version moves the wake elsewhere, the gooos side gets one
linkname-load failure at build, which is the right kind of
breakage.)

### Spawn-side: round-robin counter + migration bootstrap

The cleanest implementation avoids touching `scheduleTask` itself;
instead, `elfSpawn` spawns a tiny bootstrap goroutine whose first
act is to migrate itself and then call `ring3Wrapper`. The
critical hazard to handle is the **push-vs-pause race**: between
`runqueuePushTo(self, target)` and the local `taskPause()`, the
target CPU's scheduler can `Pop()` the task and call
`task.Resume` on it — but at that instant the source CPU is still
running the same goroutine with `RunState == Running`. Two
concurrent CPUs end up touching the same `task.Task` state.

`Gosched` at `scripts/tinygo_runtime.patch:1010–1015` does not
hit this race because it (a) holds `schedulerLock` across the
push+pause and (b) calls the lock-aware `task.PauseLocked()`.
Plan-02 must replicate that invariant.

Two equivalent strategies; **strategy A is recommended** because
it adds the smallest patch surface:

**Strategy A — set `RunState = Paused` BEFORE the cross-CPU
push.** Add a tiny new linkname-bridged helper to the runtime
patch that does the atomic state change + push under
`schedulerLock`:

```go
// In scheduler_cores.go hunk of scripts/tinygo_runtime.patch,
// next to runqueuePushTo:

// migrateAndPause atomically sets the current task to Paused,
// pushes it to target CPU's queue, wakes target, then unlocks
// and pauses. Mirrors Gosched's lock discipline but for a
// non-self-CPU target.
func migrateAndPause(targetCpu uint32) {
    schedulerLock.Lock()
    t := task.Current()
    runqueues[targetCpu].Push(t)
    schedulerWake()
    task.PauseLocked()  // unlocks schedulerLock and pauses
}
```

Then the gooos side just calls:

```go
//go:linkname migrateAndPause runtime.migrateAndPause
func migrateAndPause(targetCpu uint32)

func scheduleRing3Wrapper(proc *Process) {
    n := uint32(numCoresOnline)
    if n == 0 {
        n = 1
    }
    target := (ring3SpawnCounterAdd1() - 1) % n
    go func() {
        if target != cpuID() {
            migrateAndPause(target)
        }
        ring3Wrapper(proc)
    }()
}

func ring3SpawnCounterAdd1() uint32 {
    ring3SpawnCounter++
    return ring3SpawnCounter
}
```

**Strategy B (rejected, kept for record)**: do the push+pause
purely on the gooos side using a local `runqueuePushTo` linkname
+ `taskPause`. Rejected because `taskPause` does not hold
`schedulerLock`, leaving the same race window the reviewer flagged.

Observability: after `migrateAndPause(target)` returns (we
resumed on target CPU), the bootstrap goroutine continues with
`ring3Wrapper(proc)` on that CPU. `gooosOnResume` already
handles per-CPU TSS.RSP0 / CR3 fixups during the resume.

### Replacement at the `elfSpawn` call site

Replace the existing `go ring3Wrapper(child)` at
`src/process.go:elfSpawn` with `scheduleRing3Wrapper(child)`.
Also do the same replacement in `src/elf.go:elfLoad` (boot-shell
path); round-robin doesn't really matter for the single boot
shell but consistency keeps the code simple (the counter starts
at 0 so the shell still lands on BSP).

### Lock-rank and ISR safety

- `scheduleRing3Wrapper` is called from syscall context
  (`sys_spawn`) which already holds interrupts-enabled +
  `procLock` released; no lock-rank impact.
- The inner `go func()` bootstrap runs on the current CPU, then
  calls `runqueuePushTo` + `taskPause`. `runqueuePushTo` acquires
  the target queue's spinlock internally (patched
  `task.Queue.Push`, rank documented implicitly as queue-local
  rank 15). `taskPause` does not take any gooos lock.
- No interaction with `gooosOnResume` changes: when the target
  CPU resumes the bootstrapped goroutine, `gooosOnResume` runs
  on the target — *correct* behaviour, exactly as for any cross-
  CPU task wake.
- No ISR-context callers.

### Interaction with existing gooos hooks

- `gooosWakeupCPU(target)` is the sole new IPI: it guarantees an
  idle target AP gets woken from `hlt` if it was sleeping at the
  moment we pushed.
- The existing `gooosOnResume` hook already handles per-CPU
  TSS.RSP0 / CR3 updates when the bootstrap goroutine resumes
  elsewhere — no change needed.
- Preempt phase gate is orthogonal; this work changes *which
  CPU* a wrapper starts on, not whether it can be preempted.

## File / symbol touch-points

| File | Status | Purpose |
|---|---|---|
| `scripts/tinygo_runtime.patch` | **Modify** | Add `runqueuePushTo` and `migrateAndPause` to the `scheduler_cores.go` hunk (after the existing `stealWork` block, ~patch line 1044). |
| `src/goroutine_tss.go` | Modify | Add `//go:linkname migrateAndPause runtime.migrateAndPause` declaration. (Optional: also linkname `runqueuePushTo` if a future caller needs the bare push.) |
| `src/process.go` | Modify | Add `ring3SpawnCounter` var; add `scheduleRing3Wrapper` helper; change `elfSpawn` to call it (replaces `go ring3Wrapper(child)` at `src/process.go:415`). |
| `src/elf.go` | Modify | Change `elfLoad` Ring-3 launch path to `scheduleRing3Wrapper` (replaces `go ring3Wrapper(proc)` at `src/elf.go:250`) for consistency. |
| `current_impl_2026_04_24/03_smp_preempt_phase_gating.md` | Doc update | Close the "smpprobe all-on-cpuID=0" open question. |
| `current_impl_2026_04_24/FINAL_REPORT.md` | Doc update | Remove DEFERRED item 2. |

### Ambiguity flags for the implementer

- **Exact sig of `runqueuePushTo` / `migrateAndPause`**: the patch uses
  `runqueuePushTo(t *task.Task, cpuIdx uint32)`. On the gooos
  side `taskCurrent()` returns `uintptr` (`src/goroutine_tss.go:55`).
  Because `migrateAndPause` does not pass a task pointer across
  the linkname (it reads `task.Current()` inside the runtime),
  the gooos side need only declare it as
  `func migrateAndPause(targetCpu uint32)` — no ABI gymnastics.

## TinyGo runtime patch changes

**Required.** Two additions inside the
`scheduler_cores.go` hunk of `scripts/tinygo_runtime.patch`,
placed right after the existing `stealWork` block (`~patch
line 1044`):

1. `func runqueuePushTo(t *task.Task, cpuIdx uint32)` — same
   bounds-checking + push as the cooperative version at patch
   line 823, and additionally calls `schedulerWake()` so the
   target AP wakes from `hlt` if it was idle.
2. `func migrateAndPause(targetCpu uint32)` — `Lock` →
   `Push(task.Current(), targetCpu)` → `schedulerWake()` →
   `task.PauseLocked()`. Mirrors the existing `Gosched`
   structure at patch line 1010–1015.

The patch addition is small (≤ 20 lines) and structured exactly
like the existing gooos blocks so a future TinyGo upgrade
re-applies cleanly.

## Acceptance criteria

1. `make build` + `make lint` + `make verify-globals` pass.
2. `scripts/test_smp_shell_smpprobe.sh` under `-smp 4`:
   - 4 workers observed on ≥ 2 distinct `cpuID=N` values over a
     single run.
   - ≥ 95 % pass rate across 20 boots (sampler).
3. `scripts/test_smp_shell_preempt.sh`: no regression (should
   improve from the current ~0-5 markers to the ≥ 5 PASS
   criterion once workers actually distribute).
4. `scripts/test_smp_shell_distribution.sh` continues to PASS.
5. No regression in `scripts/test_preempt_kernel.sh`,
   `scripts/test_net.sh`, `scripts/test_ps.sh`,
   `scripts/test_shell_background.sh`.

## Verification plan

```
make build
make lint
make verify-globals
make iso
bash scripts/test_smp_shell_smpprobe.sh
bash scripts/test_smp_shell_distribution.sh
bash scripts/test_smp_shell_preempt.sh
bash scripts/test_ps.sh
bash scripts/test_shell_background.sh
bash scripts/test_preempt_kernel.sh
bash scripts/test_preempt_user.sh
```

Then the sampling run:

```
bash scripts/test_smp_stability_sample.sh
```

Expected:

- First-run observation of all four `worker-N: cpuID=M`
  lines shows M ∈ {0,1,2,3} in at least 3 of 4 lines.
- Sampler reports `distribution_pass_rate >= 95 %`.

## Risk & rollback

| Risk | Impact | Mitigation |
|---|---|---|
| `runqueuePushTo` linkname signature drift | Compile error | Pin TinyGo version in Makefile; Phase-1 survey shows the signature stable since the gooos patch was written. |
| Target CPU not yet fully online when a worker is pushed | Worker sits on an unreached queue | Counter is `% numCoresOnline`; `numCoresOnline` is finalised well before any user `sys_spawn` reaches this path. |
| Round-robin is "too even" and ties up a preempt-pressured CPU | Minor fairness regression | Accept; alternative (least-loaded peek) is out of scope. |
| `runqueuePushTo` followed by a non-locking `taskPause` allows the target CPU to resume the task while the source CPU is still running it | Two CPUs touch the same `task.Task` simultaneously → silent state corruption | Use **strategy A above** (`migrateAndPause` holds `schedulerLock` across push + pause via `task.PauseLocked`), exactly mirroring `Gosched`'s discipline. The single-cpu `Gosched` path is provably race-free in the patched runtime; the cross-cpu variant inherits the same proof. |

**Rollback**: revert the `elfSpawn` / `elfLoad` change; the
linkname bridges can remain unused. One-commit revert.

## Dependencies

- **Does not depend on DEFERRED 1** (Phase 4.4). This is a
  pure `ring3Wrapper` spawn-side change and works on top of the
  existing TinyGo scheduler.
- DEFERRED 5 (harness re-gating) depends on this landing.

## Estimated effort

**Small.** ~30 LOC in `src/process.go` + 2 linkname bridges in
`src/goroutine_tss.go` + the call-site edits in `src/elf.go`.
One focused session including sampler verification.
