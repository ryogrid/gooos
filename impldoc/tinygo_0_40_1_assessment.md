# TinyGo 0.40.1 — x86_64 Bare-Metal SMP Suitability Assessment

**Scope.** Evidence-based verdict on whether TinyGo 0.40.1 can support SMP / multi-core goroutine scheduling for gooos on x86_64 bare metal. All citations are to the cloned 0.40.1 source tree at `/home/ryo/work/tinygo` (spot-checked against the installed toolchain at `/home/ryo/.local/tinygo0.40.1`, layout matches).

**Cross-links.**
- Overview and milestone map: `impldoc/smp_migration_overview.md`
- Scheduler design that consumes this verdict: `impldoc/smp_scheduler_design.md`
- Patch rebase that depends on file-location evidence here: `impldoc/runtime_patches.md`
- Existing SMP v2 design these findings must stay consistent with: `impldoc/smp_overview.md`, `impldoc/smp_kernel_scheduler.md`

---

## 0. Verdict

**SUITABLE-WITH-PATCHES.**

Upstream 0.40.1 introduces the `scheduler.cores` build-tag variant and per-CPU task storage (`cpuTasks [numCPU]*task.Task`) that mirror the abstractions gooos already implements on top of 0.33.0. It does **not** ship an x86_64 bare-metal target; gooos must create one. Critical subsystems for multi-core correctness — `lockAtomics` / `gcMarkReachable` / futex-cores / mutex-preemptive — are present and usable if gooos supplies the expected runtime linkname hooks. Three gooos blockers (AP LAPIC timer race, Ring-3 triple-fault on AP `iretq`, GC stop-the-world) are **orthogonal to TinyGo** and remain post-migration; the assessment recommends proceeding with the toolchain switch because it *shrinks* the gooos patch and aligns the project with upstream's multi-core direction, not because it auto-fixes the blockers.

### Falsifiability conditions (what would flip the verdict)

- If `scheduler.cores` turns out to hard-require a build-time constant `numCPU` that cannot be made dynamic via patch (see §2.3), and gooos insists on CPU-count autodetection rather than a ceiling constant, the verdict drops to **UNSUITABLE** for the near term and `impldoc/smp_alternatives.md` is authored instead.
- If re-basing gooos's `scheduler.go` hunks onto 0.40.1's `scheduler_cooperative.go` / `scheduler_cores.go` file split (see §1.3) — including the per-CPU runqueue patching of upstream push sites (`scheduleTask`, `Gosched`) — costs more than the saved upstream-adoption benefit (estimated >400 net lines of new patch surface), the team may elect to stay on 0.33.0 indefinitely. This is an economic decision, not a technical blocker.
- If `task_stack_amd64.go`'s cores-mode widening (§5.1 option 1) runs into a build-tag conflict that blocks compilation under `scheduler=cores`, the verdict drops to **SUITABLE-WITH-PATCHES (Wave 2 deferred)** — Wave 1 still lands but cores promotion waits for an upstream or gooos-side resolution.

---

## 1. Scheduler architecture

### 1.1 Four scheduler variants (selected via `target.json:"scheduler"`)

- `scheduler.none` — no scheduler.
- `scheduler.tasks` — cooperative single-threaded. Implementation: `src/runtime/scheduler_cooperative.go`.
- `scheduler.cores` — multi-core aware, per-CPU runqueues. Implementation: `src/runtime/scheduler_cores.go`.
- `scheduler.threads` — OS-thread-backed (Linux/Windows).

### 1.2 `scheduler.cores` key signals (upstream, unpatched)

- **Parallelism flag:** `src/runtime/scheduler_cores.go:13` — `const hasParallelism = true`.
- **Per-CPU task storage:** `src/runtime/scheduler_cores.go:22` — `var cpuTasks [numCPU]*task.Task`.
- **CPU count API:** `src/runtime/scheduler_cores.go:93-96`
  ```go
  // NumCPU returns the number of CPU cores on this system.
  func NumCPU() int {
      return numCPU
  }
  ```
- **Atomic-lock provider:** `src/runtime/scheduler_cores.go:281-290`
  ```go
  func lockAtomics() interrupt.State {
      mask := interrupt.Disable()
      atomicsLock.Lock()
      return mask
  }
  func unlockAtomics(mask interrupt.State) {
      atomicsLock.Unlock()
      interrupt.Restore(mask)
  }
  ```

### 1.3 `scheduler.cooperative` (what gooos uses today, relocated in 0.40.1)

- `src/runtime/scheduler_cooperative.go:1-2`
  ```go
  //go:build scheduler.tasks || scheduler.asyncify
  ```
- `src/runtime/scheduler_cooperative.go:28` — `const hasParallelism = false`.
- `src/runtime/scheduler_cooperative.go:38-42`
  ```go
  var (
      runqueue           task.Queue
      sleepQueue         *task.Task
      sleepQueueBaseTime timeUnit
  )
  ```

**Relocation impact.** The hunks gooos's `scripts/tinygo_runtime.patch` currently applies to `runtime/scheduler.go` (lines 606–827 of the patch) must be split in 0.40.1:
- If gooos continues on `scheduler=tasks`, they retarget `scheduler_cooperative.go`.
- If gooos promotes to `scheduler=cores`, they retarget `scheduler_cores.go` and drop per-CPU-runqueue hunks that the upstream file already provides.

### 1.4 GC stack scanning is already cores-aware

`src/runtime/gc_stack_cores.go:1` — `//go:build scheduler.cores`.

`src/runtime/gc_stack_cores.go:16–80` — `gcMarkReachable()` calls `gcPauseCore(i)` across all peer CPUs before scanning. This is the stop-the-world protocol gooos's design docs describe as unsolved (`impldoc/smp_deferred_and_known_issues.md §5`). If gooos moves to `scheduler=cores` and supplies a `gcPauseCore` linkname body (IPI-based), a large open item closes upstream-first.

---

## 2. Multi-core primitives

### 2.1 `NumCPU` and `cpuTasks`

See §1.2 for citations. The array size `numCPU` is a build-time constant; see §2.3.

### 2.2 Per-core task pointer

Upstream `scheduler_cores.go` indexes `cpuTasks[]` by `currentCPU()` (linkname imported from the target-specific runtime). For RP2040/RP2350 this is defined in `src/runtime/runtime_rp2.go`. For gooos, the project already supplies a `cpuID()` helper in `src/percpu.go` (via `%gs:0`); the linkname bridge is trivial.

### 2.3 `numCPU` definition

- **RP2040 / RP2350:** `src/runtime/runtime_rp2.go:16` — `const numCPU = 2` under build tag `//go:build rp2040 || rp2350` (`runtime_rp2.go:1`).
- **x86_64:** **absent upstream.** gooos must supply the constant for its target. Options (rank by effort, cheapest first):
  1. Static `const numCPU = 17` matching gooos's existing `maxCPUs` (`impldoc/smp_percpu_and_sync.md §1.3`, decision D10 in `impldoc/smp_overview.md`). Simplest; matches the dimension already used for `runqueues[17]`, `systemStacks[17]`, `currentTasks[17]`, `perCPUBlocks[17]`.
  2. Patch TinyGo to read a runtime variable (gooos sets `runtime.numCPU = activeCPUs` during BSP bring-up). Requires turning `const` into `var`, updating every array dimension that uses `numCPU`. Viable but propagates through `cpuTasks`, `gc_stack_cores.go`, and array-index bounds checks.
  3. Autodetect at TinyGo code-gen time by passing `-tags numcpu17` (or similar) and selecting one of several compile-time branches. Heavyweight.

**Recommendation for migration plan:** option 1. It keeps the patch small and aligns with `maxCPUs = 17` already established in gooos SMP v2 design.

### 2.4 Work stealing

Upstream `scheduler_cores.go` (as of 0.40.1) does not implement work-stealing across peer `cpuTasks`; it supports per-core task ownership but schedules cooperatively within a core. gooos's `scripts/tinygo_runtime.patch` already supplies a `stealWork()` helper targeting `scheduler.go` (per `impldoc/smp_kernel_scheduler.md §5`). After migration, that helper targets `scheduler_cores.go` and remains a gooos-local addition (likely not upstream-acceptable because RP2040/RP2350 use cases do not need it).

---

## 3. Bare-metal / freestanding targets

### 3.1 No stock x86_64 target in 0.40.1

`ls /home/ryo/work/tinygo/targets/ | grep -Ei 'x86|amd64|x64'` returns no matches. The only `scheduler: cores` targets shipped are `targets/rp2040.json` and `targets/rp2350.json` (ARM Cortex-M0+/M33).

### 3.2 gooos's existing target

`src/target.json` (current, 16 lines):
```json
{
    "llvm-target": "x86_64-unknown-linux-elf",
    "cpu": "x86-64",
    "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
    "build-tags": ["gooos", "baremetal", "kernelspace"],
    "goos": "linux",
    "goarch": "amd64",
    "gc": "conservative",
    "scheduler": "tasks",
    ...
}
```

**Action item captured in `impldoc/toolchain_switch_plan.md`:** keep `"scheduler": "tasks"` for M0 (parity) and M1 (BSP-only on 0.40.1); flip to `"scheduler": "cores"` at M3 when AP scheduler entry is wired to `scheduler_cores.go`.

### 3.3 Build-tag family in 0.40.1 relevant to gooos

- `baremetal` — common bare-metal tag used by `atomics_critical.go` and the interrupt-disable-based atomics path.
- `scheduler.cores` — enables the multi-core scheduler file.
- `!tinygo.unicore` — enables `mutex-preemptive.go`.
- `tinygo.wasm` — negated in `atomics_critical.go:1-2`.

---

## 4. Synchronization primitives

### 4.1 Baremetal atomics (interrupt-disable)

`src/runtime/atomics_critical.go:1-2` — `//go:build baremetal && !tinygo.wasm`.

Representative op at `atomics_critical.go:21-29`:
```go
//export __atomic_load_2
func __atomic_load_2(ptr *uint16, ordering uintptr) uint16 {
    mask := lockAtomics()
    val := *ptr
    unlockAtomics(mask)
    return val
}
```

`lockAtomics` / `unlockAtomics` come from the selected scheduler (`scheduler_cores.go:281-290` for cores mode; `scheduler_cooperative.go` provides the single-threaded version that just calls `interrupt.Disable`).

**Consequence.** On `scheduler=cores`, atomic ops take `atomicsLock.Lock()` — a real cross-core mutex — plus `interrupt.Disable`. This is sufficient for multi-core correctness. On `scheduler=tasks`, atomic ops only disable interrupts on the calling CPU — **not sufficient for SMP**. Gooos's existing explicit spinlocks around `sleepQueue`/`timerQueue`/`runqueues`/heap remain necessary in either mode.

### 4.2 Mutex and the spinlock-type resolution

`src/internal/task/mutex-preemptive.go:1` — `//go:build !tinygo.unicore`.

`mutex-preemptive.go:31-36`
```go
func (m *Mutex) Lock() {
    // Fast path: try to take an uncontended lock.
    if m.futex.CompareAndSwap(0, 1) {
        // We obtained the mutex.
        return
```

`Mutex.Lock` uses `sync/atomic.Uint32.CompareAndSwap`, which routes through the atomic ops described in §4.1. If `atomicsLock` (used by `lockAtomics`) were itself a `Mutex`, its `Lock()` would recurse back into `lockAtomics` → deadlock.

**Upstream resolves this by type choice, not by probe.** On RP2 targets, `atomicsLock`, `futexLock`, `schedulerLock`, and `printLock` are all declared as a target-local `spinLock` struct, not as `task.Mutex` — see `src/runtime/runtime_rp2.go:293-299`:

```go
var (
    printLock     = spinLock{id: 20}
    schedulerLock = spinLock{id: 21}
    atomicsLock   = spinLock{id: 22}
    futexLock     = spinLock{id: 23}
)
```

The `spinLock.Lock()` method (defined per-target) never calls `CompareAndSwap`, so the recursion path does not exist.

**Design implication for gooos.** Declare the same four lock variables in `runtime_gooos.go` (Wave 2) using a gooos-local spinlock struct (plausibly delegating to `src/spinlock.go` / `src/stubs.S` `spinlockAcquire/Release`). Do not use `task.Mutex` for these. The recursion concern is design-eliminated, not runtime-validated. M1 still runs a boot-time atomic probe as a smoke test (`impldoc/smp_milestones_and_verification.md §M1`), but a probe failure signals an unrelated bug, not the recursion path.

### 4.3 Futex — upstream defines the functions, gooos supplies the variable

`src/internal/task/futex-cores.go:1` — `//go:build scheduler.cores`.

`futex-cores.go:60-64` (linkname import, the consumer side)
```go
//go:linkname lockFutex runtime.lockFutex
func lockFutex() interrupt.State

//go:linkname unlockFutex runtime.unlockFutex
func unlockFutex(interrupt.State)
```

`futex-cores.go:21-26` (Wait header)
```go
func (f *Futex) Wait(cmp uint32) (awoken bool) {
    mask := lockFutex()

    if f.Uint32.Load() != cmp {
        unlockFutex(mask)
        return false
```

**The bodies are already defined upstream** at `src/runtime/scheduler_cores.go:268-277`:

```go
func lockFutex() interrupt.State {
    mask := interrupt.Disable()
    futexLock.Lock()
    return mask
}

func unlockFutex(state interrupt.State) {
    futexLock.Unlock()
    interrupt.Restore(state)
}
```

**gooos implication.** Do **not** redefine `lockFutex` / `unlockFutex` in `runtime_gooos.go` — that would produce a duplicate-symbol link error. gooos supplies the **variable** `futexLock` (plus `atomicsLock`, `schedulerLock`, `printLock`) using a gooos-local `spinLock` type, mirroring the RP2 pattern in `src/runtime/runtime_rp2.go:293-299`. See §4.2 for the type choice.

### 4.4 Queue (task runqueue)

`src/internal/task/queue.go:9-11`
```go
type Queue struct {
    head, tail *Task
}
```

`queue.go:14-18` (Push)
```go
func (q *Queue) Push(t *Task) {
    mask := lockAtomics()
    if asserts && t.Next != nil {
        unlockAtomics(mask)
        panic("runtime: pushing a task to a queue with a non-nil Next pointer")
```

**Change vs. 0.33.0.** Upstream 0.33.0 used `interrupt.Disable()` directly. Upstream 0.40.1 routes through `lockAtomics()`, which under `scheduler=cores` provides real multi-core protection. gooos's current 0.33.0 patch adds an explicit per-Queue spinlock on top; under 0.40.1 + `scheduler=cores` the Queue is already cross-core safe **if** `lockAtomics` is the cores variant. For `scheduler=tasks` (the staged intermediate), gooos's explicit spinlock is still required. Detailed migration notes in `impldoc/runtime_patches.md §Queue`.

---

## 5. Task stack / per-CPU stacks

### 5.1 `task_stack_amd64.go` upstream state — scheduler-mode dependent

`src/internal/task/task_stack_amd64.go:1` build-tag is **`//go:build scheduler.tasks && amd64 && !windows`**. Under `scheduler=cores` on amd64, **this file does not compile at all** — upstream ships no amd64 cores task-stack binding.

Under tasks mode (lines 7, 47-55):
```go
var systemStack uintptr
...
func (s *state) resume() { swapTask(s.sp, &systemStack) }
func (s *state) pause() { newStack := systemStack; systemStack = 0; swapTask(newStack, &s.sp) }
```

Under cores mode, upstream declares the per-CPU array at `src/runtime/scheduler_cores.go:292` and publishes an accessor:
```go
var systemStack [numCPU]uintptr

// Implementation detail of the internal/task package. ...
func systemStackPtr() *uintptr {
    return &systemStack[currentCPU()]
}
```

The **only** upstream `task_stack_*.go` file that uses this accessor is `task_stack_tinygoriscv.go:13-72` via `//go:linkname runtime_systemStackPtr runtime.systemStackPtr`. There is no amd64 counterpart.

**Migration implication (split per wave):**

- **Wave 1 (tasks mode).** Keep gooos's current patch on `task_stack_amd64.go` (replace singular `systemStack` with `systemStacks [numCPU]uintptr` indexed by `cpuID()`). The existing build tag (`scheduler.tasks && amd64`) still matches.
- **Wave 2 (cores mode).** Upstream's `systemStack [numCPU]uintptr` in `scheduler_cores.go:292` supplants the per-CPU array. gooos has two options:
  1. **Recommended:** widen the build tag on `task_stack_amd64.go` to `(scheduler.tasks || scheduler.cores) && amd64 && !windows`, swap `&systemStacks[cpuID()]` for a `runtime_systemStackPtr()` linkname import mirroring `task_stack_tinygoriscv.go:12-13`. This retires gooos's per-CPU `systemStacks` array in favour of upstream.
  2. **Fallback:** keep the gooos patch unchanged; accept a duplicate of upstream's `systemStack[numCPU]` in the gooos build.
  
  Option 1 is preferred because it matches upstream's direction. Option 2 is listed for completeness; decide at M3 based on rebase complexity.

### 5.2 `task_stack.go`

No `stackTop` field upstream (confirmed absent). gooos's `gooosStackOverflow` hook and `stackTop` field remain gooos-local patches; the patch file location is unchanged in 0.40.1.

### 5.2 `task_stack.go`

No `stackTop` field upstream (confirmed absent). gooos's `gooosStackOverflow` hook and `stackTop` field remain gooos-local patches; the patch file location is unchanged in 0.40.1.

---

## 6. CHANGELOG / release notes

`CHANGELOG.md` entries relevant to SMP:

- Line ~137: `runtime: enable multi-core scheduler for rp2350`
- Line ~206: `implement NumCPU for the multicore scheduler`
- Line ~207: `add support for multicore scheduler`
- Line ~232: `rp2040: add multicore support`

0.40.1 adds no x86_64-specific multicore changes beyond the cores scheduler generalization shipped in 0.40.0.

---

## 7. Spot-check of installed toolchain

`/home/ryo/.local/tinygo0.40.1/` top-level contents (per Explore): `bin/`, `lib/`, `src/`, `targets/`. Layout matches the cloned `../tinygo/` source tree. `scheduler_cores.go:13` in the install also reads `const hasParallelism = true`, confirming the install and clone are the same version — both are acceptable patch-apply targets, though the gooos build will only consume the install.

---

## 8. Gap analysis for x86_64 bare-metal SMP

| Requirement for gooos SMP goals | Stock 0.40.1 | gooos-today (patched 0.33.0) | What the migration must do |
|---|---|---|---|
| Cooperative goroutine scheduling | ✓ (`scheduler.tasks` / cooperative) | ✓ | Re-base hunks onto `scheduler_cooperative.go` for M0/M1 |
| Per-core task pointer | ✓ (`cpuTasks`, cores mode only) | ✓ (`currentTasks[17]`) | Supply `currentCPU()` linkname for cores mode |
| Per-core runqueue | ✗ (cores mode has single global `runqueue` + `schedulerLock`) | ✓ (`runqueues[17]`) | Keep gooos `runqueues[17]` + `stealWork()` on top of cores mode; patch upstream push sites (`scheduleTask`, `Gosched`, chan.go wakes) to route per-CPU |
| Per-core system stack | ✓ under cores mode (`systemStack [numCPU]uintptr` + `systemStackPtr()`) | ✓ (`systemStacks[17]`) | Wave 1: keep gooos patch. Wave 2: retire gooos patch, widen `task_stack_amd64.go` build-tag, consume upstream `systemStackPtr()` via linkname |
| Cross-core atomics | ✓ under cores (`atomicsLock` declared as target-local `spinLock`) | ✓ (per-structure spinlocks) | Declare `atomicsLock` variable of type `spinLock` in `runtime_gooos.go` Wave 2 (see §4.2) |
| `NumCPU` API | ✓ (cores mode) | ✗ | Define `const numCPU = 17` for x86_64 target |
| Futex | ✓ under cores: `lockFutex`/`unlockFutex` bodies in `scheduler_cores.go:268-277` | ✗ (unused) | Declare `futexLock` variable of type `spinLock` in `runtime_gooos.go`. **Do not** redefine the functions |
| Scheduler lock | ✓ under cores: `lockScheduler`/`unlockScheduler` in `scheduler_cores.go:260-266` | Implicit in gooos per-queue locks | Declare `schedulerLock` variable in `runtime_gooos.go` |
| Print lock | ✓ under cores: `printLock.Lock()` in `scheduler_cores.go:306-315` | ✗ (gooos uses `vgaLock` / `serialLock` directly) | Declare `printLock` variable in `runtime_gooos.go` |
| GC stop-the-world | ✓ orchestration: `gcMarkReachable()` in `gc_stack_cores.go`; `gcPauseCore(i)` is a runtime linkname | ✗ | Provide `gcPauseCore(i)` body (IPI-driven) — closes SMP v2 open item 5 |
| AP LAPIC timer race fix | n/a (kernel bug) | ✗ | Orthogonal to migration — remains in TODO |
| AP Ring-3 `iretq` triple-fault | n/a (kernel bug) | ✗ | Orthogonal to migration — remains in TODO |

---

## 9. Patch surface delta vs. 0.33.0

Current gooos patch (`scripts/tinygo_runtime.patch`): **853 lines, 12 file regions** (8 modified, 5 new; enumeration in `impldoc/runtime_patches.md §Current State`).

After migration to 0.40.1 + `scheduler=cores`:
- **Shrinks:** per-CPU task pointer, `NumCPU`, GC-cores stack scan come from upstream.
- **Relocates:** scheduler.go hunks → `scheduler_cores.go`; `wait_other.go` may disappear (needs verification at M0).
- **Grows slightly:** runtime-lock **variable declarations** (`atomicsLock`, `schedulerLock`, `futexLock`, `printLock` of gooos-local `spinLock` type), plus new `gcPauseCore` and `currentCPU` bodies. The lock *functions* (`lockFutex`, `unlockFutex`, `lockAtomics`, `unlockAtomics`, `lockScheduler`, `unlockScheduler`) are **not** re-defined by gooos — they live upstream in `scheduler_cores.go:260-290`.

**Estimated net patch size after migration:** ~550 lines (-35%). Estimation tracked in `impldoc/runtime_patches.md §Estimated size`.

---

## 10. What this verdict does NOT promise

1. **Migration does not fix AP Ring-3 `iretq` triple-fault** (`impldoc/smp_deferred_and_known_issues.md §2.1`). Root cause is gooos-side per-CPU TSS / Ring-3 → Ring-0 transition, not TinyGo.
2. **Migration does not fix AP LAPIC timer global-counter race** (`§2.2`). Root cause is gooos ISR prologue dual-counter design.
3. **Migration does not guarantee numeric performance gains** — the goal is correctness and patch-maintenance reduction.
4. **The `mutex-preemptive.go` ↔ `atomics_critical.go` recursion chain (§4.2) is theoretical until validated** on gooos's x86_64 target. M1 Exit gate includes a bring-up smoke that triggers one `atomicsLock.Lock()` during boot and asserts no hang.

---

## 11. Evidence capture log

Every cited line pair in this document was captured from `/home/ryo/work/tinygo` and, where specified, spot-checked against `/home/ryo/.local/tinygo0.40.1/src/`. Install-layout verification is part of the M0 Entry gate (see `impldoc/smp_milestones_and_verification.md §M0`).

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
