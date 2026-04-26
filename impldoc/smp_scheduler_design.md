# SMP Scheduler Design under TinyGo 0.40.1

**Scope.** How TinyGo 0.40.1's `scheduler.cores` composes with gooos's existing SMP v2 per-CPU infrastructure to reach the milestone "goroutines scheduled on each processor / core". Does **not** describe kernel-level SMP bring-up (GS base, GDT, TSS, IPI) — those remain as-built per `impldoc/smp_percpu_and_sync.md` and `impldoc/smp_kernel_lapic_and_ipi.md`.

**Cross-links.**
- Verdict this doc builds on: `impldoc/tinygo_0_40_1_assessment.md`
- Existing (0.33.0-era) scheduler design this doc **extends**: `impldoc/smp_kernel_scheduler.md`, `impldoc/goroutine_design_scheduler.md`
- Patch rebase detail: `impldoc/runtime_patches.md`
- Milestone/verification: `impldoc/smp_milestones_and_verification.md`
- Top-level index: `impldoc/smp_migration_overview.md`

This document does **not supersede** `impldoc/smp_kernel_scheduler.md`. The SMP v2 per-CPU runqueue + work-stealing design documented there is the foundation; this document describes how it lands on top of 0.40.1 file structure instead of 0.33.0.

---

## 1. Design Principles

1. **Staged promotion.** Milestone M0/M1 stay on `scheduler=tasks` (cooperative, scheduler_cooperative.go). Milestone M3 promotes to `scheduler=cores` (scheduler_cores.go). This lets each step be bisected against the previous; `-smp 1` parity is the M0 gate.
2. **Per-CPU arrays dimension = 17.** Match existing gooos constant `maxCPUs = 17` (`impldoc/smp_percpu_and_sync.md §1.3`, decision D10 in `impldoc/smp_overview.md`). Define `const numCPU = 17` in the patched TinyGo runtime so the upstream `scheduler_cores.go:22` declaration (`var cpuTasks [numCPU]*task.Task`) matches gooos's array sizing.
3. **`cpuID()` is authoritative.** Every per-CPU index uses the `%gs:0`-based `cpuID()` helper (`src/percpu.go` / `src/stubs.S`) via `//go:linkname` into the runtime. This keeps O(1) per-access cost and avoids a second source of truth for "which CPU am I on".
4. **Upstream-first where cheap.** Use `scheduler_cores.go`'s `cpuTasks`, `NumCPU`, `lockAtomics`, `gcMarkReachable` verbatim. Patch only what upstream lacks: per-CPU *runqueue* array (upstream tracks single task, not queue), `stealWork`, AP scheduler entry hook.
5. **Orthogonal bugs stay orthogonal.** This design does not attempt to fix the AP LAPIC timer race (`impldoc/smp_deferred_and_known_issues.md §2.2`) or Ring-3 `iretq` triple-fault (`§2.1`). Those are on the milestone list (`impldoc/smp_milestones_and_verification.md §M2`, `§M4`) but are kernel-side bugs, not scheduler design items.

---

## 2. Component Map: upstream vs. gooos

| Concern | Upstream 0.40.1 supplies | gooos supplies (via patch or kernel code) |
|---|---|---|
| `const numCPU` for x86_64 | — | Patch: `runtime_gooos.go` sets `const numCPU = 17`; see §3.1 |
| Per-CPU current task | `cpuTasks [numCPU]*task.Task` (`scheduler_cores.go:22`) | Linkname `currentCPU()` → gooos `cpuID()` |
| `NumCPU()` API | `scheduler_cores.go:93-96` | — |
| Per-CPU runqueue | ✗ — upstream has single global `runqueue` + `schedulerLock` (`scheduler_cores.go:26, 43, 89`) | Patch on `scheduler_cores.go`: `var runqueues [numCPU]task.Queue`; retarget upstream push sites `scheduleTask` (line 43) and `Gosched` (line 89) to route per-CPU |
| `stealWork()` peer-scan | — | Patch on `scheduler_cores.go`: round-robin, `PopTail()` on peer queues (same algorithm as `impldoc/smp_kernel_scheduler.md §5`) |
| Per-CPU system stack | ✓ under cores: `scheduler_cores.go:292 var systemStack [numCPU]uintptr` + `systemStackPtr()` accessor (line 298-300) | Wave 1: keep `systemStacks [numCPU]uintptr` patch on `task_stack_amd64.go`. Wave 2: widen `task_stack_amd64.go` build tag to include cores, consume upstream `systemStackPtr()` via `//go:linkname runtime_systemStackPtr runtime.systemStackPtr` (mirror `task_stack_tinygoriscv.go:12-13`) |
| Queue spinlock | Upstream `queue.go:14-18` wraps Push with `lockAtomics()`. In cores mode `lockAtomics` uses `atomicsLock.Lock()` — cross-core safe | Keep gooos per-Queue spinlock shim for `scheduler=tasks` intermediate (Wave 1, `lockAtomics` there is per-CPU only). Remove at Wave 2 cores promotion (§4.3) |
| AP scheduler entry | — | Patch on `scheduler_cores.go`: exported `apScheduler()` that runs the scheduler loop on this AP's stack |
| Atomic ops safety | `atomics_critical.go` + `lockAtomics` (cores mode is cross-core safe via `atomicsLock.Lock()`) | Declare `atomicsLock` variable of type gooos-local `spinLock` in `runtime_gooos.go` Wave 2 (mirrors `runtime_rp2.go:297`). **Do not** redefine `lockAtomics` — upstream does |
| Futex `Wait`/`Wake` | `futex-cores.go` consumer + `lockFutex`/`unlockFutex` bodies defined at `scheduler_cores.go:268-277` | Declare `futexLock` variable of type `spinLock` in `runtime_gooos.go`. **Do not** redefine `lockFutex`/`unlockFutex` |
| Scheduler lock | `lockScheduler`/`unlockScheduler` bodies at `scheduler_cores.go:260-266` | Declare `schedulerLock` variable of type `spinLock` |
| Print lock | `printLock.Lock()` called at `scheduler_cores.go:314` | Declare `printLock` variable of type `spinLock` |
| GC stop-the-world | `gcMarkReachable()` orchestrates (`gc_stack_cores.go:16-80`); `gcPauseCore(i)` is a runtime linkname gooos must body | Supply `gcPauseCore()` body in `runtime_gooos.go` (IPI + pause ack) |
| Mutex (preemptive) | `mutex-preemptive.go` (via `!tinygo.unicore`) uses CompareAndSwap through `atomics_critical.go` | No recursion risk because gooos declares `atomicsLock` as `spinLock` (§4.4), not `Mutex` |

---

## 3. Per-CPU Dimensioning

### 3.1 `numCPU` placement

Add to the patched `runtime/runtime_gooos.go`:

```go
// Match gooos's maxCPUs (1 BSP + 16 APs). Per impldoc/smp_percpu_and_sync.md §1.3.
const numCPU = 17
```

This becomes visible to `scheduler_cores.go` at package scope. Array declarations that reference `numCPU` (upstream and gooos-added) all resolve to 17.

### 3.2 Why 17 and not CPU-count detected

`numCPU` is a **compile-time constant** upstream — array dimensions need a constant. A smaller value (say 4) would break on a 17-CPU MADT; a larger value (64) wastes ~3 KiB of `.bss` per per-CPU array (acceptable but unjustified until a use case appears). `17 = smpMaxAPs + 1` is the established gooos ceiling; every existing per-CPU array in gooos (`perCPUBlocks[17]`, `perCPUGDT[17]`, `perCPUTSS[17]`) uses it. Keep consistency.

### 3.3 Active-CPU vs. ceiling

Only `activeCPUs` slots (populated during MADT parse, `src/smp.go`) are in use at runtime; slots `[activeCPUs..numCPU-1]` hold empty queues, null tasks, unused stacks. `stealWork()` iterates `[0..activeCPUs-1]`, not `[0..numCPU-1]`, to avoid probing empty slots. Ceiling is for array dimensioning; `activeCPUs` gates all scan loops.

---

## 4. Scheduler Loop Composition

### 4.1 M0/M1 mode: `scheduler=tasks` on 0.40.1

- Runtime file: `runtime/scheduler_cooperative.go`.
- gooos patch retargets the existing scheduler hunks (currently against 0.33.0 `runtime/scheduler.go` lines 606–827 of `scripts/tinygo_runtime.patch`) to `scheduler_cooperative.go` line ranges.
- Behaviour unchanged from 0.33.0: BSP runs the single `runqueue` + `sleepQueue` globals; APs spin in `waitForEvents` or idle via IPI.
- **Purpose of this stage:** isolate "does 0.40.1 even build and boot" from "is the cores scheduler correct". If M0/M1 regress, the fault is in the rebase, not in cores semantics.

### 4.2 M3 mode: `scheduler=cores` on 0.40.1

- Runtime file: `runtime/scheduler_cores.go`.
- gooos patch additions on top of upstream:
  - `var runqueues [numCPU]task.Queue` — per-CPU runqueue (upstream tracks `cpuTasks` single pointer but no queue).
  - `stealWork(self uint32) *task.Task` — round-robin peer `PopTail()`.
  - `apScheduler()` — exported entry invoked from `src/smp.go:apEntry` after per-CPU init completes.
- BSP continues through upstream's `run()` → `scheduler()` path; the scheduler call becomes `scheduler(cpuID())` (or uses `currentCPU()` internally).
- Channel ops (`chan.go:resumeRX/resumeTX`) push to `runqueues[currentCPU()]` instead of a single global.

### 4.3 Queue lock under two modes

- On `scheduler=tasks` (M0/M1): upstream `Queue` uses `lockAtomics()` which in cooperative mode is just `interrupt.Disable()`. That is **per-CPU only**, so gooos keeps the explicit per-Queue spinlock it added in 0.33.0 (`gooos_spinlockAcquire/Release`) for the rare case where an AP might still enter the scheduler despite `bspBootDone` gating.
- On `scheduler=cores` (M3+): `lockAtomics()` becomes the cores variant (`atomicsLock.Lock() + interrupt.Disable()`), which *is* cross-core safe. gooos's explicit Queue spinlock becomes redundant and should be removed from the patch at the M3 cut-over (see `impldoc/runtime_patches.md §Queue`).

### 4.4 `atomicsLock` spinlock declaration (design eliminates recursion)

Upstream RP2 targets resolve the recursion risk by declaring `atomicsLock` (and the other runtime-internal locks) as a target-local `spinLock` struct — not as `task.Mutex`. See `src/runtime/runtime_rp2.go:293-299`:

```go
var (
    printLock     = spinLock{id: 20}
    schedulerLock = spinLock{id: 21}
    atomicsLock   = spinLock{id: 22}
    futexLock     = spinLock{id: 23}
)
```

`spinLock.Lock()` never calls `CompareAndSwap`, so the recursion path `Mutex.Lock → CompareAndSwap → lockAtomics → Mutex.Lock` does not exist.

**gooos design decision.** Mirror this pattern in `runtime_gooos.go` (Wave 2):

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

Backed by `src/stubs.S` `spinlockAcquire`/`spinlockRelease` assembly (already present for gooos SMP v2 — `impldoc/smp_percpu_and_sync.md §4.2`).

**M1 probe (smoke, not recursion-detector).** The M1 Exit gate still runs a boot-time `atomic.AddUint32` + `CompareAndSwap` probe (`impldoc/smp_milestones_and_verification.md §M1`), but its purpose is to catch unrelated bugs (linker error, stub missing, etc.). A probe failure no longer signals recursion — the recursion path was design-eliminated.

---

## 5. AP Scheduler Entry

### 5.1 Upstream: no AP entry hook

`scheduler_cores.go` does not expose a function that a non-BSP core can call to "join the scheduler pool". Upstream's RP2040 / RP2350 targets start secondary cores from within `runtime.run()` itself; gooos's kernel-driven bring-up (trampoline → `apEntry`) is a different shape.

### 5.2 gooos addition (same as current 0.33.0 design)

Per `impldoc/smp_kernel_scheduler.md §7.3`, gooos already defines:

```go
// runtime/scheduler_cores.go (added by patch)
func apScheduler() {
    // System stack for this AP = current stack (AP boot stack).
    // Saved into systemStacks[currentCPU()] on first resume().
    scheduler()
}
```

gooos-side bridge (unchanged from existing design, `src/smp.go`):

```go
//go:linkname apSchedulerEntry runtime.apScheduler
func apSchedulerEntry()
```

### 5.3 Call order in `apEntry`

The call sequence is already established:
1. `percpuInitAP(apIndex)` — set `%gs:0`.
2. `gdtInitPerCPU(apIndex+1)` — load per-CPU GDT + TSS.
3. `lapicTimerInitAP()` — start 100 Hz periodic (blocked on §M2 fix).
4. `apSchedulerEntry()` → `runtime.apScheduler()` → `scheduler()`.

No change from `impldoc/smp_ap_safety_overview.md §3` work plan item 2.

### 5.4 Boot gate: `bspBootDone`

Preserve existing boot-phase gating. APs spin on `bspBootDone == 0` via `gooosPause()` **before** calling `apSchedulerEntry()`. Gate is released by BSP immediately before `setupUserspace()` (`src/main.go`). No change from `impldoc/smp_ap_safety_overview.md §2.1`.

### 5.5 Gate release under `scheduler=cores`

Under cores mode, the instant BSP flips `bspBootDone = 1`, all APs wake from their spin and enter `scheduler()`. Each AP starts with an empty `runqueues[cpuID()]`; the first work arrives either via `stealWork()` (from BSP's queue) or via a channel-side push targeting that CPU.

---

## 6. Work Stealing under cores

### 6.1 Algorithm (unchanged from `impldoc/smp_kernel_scheduler.md §5`)

```go
func stealWork(self uint32) *task.Task {
    for i := uint32(1); i < activeCPUs; i++ {
        peer := (self + i) % activeCPUs
        t := runqueues[peer].PopTail()
        if t != nil {
            return t
        }
    }
    return nil
}
```

- O(n) scan over `activeCPUs` peers (not `numCPU` — slots beyond `activeCPUs` are always empty).
- `PopTail()` steals from the tail end; owner CPU's `Pop()` operates on the head. Reduces cache-line ping-pong.
- `runqueues[peer].PopTail()` takes the queue's lock (either upstream `lockAtomics()` in cores mode or gooos explicit spinlock in tasks mode) for a short critical section.

### 6.2 When `stealWork` runs

From the scheduler loop:
```go
for !schedulerDone {
    // sleep/timer queue progress, unchanged
    t := runqueues[cpuID()].Pop()
    if t == nil {
        t = stealWork(cpuID())
    }
    if t == nil {
        waitForEvents() // sti; hlt; cli
        continue
    }
    t.Resume()
}
```

### 6.3 Steal frequency and CPU-time budget

Stealing only happens when `runqueues[cpuID()]` is empty. In steady state (BSP-loaded workload, APs idle), stealing succeeds on first iteration and APs execute stolen tasks. No rate limiting needed; 17-peer scan completes in <200 cycles.

---

## 7. GC Composition

### 7.1 `gc_stack_cores.go` expectations

`scheduler_cores.go` + `gc_stack_cores.go:16-80` (`gcMarkReachable`) provide a stop-the-world protocol:
1. Set `gcScanState = 1`.
2. Iterate `[0..numCPU-1]`, call `gcPauseCore(i)` for each peer.
3. Scan current stack + globals.
4. Scan each paused peer's stack.
5. Resume peers.

### 7.2 gooos-side linkname body: `gcPauseCore`

Add to `runtime_gooos.go`:

```go
//go:linkname gcPauseCore runtime.gcPauseCore
func gcPauseCore(cpu uint32) {
    // Send IPI to target CPU; wait for pause ack (per-CPU flag set by handler).
    lapicSendIPI(uint8(perCPUBlocks[cpu].apicID), vectorGCPause)
    for atomic.LoadUint32(&perCPUBlocks[cpu].gcPaused) == 0 {
        gooosPause()
    }
}
```

Kernel-side handler (new, in `src/smp.go` or a new `src/gc_ipi.go`):
- Registered on `vectorGCPause`.
- Sets `perCPUBlocks[cpu].gcPaused = 1`.
- Spins on `gcScanState == 1` until BSP sets `gcScanState = 0`.
- Clears `gcPaused = 0`.

### 7.3 `vectorGCPause` allocation

Reserve a new IPI vector (suggestion: `0xFB`, one below the existing wakeup vector `0xFC`). Add to `src/ipi.go` vector table.

### 7.4 Scope

GC stop-the-world is a milestone-M5 item (`impldoc/smp_milestones_and_verification.md`). The design is captured here because it composes into the scheduler's overall correctness story; the M5 milestone document carries the verification plan.

---

## 8. Channel Wakeup Routing

### 8.1 Upstream behaviour in cores mode

Upstream `chan.go:resumeRX/resumeTX` push the unblocked task through `scheduleTask(t)` (`scheduler_cores.go:37-57`), which in turn takes `schedulerLock` and pushes to the **single global** `runqueue`. It does **not** route per-CPU. Under upstream's model, any CPU pulls the next task from the same global queue under `schedulerLock` contention.

**gooos decision:** retain the per-CPU `runqueues[numCPU]` + `stealWork` design captured in `impldoc/smp_kernel_scheduler.md §§2–5`. This means patching upstream `scheduleTask` and `Gosched` push sites to route per-CPU (§2 table).

### 8.2 `chan.go` patch — retained at both waves

The gooos patch on `chan.go` (`scripts/tinygo_runtime.patch` lines 232-257) replaces the push through `scheduleTask` / `runqueue.Push` with `runqueues[gooosCpuID()].Push`. This is required at both waves:

- **Wave 1 (tasks mode).** Upstream `scheduler_cooperative.go` also uses a single global `runqueue` — gooos patch routes per-CPU.
- **Wave 2 (cores mode).** Upstream `scheduler_cores.go` still uses a single global `runqueue` (see §8.1) — gooos patch routes per-CPU.

The `chan.go` hunks do **not** drop at Wave 2. `impldoc/runtime_patches.md §3.4` captures the retention decision.

### 8.3 Cross-CPU wakeup IPI

Optional — documented in `impldoc/smp_kernel_scheduler.md §6.3/6.4` as a latency optimization. No change for migration. Deferred per existing design.

---

## 9. Sleep / Timer Queue

### 9.1 Upstream state

`scheduler_cooperative.go:38-42` keeps `sleepQueue` + `sleepQueueBaseTime` as single globals. `scheduler_cores.go` does not split them per-CPU either — upstream accepts a global `sleepQueue` protected by `lockAtomics()`.

### 9.2 gooos composition

Per `impldoc/smp_kernel_scheduler.md §12 Open Questions #1`, gooos adopts the "global + `schedLock`" strategy. Existing gooos patch on 0.33.0's `scheduler.go` already wraps `sleepQueue`/`timerQueue` mutations with `schedLock.Acquire/Release`. In 0.40.1 this relocates to `scheduler_cooperative.go` (M0/M1) or `scheduler_cores.go` (M3+). File paths change; semantics identical.

---

## 10. Call / Link Graph (M3 steady state)

```
src/smp.go:apEntry(apIndex)
  └─ percpuInitAP(apIndex)                 // gooos kernel, gs base
  └─ gdtInitPerCPU(apIndex+1)              // gooos kernel, per-CPU GDT/TSS
  └─ lapicTimerInitAP()                    // gooos kernel (pending M2)
  └─ for bspBootDone == 0 { gooosPause() } // gooos kernel gate
  └─ apSchedulerEntry()
       └─ linkname → runtime.apScheduler()
            └─ scheduler()                 // scheduler_cores.go
                 ├─ runqueues[currentCPU()].Pop()
                 ├─ stealWork(currentCPU()) on nil
                 ├─ waitForEvents() on all-nil
                 └─ t.Resume()
                      └─ swapTask(s.sp, &systemStacks[currentCPU()])
                           └─ gooosOnResume()  // gooos kernel (TSS.RSP0)
```

Cross-cut hooks called by scheduler/runtime during goroutine life:

- `lockAtomics` / `unlockAtomics` — defined upstream at `scheduler_cores.go:281-290`. gooos supplies only the `atomicsLock` variable (type `gooosSpinLock`, §4.4).
- `lockFutex` / `unlockFutex` — defined upstream at `scheduler_cores.go:268-277`. gooos supplies only the `futexLock` variable.
- `lockScheduler` / `unlockScheduler` — defined upstream at `scheduler_cores.go:260-266`. gooos supplies only the `schedulerLock` variable.
- `systemStackPtr()` — defined upstream at `scheduler_cores.go:298-300` (returns `&systemStack[currentCPU()]`). At Wave 2, gooos consumes it via `runtime_systemStackPtr` linkname import in `task_stack_amd64.go`.
- `interrupt.Disable` / `interrupt.Restore` — gooos `runtime/interrupt/interrupt_gooos.go` (unchanged).
- `interrupt.In` — gooos `interrupt_gooos.go`. Current implementation returns `false` (per `impldoc/smp_deferred_and_known_issues.md §1`); retained as-is post-migration.
- `gcPauseCore` — **new** gooos body, described in §7.2. (This one truly needs a new function body because upstream RP2 supplies it target-locally via FIFO interrupts — x86_64 uses IPIs.)
- `currentCPU` → `cpuID()` — linkname (unchanged).

---

## 11. Known risks (captured from parent design)

Carried forward from `impldoc/smp_overview.md §6 Risk Register` and `impldoc/smp_kernel_scheduler.md §13`:

| ID | Risk | Likelihood | Impact | Mitigation in migration |
|---|---|---|---|---|
| R-fork-divergence | Upstream evolves; patch drift | Medium | High | Staged promotion (tasks → cores) keeps a bisect point |
| R-atomicslock-recursion | `atomicsLock.Lock()` re-enters `lockAtomics` | Low | High | M1 Exit probe; contingent spinlock shim |
| R-ap-gc | AP mid-mutation during GC | Medium | High | M5 milestone: implement `gcPauseCore` IPI + ack |
| R-sleepqueue-race | AP `addSleepTask` concurrent with BSP | Low | Medium | Keep `schedLock` global spinlock from existing design |
| R-ap-ring3-triple-fault | Kernel bug, orthogonal | High | High | M4 milestone (QEMU+GDB debug) |
| R-ap-lapic-timer-race | Kernel bug, orthogonal | High | Medium | M2 milestone (per-CPU counter migration) |
| R-numcpu-static | `numCPU = 17` rules out >16 AP machines | Low | Low | Accepted; align with existing gooos constant |

---

## 12. Not covered here

- AP bring-up sequence (ACPI MADT, INIT-SIPI-SIPI) — `src/smp.go`, unchanged. See `impldoc/smp_percpu_and_sync.md §3`.
- Per-CPU TSS / RSP0 update on Ring-3 resume — `src/goroutine_tss.go` `gooosOnResume`, unchanged.
- User-space SMP (Ring-3 goroutines on APs) — `impldoc/smp_user_multicore.md`, unchanged design; blocked by `impldoc/smp_deferred_and_known_issues.md §2.1`.
- User-side TinyGo migration — `user/target.json` carries `!kernelspace`; same 0.40.1 migration applies mechanically (covered in `impldoc/toolchain_switch_plan.md`).

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
