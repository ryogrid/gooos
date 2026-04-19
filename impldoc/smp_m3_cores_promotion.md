# M3 — `scheduler=cores` Promotion + `stealWork()` Wire-up

**Scope.** Flip `src/target.json` from `scheduler=tasks` to `scheduler=cores`, add the Wave 2 patch hunks that make the cores-mode build link cleanly, and wire the existing `stealWork()` helper into the scheduler's pop site so kernel and Ring-3 goroutines actually execute on APs. **Depends on M4** (AP Ring-3 `iretq` triple-fault) being resolved first — or on a documented kernel-only affinity fallback (§6). **Does not** cover the M4 investigation itself or the M2 AP LAPIC timer race; those are sibling docs.

**Cross-links.**
- Deferred-item charter: `TODO_SMP3.md §"Deferred further"` item 2.
- Upstream-assessment anchors: `impldoc/tinygo_0_40_1_assessment.md §4.2` (atomicsLock as spinLock), `§4.3` (futex upstream-defined, supply variable only), `§5.1` (task_stack_amd64.go widening + `systemStackPtr` linkname).
- Patch-plan anchors: `impldoc/runtime_patches.md §3.3` (task_stack_amd64 Wave 2 redesign), `§3.8` (runtime_gooos.go Wave 2 variable declarations), `§3.10` (scheduler_cores.go push-site retargeting).
- Scheduler composition: `impldoc/smp_scheduler_design.md §2` (component map), `§3` (numCPU dimensioning), `§5` (AP scheduler entry), `§6` (work stealing), `§7` (GC composition).
- Toolchain commit plan source: `impldoc/toolchain_switch_plan.md §3` commits 7-9.
- Milestone entry: `impldoc/smp_unblock_overview.md`.
- Unified schedule: `impldoc/smp_unblock_milestones_and_verification.md §M3`.
- Rollback source: `impldoc/rollback_plan.md §4`.

---

## 1. Current State

The Wave 1 migration (branch `smp-take3`, commits `1de2050..2a1a13d`) landed:

- `src/target.json:9` = `"scheduler": "tasks"` (unchanged from 0.33.0).
- Patched `scheduler_cooperative.go` (Wave 1 scheduler file, `~/.local/tinygo0.40.1/src/runtime/`) with `runqueues[17]`, `schedLock`, `stealWork()`, `apScheduler()` — **but the `stealWork()` call is intentionally not wired** into the scheduler's pop site (commit `d0cba8e fix(smp): disable stealWork call under Wave 1 tasks mode`, which left a comment block at `scheduler_cooperative.go:248-254`).
- Patched `task_stack_amd64.go` build-tag: `//go:build scheduler.tasks && amd64 && !windows` — does NOT compile under cores mode.
- No `numCPU` constant for x86_64. No gooos-local `spinLock` declarations for `atomicsLock` / `schedulerLock` / `futexLock` / `printLock`.
- `impldoc/tinygo_0_40_1_assessment.md §4.3` records: upstream `scheduler_cores.go:268-277` defines `lockFutex` / `unlockFutex`, and `scheduler_cores.go:281-290` defines `lockAtomics` / `unlockAtomics`; gooos must NOT redefine them, only provide the variable bindings.

Result: `smpprobe` today reports `cpuID=0` for every worker because all goroutines execute on BSP. The user confirmed this behaviour in the previous session.

## 2. Dependency Order & Fallback

**Primary dependency.** M3 requires M4 resolved. If `stealWork()` is wired while the AP Ring-3 `iretq` triple-fault is unfixed, every boot under `-smp N` with a user shell (default) crashes as soon as the BSP pushes the `ring3Wrapper` onto the runqueue and an AP steals it — reproduced during the 0.40.1 migration M1 verification (see `impldoc/smp_m4_ring3_fault.md §1`).

**Fallback (kernel-only affinity).** If M4 stalls but the project still wants M3 value (kernel goroutines distributed, Ring-3 on BSP), ship a **stealWork affinity variant** that excludes `ring3Wrapper` goroutines from stealing:

- Add a `task.Task` flag (or a new field on gooos-side `gInfo` if the task struct is off-limits) marking "this is a Ring-3 wrapper; pin to BSP".
- `stealWork()` in `scheduler_cores.go` iterates peer runqueues' tails (reuse the existing `PopTail` approach from `impldoc/smp_kernel_scheduler.md §5`) but skips tasks whose `homeCPU` hint is 0 (= BSP) and whose "ring3 wrapper" flag is set.
- `elfSpawn` / `process.go:ring3Wrapper` set the flag on the wrapper goroutine it spawns.

Fallback cost: ~40 lines of patch (flag field, set site, skip site in stealWork). Fallback benefit: partial M3 without waiting on M4.

**Decision at M3 entry:** pick primary OR fallback based on M4 status. If M4 is already landed, do primary. Otherwise, **ask the user** (do not silently switch paths).

## 3. Commit-per-edit Plan

One `build(…)`/`fix(smp)` commit per item. Match `scope(subsys): …` subject style visible in `git log --oneline master..smp-take3`.

| # | Subject | Files |
|---|---|---|
| 1 | `build(toolchain): add Wave 2 runtime variable declarations (numCPU, spinlocks)` | patched `runtime_gooos.go` + regenerated `scripts/tinygo_runtime.patch` |
| 2 | `build(toolchain): widen task_stack_amd64.go build tag + consume upstream systemStackPtr` | patched `task_stack_amd64.go` + regenerated patch |
| 3 | `build(toolchain): Wave 2 scheduler_cores.go hunks — push-site retargeting + stealWork + apScheduler` | patched `scheduler_cores.go` + regenerated patch |
| 4 | `build(target): flip scheduler to cores` | `src/target.json:9` |
| 5 | `build(toolchain): patch script Wave 2 post-conditions` | `scripts/patch_tinygo_runtime.sh` |
| 6 | `fix(smp): wire stealWork call into scheduler pop site (requires M4 resolved)` | patched `scheduler_cores.go` |
| 7 | `test(smp): add scripts/test_smp_basic.sh — kernel goroutine distribution probe` | `scripts/test_smp_basic.sh` + boot-time probe in `src/main.go` gated by `const runSmpBasicProbe = true` |
| 8 | `docs(smp): M3 resolution — update deferred/known issues + TODO_SMP3` | `impldoc/smp_deferred_and_known_issues.md`, `TODO_SMP3.md` |

Commits 1-3 land the infrastructure; commits 4-5 flip the build; commit 6 is the gated "goes live" change; commit 7 is verification. Commits 1-5 should leave the tree buildable at every step (even if the build does nothing useful until commit 6).

---

## 4. Per-File Edits

### 4.1 Patched `runtime_gooos.go` (commit #1) — per `impldoc/runtime_patches.md §3.8`

Add at package scope:

```go
// Match gooos's maxCPUs (1 BSP + 16 APs). Per impldoc/smp_percpu_and_sync.md §1.3.
const numCPU = 17

// gooos SMP spinlock type. Mirrors the RP2 pattern in
// runtime_rp2.go:295-298. Under scheduler=cores, upstream's
// lockAtomics / lockScheduler / lockFutex / printlock paths
// (defined in scheduler_cores.go:260-290 and :306-315) call
// the Lock/Unlock methods below on these variables.
type gooosSpinLock struct {
    locked uint32
}

//go:linkname gooos_spinlockAcquire spinlockAcquire
func gooos_spinlockAcquire(lock *uint32)

//go:linkname gooos_spinlockRelease spinlockRelease
func gooos_spinlockRelease(lock *uint32)

//go:nosplit
func (l *gooosSpinLock) Lock()   { gooos_spinlockAcquire(&l.locked) }

//go:nosplit
func (l *gooosSpinLock) Unlock() { gooos_spinlockRelease(&l.locked) }

// Runtime-lock variables consumed by upstream lockFutex/unlockFutex
// (scheduler_cores.go:268-277), lockAtomics/unlockAtomics
// (scheduler_cores.go:281-290), and lockScheduler/unlockScheduler
// (scheduler_cores.go:260-266). printLock is consumed by
// scheduler_cores.go:306-327 printlock()/printunlock().
// DO NOT redefine those functions here; only supply the variables.
var (
    printLock     gooosSpinLock
    schedulerLock gooosSpinLock
    atomicsLock   gooosSpinLock
    futexLock     gooosSpinLock
)

// currentCPU linkname target. Calls through to gooos cpuID().
// The kernel's cpuID symbol is already consumed by task_stack_amd64.go:17-18
// under the name gooosCpuID; reuse that import pattern here to avoid
// two differently-named extern declarations of the same underlying symbol
// across runtime/ and internal/task/.
//
//go:linkname gooosCpuID cpuID
func gooosCpuID() uint32

//go:linkname currentCPU runtime.currentCPU
//go:nosplit
func currentCPU() uint32 { return gooosCpuID() }

// gcPauseCore stub for M3. Real IPI-based implementation lands
// at M5 per impldoc/smp_migration_overview.md §5 milestone map.
// Under scheduler=cores, upstream gc_stack_cores.go:16-80
// gcMarkReachable() calls this once per peer core before scanning.
// Returning immediately is safe at M3 because the M5 stop-the-
// world is a correctness-under-allocation-stress issue; a short
// GC window without pausing peers may miss live roots but the
// existing test matrix does not exercise heavy concurrent allocation.
//
//go:linkname gcPauseCore runtime.gcPauseCore
//go:nosplit
func gcPauseCore(cpu uint32) { /* M3 stub; M5 replaces with IPI send + ack spin */ }
```

**Do NOT** add bodies for `lockFutex`, `unlockFutex`, `lockAtomics`, `unlockAtomics`, `lockScheduler`, `unlockScheduler` — upstream `scheduler_cores.go:268-277` and `:281-290` already defines them. Duplicate-symbol link error guaranteed otherwise (verified pattern during the 0.40.1 migration: `CRITICAL` reviewer finding #1 in `impldoc/smp_migration_overview.md §Reviewer findings`).

### 4.2 Patched `task_stack_amd64.go` (commit #2) — per `impldoc/runtime_patches.md §3.3`

Widen the build tag:

```go
// Before:
//go:build scheduler.tasks && amd64 && !windows

// After:
//go:build (scheduler.tasks || scheduler.cores) && amd64 && !windows
```

Retire gooos's own `systemStacks [numCPU]uintptr` array. Import upstream's accessor via linkname (mirror of `task_stack_tinygoriscv.go:12-13`):

```go
//go:linkname runtime_systemStackPtr runtime.systemStackPtr
func runtime_systemStackPtr() *uintptr
```

Rewrite `resume()` / `pause()` / `SystemStack()` to consume the linkname:

```go
func (s *state) resume() {
    gooosOnResume() // keep the TSS.RSP0 update
    swapTask(s.sp, runtime_systemStackPtr())
}

func (s *state) pause() {
    systemStackPtr := runtime_systemStackPtr()
    newStack := *systemStackPtr
    *systemStackPtr = 0
    swapTask(newStack, &s.sp)
}

func SystemStack() uintptr {
    return *runtime_systemStackPtr()
}
```

Under `scheduler=tasks` the `systemStackPtr` linkname resolves to `runtime.systemStackPtr` which `~/.local/tinygo0.40.1/src/runtime/scheduler_tasks.go:11` provides (scalar `&systemStack`) under tasks mode, and `scheduler_cores.go:298-300` provides (array-indexed `&systemStack[currentCPU()]`) under cores mode. **Both files expose the accessor** — verified via `grep -n 'func systemStackPtr' ~/.local/tinygo0.40.1/src/runtime/scheduler_tasks.go` before writing this doc. So the commit-2 widening is safe: the linkname resolves in both modes. The risk R-systemStackPtr-tasks flagged in the 0.40.1-migration review turn is therefore **design-eliminated** for M3; the risk row is kept in §8 for archival reference but marked "(resolved)".

### 4.3 Patched `scheduler_cores.go` (commit #3) — per `impldoc/runtime_patches.md §3.10`

Current upstream `scheduler_cores.go` key sites (verified anchors, 0.40.1):

| Site | Upstream file:line | Purpose |
|---|---|---|
| `scheduleTask(t)` pushes to single global `runqueue` | `scheduler_cores.go:37` | gooos retargets to `runqueues[gooosCpuID()].Push(t)` |
| `Gosched()` pushes `task.Current()` to single global `runqueue` | `scheduler_cores.go:87` | gooos retargets identically |
| `currentTask()` accessor | `scheduler_cores.go:253` | no retarget needed; upstream uses `cpuTasks[currentCPU()]` |
| `systemStack [numCPU]uintptr` / `systemStackPtr()` | `scheduler_cores.go:292, 298-300` | upstream, gooos consumes via linkname |

Retarget hunks (to be added to the patch set):

- Near `scheduler_cores.go:26` `var runqueue task.Queue` — alongside it add `var runqueues [numCPU]task.Queue`. Do NOT remove the upstream `runqueue` (other push sites may exist in the main scheduler loop); it remains as a secondary queue if the gooos patch ever loses coverage. gooos's retargeting hunk is additive: every `runqueue.Push(...)` that gooos cares about becomes `runqueues[gooosCpuID()].Push(...)`.
- `scheduler_cores.go:37-57` `scheduleTask` body: `runqueue.Push(t)` → `runqueues[gooosCpuID()].Push(t)`.
- `scheduler_cores.go:87-91` `Gosched` body: `runqueue.Push(task.Current())` → `runqueues[gooosCpuID()].Push(task.Current())`.
- Add `stealWork()` and `apScheduler()` helpers (same shape as Wave 1's scheduler_cooperative.go versions; verify anchors at `~/.local/tinygo0.40.1/src/runtime/scheduler_cooperative.go:173-194` — copy those function bodies with the scheduler-cores file's local variable names).
- Locate the main `scheduler()` pop path in `scheduler_cores.go` (search for `runqueue.Pop()` via `grep -n 'runqueue.Pop' scheduler_cores.go`). Retarget to `runqueues[gooosCpuID()].Pop()`.

**Commit #6** (the follow-on) wires `stealWork()` into the pop-on-nil path:

```go
t := runqueues[gooosCpuID()].Pop()
if t == nil {
    t = stealWork()
}
```

This is the single line that commit `d0cba8e` intentionally disabled in the Wave 1 tasks-mode file. In the cores-mode file it lands fresh.

### 4.4 `src/target.json` (commit #4)

Single line:

```json
    "scheduler": "tasks"
```

becomes:

```json
    "scheduler": "cores"
```

At commit #4 the build flips to cores mode. Commits 1-3 must already be in place, else the build fails with missing symbols.

### 4.5 `scripts/patch_tinygo_runtime.sh` (commit #5)

Update the idempotency grep post-conditions to probe for the new symbols:

- `runtime_gooos.go` must contain `numCPU = 17`, `atomicsLock`, `schedulerLock`, `futexLock`, `printLock`, `gcPauseCore`, `currentCPU`.
- `scheduler_cores.go` must contain `runqueues`, `stealWork`, `apScheduler`.
- `task_stack_amd64.go` build tag must contain `scheduler.cores`.

Additionally, the current Wave 1 script probes `scheduler_cooperative.go` for `runqueues`/`stealWork`/`apScheduler` — those probes stay because scheduler_cooperative.go is still compiled-and-patched under `scheduler.asyncify || scheduler.tasks` (per its build tag); dropping them breaks Wave 1 bisects.

### 4.6 `src/main.go` (commit #7) + `scripts/test_smp_basic.sh`

Boot-time probe (gated by `const runSmpBasicProbe = true`, off in release):

```go
// M3 probe: spawn N kernel-side goroutines, each prints its
// cpuID and increments a shared counter. After 500 ms verify
// at least 2 distinct cpuIDs were observed.
```

Emit `"smp_basic: PASS distinct=N cpuids=X,Y,Z"` on success; harness `scripts/test_smp_basic.sh` greps for `distinct=` and asserts value ≥ 2.

---

## 5. Entry Criteria

- M4 has landed (AP Ring-3 `iretq` triple-fault resolved per `impldoc/smp_m4_ring3_fault.md`), OR the fallback kernel-only-affinity path is explicitly chosen and the user has confirmed.
- Wave 1 state (`smp-take3` branch, tip ≥ `2a1a13d`) is present; `make build`/`make lint`/`make verify-globals` all clean under `scheduler=tasks`.
- `atomicsLock`-recursion smoke observation: boot under `-smp 4` does not hang before the shell prompt under the cores-mode build. (The original "M1 smoke probe" was a manual boot-log check, not a dedicated harness; if the user wants it formalised, add a boot-time `atomic.AddUint32` + `CompareAndSwap` probe in `src/main.go` gated by `const runAtomicsProbe = true`, and grep serial log for the expected output.)

## 6. Exit Criteria

- `scripts/test_smp_basic.sh` — PASS with ≥ 2 distinct cpuIDs under `-smp 4`.
- Full regression matrix under `-smp 4`:
  - `bash scripts/test_net.sh` → PASS.
  - `bash scripts/test_tcp_phase{1..5}.sh` → PASS.
  - `bash scripts/test_gochan.sh` → PASS.
  - `bash scripts/test_goprobe.sh` → PASS.
  - `bash scripts/test_sendkey.sh 1` (Ring-3, requires M4) → PASS.
- `make build && make lint && make verify-globals` clean.
- `bash scripts/patch_tinygo_runtime.sh` idempotent (re-run prints `already-applied:`).
- `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/` diff against pre-M3 baseline: zero new markers.
- `smpprobe` from the shell reports worker goroutines on ≥ 2 cpuIDs.

---

## 7. Rollback

Per `impldoc/rollback_plan.md §4` (Wave 2 rollback):

1. `git revert` commits 6, 5, 4, 3, 2, 1 in reverse order (Wave 2 stack).
2. `bash scripts/patch_tinygo_runtime.sh` — regenerate Wave 1 tree.
3. `make clean && make build` — verify Wave 1 rebuilds.
4. Append observation to `impldoc/smp_deferred_and_known_issues.md §5` under a new "M3 attempt N" subsection.

If only commit #6 regresses (stealWork wire-up triggers a crash that `scripts/test_smp_basic.sh` doesn't catch but a shell session does), revert **just commit #6**. Commits 1-5 then stay — the tree is on cores mode but without live stealing, which is a valid intermediate state for M5 to build on.

---

## 8. Risks

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R-systemStackPtr-tasks (**resolved**) | `scheduler_tasks.go` under tasks mode does not expose `systemStackPtr()` — commit #2 build-tag widening breaks tasks-mode builds | — | — | **Resolved at write-time**: `~/.local/tinygo0.40.1/src/runtime/scheduler_tasks.go:11` does expose `systemStackPtr() *uintptr { return &systemStack }`. Commit #2 widening is safe under both `scheduler.tasks` and `scheduler.cores`. Row retained for archival only. |
| R-scheduler-cores-other-push-sites | `scheduler_cores.go` has push sites beyond lines 37 and 87 that gooos misses | Low | High | `grep -n 'runqueue.Push' scheduler_cores.go` at M3 start; add retarget hunks for every hit. |
| R-atomicsLock-recursion | gooos's spinlock variable declarations somehow re-trigger the recursion path that the assessment doc design-eliminated | Low | High | M1 atomics smoke probe reused as M3 entry gate (see §5). |
| R-cores-chan-push | `chan.go` under cores mode pushes via `scheduleTask` which my retarget covers; but if there are direct `runqueue.Push` calls in chan.go they miss routing | Low | Medium | `grep -n 'runqueue' chan.go` at M3 start. |
| R-numcpu-ceiling | `numCPU = 17` hard-limits to 16 APs | Accepted | Low | Matches existing gooos `maxCPUs`; bump requires coordinated patch. |
| R-m4-regression | M3 ships before M4 is truly fixed; Ring-3 shell triple-faults on boot | Medium (if M4 skipped) | High | §2 dependency check; fallback affinity path if M4 stalls. |

---

## 9. Deliverables

1. Wave 2 patch hunks applied to `~/.local/tinygo0.40.1/src/` via the refreshed `scripts/tinygo_runtime.patch`.
2. `src/target.json:9` = `"scheduler": "cores"`.
3. Live `stealWork()` call in `scheduler_cores.go` main-loop pop path.
4. `scripts/test_smp_basic.sh` PASS; regression matrix green under `-smp 4`.
5. `impldoc/smp_deferred_and_known_issues.md` §2 + §5 updated; `TODO_SMP3.md` M3 items ticked.
6. No `git push`; no branch ops; no `master` merge without explicit user instruction.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
