# TinyGo Runtime Patch Rebase — 0.33.0 → 0.40.1

**Scope.** Per-file plan for rebasing `scripts/tinygo_runtime.patch` (currently 853 lines against TinyGo 0.33.0) onto TinyGo 0.40.1. Each file entry covers: source path in the 0.40.1 tree, purpose of the hunk set, diff-approach outline, upstream-PR feasibility, and any file-split relocations introduced by 0.40.0's source reorganization.

**Cross-links.**
- Assessment + file-layout evidence: `impldoc/tinygo_0_40_1_assessment.md`
- Scheduler design that shapes these hunks: `impldoc/smp_scheduler_design.md`
- Apply/revert mechanics: `impldoc/toolchain_switch_plan.md`
- Milestone gates that validate each rebase: `impldoc/smp_milestones_and_verification.md`

---

## 1. Current State (0.33.0, reference)

`scripts/tinygo_runtime.patch` at commit `7e1846a` contains 12 file regions totalling 853 lines, applied against `$HOME/.local/tinygo/src/`. Enumerated from the patch's `--- a/ / +++ b/` boundaries:

| # | File (0.33.0 path) | Patch line range | Kind |
|---|---|---|---|
| 1 | `src/internal/task/queue.go` | 3–95 | modify |
| 2 | `src/internal/task/task_stack.go` | 96–176 | modify |
| 3 | `src/internal/task/task_stack_amd64.go` | 177–231 | modify |
| 4 | `src/runtime/chan.go` | 232–257 | modify |
| 5 | `src/runtime/gc_blocks.go` | 258–361 | modify |
| 6 | `src/runtime/interrupt/interrupt_gooos.go` | 362–400 | new |
| 7 | `src/runtime/interrupt/interrupt_gooos_user.go` | 401–420 | new |
| 8 | `src/runtime/runtime_gooos.go` | 421–517 | new |
| 9 | `src/runtime/runtime_gooos_user.go` | 518–605 | new |
| 10 | `src/runtime/scheduler.go` | 606–827 | modify |
| 11 | `src/runtime/wait_gooos.go` | 828–845 | new |
| 12 | `src/runtime/wait_other.go` | 846–853 | modify |

Apply machinery: `scripts/patch_tinygo_runtime.sh` (idempotent, grep-based post-condition).

---

## 2. Migration Strategy

**Two waves aligned to milestones** (see `impldoc/smp_milestones_and_verification.md`):

- **Wave 1 (M0/M1): tasks-mode rebase.** Keep `"scheduler": "tasks"`. Retarget hunks from `runtime/scheduler.go` → `runtime/scheduler_cooperative.go`. All other files retain their paths. This is a mechanical rebase; semantics unchanged from 0.33.0.
- **Wave 2 (M3): cores-mode promotion.** Flip `"scheduler": "cores"`. Retarget scheduler hunks → `runtime/scheduler_cores.go`. Introduce new hunks for `lockFutex`/`unlockFutex`/`gcPauseCore` bodies. Drop hunks rendered redundant by upstream (e.g., `chan.go` per-CPU routing if cores chan.go already does this).

Each wave is one PR-sized unit for local bisectability; neither wave is a "big bang".

---

## 3. Per-File Rebase Plan

### 3.1 `src/internal/task/queue.go` (Wave 1 — path unchanged)

**Purpose.** gooos adds a per-Queue spinlock field (`lock uint32`) and wraps `Push`/`Pop`/`Append`/`Empty` with `gooos_spinlockAcquire/Release` for cross-CPU safety under `scheduler=tasks`. Adds `PopTail()` for work stealing.

**0.40.1 delta.** Upstream 0.40.1 `queue.go:14-18` already wraps `Push` with `lockAtomics()/unlockAtomics()` — but `lockAtomics` under `scheduler=tasks` is only `interrupt.Disable()`, which is per-CPU. gooos's explicit spinlock is still required for `scheduler=tasks` correctness under SMP.

**Rebase approach.**
1. Apply existing hunk against 0.40.1 `queue.go`. Expect ~3-line context drift; `patch -p1 --forward` handles it.
2. Retain all spinlock calls. Retain `PopTail()` addition.
3. At Wave 2 (cores promotion): the `lockAtomics()` upstream calls provide cross-core safety when cores mode is active. gooos's explicit spinlock becomes redundant; remove the spinlock field and shim calls. This is an **opt-in simplification**, not required for correctness on cores; the wrapper is cheap enough to retain for bisect safety. Decision: retain through Wave 2, schedule removal as a cleanup PR after M4.

**Upstream-PR feasibility.** Not applicable — gooos-specific (the upstream cores mode covers the same concern via `atomicsLock`).

### 3.2 `src/internal/task/task_stack.go` (Wave 1 — path unchanged)

**Purpose.** gooos adds `stackTop` field to `state` struct + `gooosStackOverflow()` hook called from `Pause()` on canary mismatch.

**0.40.1 delta.** Upstream `state` struct unchanged; no new fields. Confirmed absent `stackTop` in upstream (evidence in `impldoc/tinygo_0_40_1_assessment.md §5.2`).

**Rebase approach.** Apply hunk against 0.40.1 `task_stack.go` unchanged. Expect clean apply. The hook is a simple conditional call that upstream accepts via linkname; no upstream interaction.

**Upstream-PR feasibility.** Low value — gooos-specific diagnostic.

### 3.3 `src/internal/task/task_stack_amd64.go` (Wave 1 — path unchanged; Wave 2 — redesign to use upstream)

**Purpose (today, Wave 1).** gooos replaces `var systemStack uintptr` (singular) with `var systemStacks [numCPU]uintptr`, indexed by `cpuID()` in `resume()`/`pause()`/`SystemStack()`. Also adds `gooosOnResume()` call at the top of `resume()` to update per-CPU TSS.RSP0.

**0.40.1 delta.** The file build tag is **`//go:build scheduler.tasks && amd64 && !windows`** — under `scheduler=cores` on amd64, this file does not compile at all. Upstream has **no** amd64 cores task-stack binding; only `task_stack_tinygoriscv.go:12-72` demonstrates the cores pattern via `//go:linkname runtime_systemStackPtr runtime.systemStackPtr`. Upstream cores mode declares `var systemStack [numCPU]uintptr` at `scheduler_cores.go:292` with accessor `systemStackPtr()` at lines 298-300 (`impldoc/tinygo_0_40_1_assessment.md §5.1`).

**Rebase approach — Wave 1 (tasks mode).** Apply existing hunk against 0.40.1 `task_stack_amd64.go` unchanged. The build tag still matches because Wave 1 stays on `scheduler=tasks`. Expect clean apply.

**Rebase approach — Wave 2 (cores promotion). Redesign.**

1. Widen the build tag: `//go:build (scheduler.tasks || scheduler.cores) && amd64 && !windows`.
2. Retire gooos's own `systemStacks [numCPU]uintptr` declaration.
3. Import upstream's `systemStackPtr()` via linkname (mirror `task_stack_tinygoriscv.go:12-13`):
   ```go
   //go:linkname runtime_systemStackPtr runtime.systemStackPtr
   func runtime_systemStackPtr() *uintptr
   ```
4. Rewrite `resume()`/`pause()`/`SystemStack()` to consume `runtime_systemStackPtr()` — see `task_stack_tinygoriscv.go:59-72` for the shape.
5. Retain `gooosOnResume()` call at `resume()` entry.

**Alternative.** Keep gooos's existing per-CPU `systemStacks` array and accept duplication with upstream's `systemStack [numCPU]uintptr`. Acceptable but wasteful of ~136 bytes and divergent from upstream convention. Reject unless the linkname approach hits an unforeseen blocker at M3.

**Upstream-PR feasibility (Wave 2).** Medium-high — a true x86_64 cores task-stack binding is plausibly upstream-acceptable because it's symmetric with the existing RISC-V pattern. Candidate PR after M4 lands.

### 3.4 `src/runtime/chan.go` (Wave 1 — path unchanged; Wave 2 — retained)

**Purpose.** gooos currently patches `resumeRX`/`resumeTX` to push the woken task to `runqueues[gooosCpuID()]` instead of the global `runqueue`.

**0.40.1 delta under tasks mode.** Upstream `chan.go` pushes to a single global `runqueue` via `scheduleTask` (or equivalent). gooos hunk retargeted as-is.

**0.40.1 delta under cores mode.** Upstream `scheduler_cores.go:37-57 scheduleTask` still pushes to a single global `runqueue` (`scheduler_cores.go:26`) under `schedulerLock`. Per-CPU routing does **not** happen upstream. gooos's per-CPU routing hunks remain relevant. **Retain the hunk at both waves.**

**Rebase approach.**
- Wave 1: apply existing hunk. Expect up to 5-line context drift from 0.40.0 renames; resolve manually if `--forward` rejects.
- Wave 2: retain; same semantics under cores mode. The hunk may need to intercept `scheduleTask` calls rather than raw `runqueue.Push`; verify at M3 by grep.

**Upstream-PR feasibility.** Not applicable — gooos-specific routing (upstream is fine with a single global runqueue for RP2040/RP2350 scale).

### 3.5 `src/runtime/gc_blocks.go` (Wave 1 — path unchanged, Wave 2 — partial retirement)

**Purpose.** gooos adds `heapLock` spinlock around `alloc()` so multiple CPUs can allocate concurrently; also adds runqueue scan under GC mark (multi-CPU version iterates `runqueues[0..activeCPUs-1]`).

**0.40.1 delta.** Upstream `gc_blocks.go` exists in 0.40.1 but the multi-core stack-scan logic has been split into `gc_stack_cores.go` (`scheduler=cores` build tag). The runqueue-scan code in gooos's current hunk for `gc_blocks.go` overlaps with `gc_stack_cores.go:16-80` `gcMarkReachable` in cores mode.

**Rebase approach.**
- Wave 1: apply hunk against 0.40.1 `gc_blocks.go`. Heap-lock addition is orthogonal to the upstream split and stays.
- Wave 2: the gooos runqueue-scan portion becomes redundant under cores mode (`gc_stack_cores.go` already scans per-CPU). Split the existing hunk into (a) `heapLock` — permanent, (b) runqueue-scan — removed at Wave 2 in favour of supplying a `gcPauseCore()` body (see §3.13 new file `gc_hooks_gooos.go`).

**Upstream-PR feasibility.** `heapLock` is potentially upstream-acceptable as a general bare-metal SMP enabler; cost is low. Defer until after M3 proves the pattern.

### 3.6 `src/runtime/interrupt/interrupt_gooos.go` (Wave 1 — path unchanged, new file)

**Purpose.** Kernel-mode `interrupt.Disable/Restore/In` implementations.

**0.40.1 delta.** None — upstream does not ship an x86_64 bare-metal interrupt provider. gooos's new file slot is free.

**Rebase approach.** Apply verbatim. Expect clean apply.

**Upstream-PR feasibility.** Not applicable — target-specific.

### 3.7 `src/runtime/interrupt/interrupt_gooos_user.go` (Wave 1 — path unchanged, new file)

**Purpose.** Userspace-mode no-op interrupt stubs (Ring 3 doesn't have interrupt enable/disable).

**0.40.1 delta.** None.

**Rebase approach.** Apply verbatim. Clean apply expected.

### 3.8 `src/runtime/runtime_gooos.go` (Wave 1 — path unchanged; Wave 2 — add variable declarations + `gcPauseCore` / `currentCPU`)

**Purpose.** Kernel bodies for `sleepTicks`, `ticks`, `putchar`, `exit`, `abort`, bare-metal `main`.

**0.40.1 delta (Wave 1).** Upstream runtime interface stable; function signatures identical to 0.33.0. Clean apply.

**0.40.1 additions at Wave 2.** Two categories:

**(a) Runtime-lock variable declarations.** Upstream `scheduler_cores.go:260-290` **defines** `lockScheduler`, `unlockScheduler`, `lockFutex`, `unlockFutex`, `lockAtomics`, `unlockAtomics` as function bodies that take methods on four variables — `schedulerLock`, `futexLock`, `atomicsLock`, `printLock`. These variables must be declared per-target. For RP2 they live in `runtime_rp2.go:293-299`. gooos declares them in `runtime_gooos.go`:

```go
// runtime_gooos.go — Wave 2 addition
type gooosSpinLock struct {
    locked uint32
}
func (s *gooosSpinLock) Lock()   { gooos_spinlockAcquire(&s.locked) }
func (s *gooosSpinLock) Unlock() { gooos_spinlockRelease(&s.locked) }

var (
    printLock     gooosSpinLock
    schedulerLock gooosSpinLock
    atomicsLock   gooosSpinLock
    futexLock     gooosSpinLock
)
```

The `gooos_spinlockAcquire` / `gooos_spinlockRelease` assembly is already present (`src/stubs.S`, `impldoc/smp_percpu_and_sync.md §4.2`).

**Do NOT redefine `lockScheduler` / `unlockScheduler` / `lockFutex` / `unlockFutex` / `lockAtomics` / `unlockAtomics`.** The functions exist upstream and re-definition is a duplicate-symbol link error.

**(b) New function bodies that upstream does not supply.**
- `const numCPU = 17` at package scope (see `impldoc/smp_scheduler_design.md §3.1`).
- `//go:linkname gcPauseCore runtime.gcPauseCore` body. Implementation: IPI send + poll per-CPU ack flag. Details in `impldoc/smp_scheduler_design.md §7.2`.
- `//go:linkname currentCPU runtime.currentCPU` body. Implementation: call-through to `cpuID()`.

**Upstream-PR feasibility.** Not applicable — these are target-specific declarations/bodies.

### 3.9 `src/runtime/runtime_gooos_user.go` (Wave 1 — path unchanged; Wave 2 — add hooks)

**Purpose.** Userspace syscall-routed runtime bodies.

**0.40.1 delta.** Stable interface; clean apply.

**Wave 2 additions.** Mirror kernel side where applicable:
- `const numCPU = 17` (the TinyGo userspace scheduler also needs the constant if `scheduler=cores` is enabled for user binaries; decision on user-mode cores promotion is deferred past M5 — keep at `scheduler=tasks` in `user/target.json` for now).
- `currentCPU` userspace body — call-through to `sys_getcpuid` syscall (already wired in gooos, `impldoc/smp_deferred_and_known_issues.md §1` confirms `sys_getcpuid` exists).

**If user-mode stays on `scheduler=tasks`** (current plan), userspace does not need the futex-cores or `gcPauseCore` bodies.

### 3.10 `src/runtime/scheduler.go` → `scheduler_cooperative.go` / `scheduler_cores.go` (RELOCATION, largest rebase)

**Purpose.** gooos replaces `var runqueue task.Queue` with `var runqueues [numCPU]task.Queue`, adds spinlock protection over `sleepQueue` / timer queue access, adds `runqueuePushTo(cpu, t)`, adds work-stealing `stealWork()`, adds `apScheduler()` export. This is the largest single hunk set (lines 606–827 of the current patch, ~222 lines).

**0.40.1 file split.**
- `scheduler_cooperative.go` hosts the `scheduler=tasks || scheduler.asyncify` body (build tag at `scheduler_cooperative.go:1-2`). Single-threaded; `runqueue`, `sleepQueue`, `sleepQueueBaseTime` globals (assessment §1.3).
- `scheduler_cores.go` hosts `scheduler=cores` body. Includes `cpuTasks [numCPU]*task.Task` (line 22), **single global `runqueue task.Queue`** (line 26 — not per-CPU), `scheduleTask` push site (lines 37-57), `Gosched` push site (lines 87-91), `lockFutex`/`unlockFutex`/`lockAtomics`/`unlockAtomics`/`lockScheduler`/`unlockScheduler` definitions (lines 260-290), `systemStack [numCPU]uintptr` + `systemStackPtr()` accessor (lines 292-300).

**Rebase approach — Wave 1 (tasks mode).** Retarget the entire hunk set to `scheduler_cooperative.go`. Every `runqueue` reference migrates unchanged. `schedLock` declaration, `apScheduler()` stub export, `runqueuePushTo`, `stealWork` all land in `scheduler_cooperative.go`. Line numbers will drift significantly; plan is to regenerate the patch after the first clean rebase (`git diff` from a working 0.40.1 source tree into `scripts/tinygo_runtime.patch.wave1`).

**Rebase approach — Wave 2 (cores promotion). Enumerated push-site patch.**

Upstream `scheduler_cores.go` pushes tasks to the single global `runqueue` at several sites. gooos must patch each to route per-CPU:

| Upstream site | Upstream line | gooos rewrite |
|---|---|---|
| `scheduleTask`: `runqueue.Push(t)` | `scheduler_cores.go:43` | `runqueues[gooosCpuID()].Push(t)` (or use `t.CPU` hint if added) |
| `Gosched`: `runqueue.Push(task.Current())` | `scheduler_cores.go:89` | `runqueues[gooosCpuID()].Push(task.Current())` |
| `scheduler()` pop site | inside scheduler loop (~line 200-240) | pop from `runqueues[gooosCpuID()]`; on nil call `stealWork` |
| `addSleepTask` — sleep queue insert | `scheduler_cores.go:59-85` | No change; sleep queue stays global |
| `addTimer` — timer queue insert | `scheduler_cores.go:98-` | No change; timer queue stays global |

Also add gooos-local: `var runqueues [numCPU]task.Queue`, `func stealWork`, `func apScheduler`, `func runqueuePushTo`.

Upstream `cpuTasks [numCPU]*task.Task` (line 22) stays in place; gooos may or may not use it (gooos has its own `currentTasks[17]` via the existing task_stack_amd64.go patch). Resolving the overlap is a Wave 2 decision — tentatively accept the duplication because `cpuTasks` is upstream-private and removing it would mean more patching.

**Upstream-PR feasibility.** Medium for `stealWork` + per-CPU runqueue array; low for `apScheduler` (AP bring-up pattern varies across targets). No action in this migration.

### 3.11 `src/runtime/wait_gooos.go` (Wave 1 — path unchanged, new file)

**Purpose.** `waitForEvents` idle loop (`sti; hlt; cli`). Replaces panic default from `runtime/wait_other.go`.

**0.40.1 delta.** Upstream `wait_other.go` may or may not exist in 0.40.1 — needs verification at M0 Entry gate (see §5). If it exists, `wait_gooos.go` provides a build-tag-selected alternative exactly as today. If it was removed upstream in favour of a cleaner default, gooos's file becomes the sole provider — still applies.

**Rebase approach.** Apply verbatim. Clean apply expected.

### 3.12 `src/runtime/wait_other.go` (Wave 1 — relocation-uncertain, modify)

**Purpose.** Modification hunk (7 lines) alters the upstream default `waitForEvents` to not-panic under a specific build condition, so gooos can override it from `wait_gooos.go`.

**0.40.1 delta.** **Uncertainty.** The file may have been renamed / restructured in 0.40.0. M0 Entry gate includes:
> Verify `src/runtime/wait_other.go` exists in 0.40.1 and hosts a `waitForEvents` default. If renamed, locate the replacement and retarget the hunk. If removed entirely, drop this hunk from the Wave 1 patch.

Expected outcomes:
- **Path unchanged:** retarget verbatim.
- **Renamed to `wait_default.go` or similar:** retarget with path rewrite; contents likely stable.
- **Removed:** drop hunk; `wait_gooos.go` becomes the sole provider via build tags.

### 3.13 NEW: `src/runtime/gc_hooks_gooos.go` (Wave 2 — new file, conditional)

**Purpose (Wave 2 only).** If the `gc_blocks.go` runqueue-scan hunk is retired in favour of upstream `gc_stack_cores.go`, gooos still needs to export a `gcPauseCore(i uint32)` linkname body. Easiest home is a new file under build tag `gooos && scheduler.cores`.

**Alternative placement.** Embed in `runtime_gooos.go` (already the home of linkname bodies). If placing there, skip the new file.

**Decision.** Embed in `runtime_gooos.go`. Do **not** create `gc_hooks_gooos.go`. This keeps the new-file count at 5 (unchanged from today).

---

## 4. Patch-file Structure Plan

### 4.1 One patch file, two apply phases

Keep a single `scripts/tinygo_runtime.patch`. At Wave 1 it targets 0.40.1 + `scheduler=tasks`. At Wave 2 the same file is updated (git-diff regenerated) to add cores-mode hunks.

Alternative: two files (`tinygo_runtime_base.patch` + `tinygo_runtime_cores.patch`). Rejected — doubles maintenance cost for modest bisect benefit. Single file + the Wave marker in commit messages provides enough traceability.

### 4.2 `patch_tinygo_runtime.sh` updates

Modify `scripts/patch_tinygo_runtime.sh` to:

1. Point `TINYGO_SRC` default at `$HOME/.local/tinygo0.40.1/src` (currently `$HOME/.local/tinygo/src` per `scripts/patch_tinygo_runtime.sh:31`).
2. Update the idempotency post-conditions to probe the new target files:
   - `grep -q 'runqueues' $SCHED` — currently `SCHED=$TINYGO_SRC/runtime/scheduler.go` (line 57). After Wave 1, `SCHED=$TINYGO_SRC/runtime/scheduler_cooperative.go`. After Wave 2, also probe `scheduler_cores.go`.
   - New grep for the `numCPU = 17` constant in `runtime_gooos.go` (Wave 2).
   - New grep for `lockFutex` in `runtime_gooos.go` (Wave 2).
3. Keep the "already-applied" fast exit; keep `--forward --batch` idempotent apply; keep `.rej` cleanup.

Exact edits are captured in `impldoc/toolchain_switch_plan.md §patch_tinygo_runtime.sh`.

### 4.3 Dual-version support during transition

Some contributors may still have `~/.local/tinygo` (0.33.0 patched). Provide a grace-period behaviour in the script:

- If `TINYGO_SRC` unset and `$HOME/.local/tinygo0.40.1` exists: use it.
- Else if `$HOME/.local/tinygo` exists: use it and print a deprecation notice recommending reinstall.
- Else error with instructions.

This avoids breaking in-flight branches mid-migration.

---

## 5. Estimated Size

Current: **853 lines, 12 file regions** (1 of which is tiny — `wait_other.go` 7 lines).

After Wave 1 (tasks-mode rebase): **~860 lines, 12 file regions** (small growth from relocation context drift; `scheduler.go` → `scheduler_cooperative.go` may add a few lines if `sleepQueue` variable names changed).

After Wave 2 (cores-mode promotion): **~550 lines, 11 file regions** (shrink: retire runqueue-scan hunk in `gc_blocks.go`, retire the gooos-added Queue spinlock if the cleanup PR lands; grow: `numCPU` + futex + gcPauseCore linknames in `runtime_gooos.go`).

**-35% vs. current state.** Maintenance-cost benefit justifies the migration on its own — independent of any SMP correctness gain.

---

## 6. Apply Order (exact)

Wave 1 (M0 → M1):

1. Copy upstream TinyGo 0.40.1 to `$HOME/.local/tinygo0.40.1` (already present; confirmed by assessment §7).
2. Update `Makefile:13` → `TINYGOROOT ?= $(HOME)/.local/tinygo0.40.1`.
3. Update `scripts/patch_tinygo_runtime.sh:31` → `TINYGO_SRC=...tinygo0.40.1/src`.
4. Rebase hunks 1–12 onto 0.40.1 (mostly mechanical; §3.10 relocation is the biggest lift).
5. Regenerate `scripts/tinygo_runtime.patch` from clean apply → `git diff`.
6. M0 Exit gate runs (see `impldoc/smp_milestones_and_verification.md §M0`).

Wave 2 (M3):

1. Flip `src/target.json:"scheduler"` `"tasks"` → `"cores"`.
2. Add Wave 2 hunks per §3.8, §3.10 (cores-mode scheduler), §3.5 (partial retirement).
3. Regenerate `scripts/tinygo_runtime.patch`.
4. M3 Exit gate runs.

---

## 7. Verification per hunk

Every modified / added file gets a grep-based post-condition in `scripts/patch_tinygo_runtime.sh` (same pattern as today). The milestone document carries the behavioural verification (boot, test harnesses, `-smp N`). Here we only assert **structural**: after apply, the expected symbols exist.

Structural post-conditions after Wave 1:

- `runtime_gooos.go` contains `func sleepTicks`, `&& kernelspace`.
- `runtime_gooos_user.go` contains `&& !kernelspace`.
- `interrupt_gooos.go`, `interrupt_gooos_user.go` present.
- `wait_gooos.go` contains `waitForEvents`.
- `task_stack.go` contains `gooosStackOverflow`.
- `task_stack_amd64.go` contains `gooosOnResume`, `systemStacks`.
- `scheduler_cooperative.go` contains `runqueues`, `schedLock`, `apScheduler`, `stealWork`.
- `chan.go` contains `gooosCpuID`.
- `queue.go` contains `gooos_spinlockAcquire`.
- `gc_blocks.go` contains `heapLock`.

Additional post-conditions after Wave 2:

- `runtime_gooos.go` contains `numCPU = 17`, `atomicsLock`, `schedulerLock`, `futexLock`, `printLock`, `gcPauseCore`, `currentCPU`.
- `scheduler_cores.go` contains `runqueues` (gooos-added), `stealWork`, `apScheduler`.
- `task_stack_amd64.go` build tag mentions `scheduler.cores` (widened).
- `src/target.json` contains `"scheduler": "cores"`.

---

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
