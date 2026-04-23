# Scheduler, Runtime Integration, and Kernel-Thread Abstraction — Delta

**Scope:** supersedes `current_impl_0421_night/04_scheduler_runtime_preemption.md` **§Preemption Configuration Gates**; extends the baseline with a new **§Kernel-Thread Abstraction** covering Phases 4.1–4.3. Baseline sections on Runtime Model, Runtime-Kernel Integration Points, Kernel Preemption Path, Ring 3 Preemption, and Scheduler-Visible Invariants remain authoritative.

## Summary of Changes Since `a384b1a`

1. `src/preempt_config.go` grew from 4 to 9 flags. Commits: `7f22b5c`, `39ed4e0`, `af9cb8f`, `4a0337c`, `7128c4e`.
2. New `src/kernel_thread.go` (Phases 4.1 → 4.3): per-CPU FIFO ready queues, `KernelThread` struct, `SavedContext` stubs, `kernelYield()`. Commits: `69029f2 e31b2bc 961cb90 3489340 f094316 9fe86e5 051cef1`.
3. `kernelYield()` is now called inside two long-lived kernel service loops: `timerDispatcher` in `src/afterticks.go:119` and `netRxLoop` in `src/net.go:73`. Commits: `f094316`, `9fe86e5`.
4. `netRxLoop` is now also registered via `kernelThreadSpawn(0, netRxLoop)` in `src/net.go:52` (parallel to the existing `go netRxLoop()` launch).
5. `src/keyboard_irq.go:110` uses `gooosSchedulerYield()` on APs and `sti();hlt()` only on BSP — see `07_keyboard_irq_ring.md` for the wake-path refactor.

## Current Design

### Preemption Configuration Gates — full current flag matrix (`src/preempt_config.go`)

Supersedes the baseline's 4-flag list. All flags are `const` compile-time booleans. A harness flips one flag via `sed`, rebuilds, runs, and reverts.

| Flag | Default | Purpose | Harness |
|---|---|---|---|
| `preemptEnabled` | `true` | Master gate for feature 2.1 (BSP broadcasts preempt IPI on every 100 Hz tick). | — (always true; rollback via source flip) |
| `runPreemptProbe` | `false` | Spawns `kpHog` + `kpMarker` kernel goroutines from `bootActivatePostShellReady`. BSP-only; monopolizes CPU. | `scripts/test_preempt_kernel.sh` |
| `runUserPreemptProbe` | `false` | Auto-loads `userpreempt.elf` via `bspBootDone`. | `scripts/test_preempt_user.sh` |
| `runSMPShellPreemptProbe` | `false` | Auto-launches `cpuhog.elf` + `markerprint.elf` from `bspBootDone`, bypassing HMP sendkey. Also turns on `APIDSTAT`/`PRESTAT` diagnostic dumps during `processExit`. | `scripts/test_smp_shell_preempt.sh` |
| `runSMPBasicProbe` | `false` | Spawns `smpBasicProbe` kernel goroutine from `bootActivatePostShellReady` — a kernel-side sanity probe for AP runqueue distribution. | (embedded in SMP distribution harnesses) |
| `runSMPProbeShellTest` | `false` | Writes `.autorun.sh` at boot so `smpprobe.elf` runs under the real shell parser; **also forces `handleLAPICTimer` to skip preempt fanout** so grep markers aren't perturbed. | `scripts/test_smp_shell_smpprobe.sh` |
| `runGoprobeTest` | `false` | Same pattern for `goprobe.elf`. | `scripts/test_goprobe_shell.sh` |
| `runSleeputestTest` | `false` | Same pattern for `sleeptest.elf` (diagnostic for user `sys_sleep` hang). | `scripts/test_sleeptest_shell.sh` |
| `runYieldtestTest` | `false` | Same pattern for `yieldtest.elf` (confirms `sys_yield` works at Ring 3 under SMP even when `sys_sleep` does not). | (manual; harness pending) |

Cross-effect: when `runSMPProbeShellTest == true`, `handleLAPICTimer` at `src/lapic_timer.go:91` short-circuits before calling `broadcastPreemptIPI`. This is intentional — the autorun harness validates raw `scheduler=cores` work-stealing, not preempt behavior.

### Kernel-Thread Abstraction (Phases 4.1–4.3)

Orthogonal to the TinyGo scheduler. Gives kernel-internal services a **per-CPU, cooperative, FIFO** execution slot independent of goroutine migration. Today this is an *invocation path*, not a context-switching runtime.

#### Data model (`src/kernel_thread.go`)

- `KernelThread{cpuID, entryFn, state, nextReady, context SavedContext}` — `:29–36`.
- `SavedContext` — `:20–27` — holds all general-purpose registers + `rip/rsp`. **Unused in Phase 4.3** (Phase 4.4 will switch to these for preemption-safe yielding).
- `ThreadState` enum: `ThreadReady, ThreadRunning, ThreadBlocked, ThreadTerminated` (`:41–46`).
- `kernelReadyQueues [maxCPUs]*KernelThread` — singly-linked FIFO per CPU (`:52`).
- `currentKernelThread [maxCPUs]*KernelThread` — per-CPU currently-running slot (`:126`).

#### API

- `kernelThreadInit()` (`:58`) — no-op; call-site placeholder in `src/main.go:367`.
- `kernelThreadSpawn(cpuID uint32, fn func())` (`:68`) — allocates a `KernelThread`, appends to `kernelReadyQueues[cpuID]`. Safe to call pre-SMP (but intended for post-init). **Rejects `cpuID >= maxCPUs` and `fn == nil`** with a `serialPrintln` and return.
- `kernelThreadGetReady()` (`:101`) — peeks the current CPU's head.
- `kernelThreadPopReady()` (`:111`) — dequeues the current CPU's head and marks it `ThreadRunning`.
- `kernelThreadSwitch(next *KernelThread)` (`:132`) — **Phase 4.3: direct invocation.** Calls `next.entryFn()` synchronously; does not return until the function returns. `SavedContext` is not touched.
- `kernelYield()` (`:143`) — pops one ready thread for the current CPU (if any) and calls `kernelThreadSwitch` on it. If none, returns immediately.

#### Call sites (current)

| Producer | Call | Purpose |
|---|---|---|
| `src/net.go:52` | `kernelThreadSpawn(0, netRxLoop)` | Queue `netRxLoop` on CPU 0 (in addition to `go netRxLoop()`). Intent: Phase 4.4 will drop the goroutine and run via `kernelYield`. |
| `src/afterticks.go:119` | `kernelYield()` | Inside `timerDispatcher`'s per-iteration `runtime.Gosched()` sibling — drains any ready kernel thread on the current CPU. |
| `src/net.go:73` | `kernelYield()` | Same pattern inside `netRxLoop`'s per-iteration poll. |

Because `kernelThreadSwitch` is direct invocation, the first `kernelYield()` call on CPU 0 pops `netRxLoop` and **calls it inline** — which loops forever. This is benign: `netRxLoop` already runs forever as a goroutine, so the inline call never returns control to the outer `timerDispatcher`/`netRxLoop` site that invoked `kernelYield`, **but that caller is itself an infinite loop**. In effect the ready-queue slot is drained exactly once per boot and the kernel-thread path becomes a no-op thereafter.

This is the design intent of Phase 4.3: land the data structures and call sites without changing runtime semantics. Phase 4.4 (not yet implemented) is where `SavedContext` + a real context switch will change behavior.

## Current Implementation Details

- **Lazy stacks:** commented out at `src/kernel_thread.go:49`. `kernelStackSize = 16 * pageSize` is defined but no stack is actually allocated in 4.3.
- **ISR safety:** `kernelThreadSpawn` takes no locks (uses linked-list append on a per-CPU slot). Commented as "Safe to call from interrupt context". Allocates a `&KernelThread{}` — this is a runtime allocation and the ISR-safety lint (`scripts/lint_isr.go`) would currently flag any ISR-reachable path into it. Today the only call site is `netInit` (non-ISR), so no lint violation exists.
- **No scheduling fairness:** threads run to completion via direct invocation. Any thread that loops forever (like `netRxLoop`) permanently claims the queue slot on its CPU.

## Diff-from-Baseline Notes

- Baseline `§Preemption Configuration Gates` listed 4 flags. The current list is 9 (above). Baseline was accurate for the 2026-04-21 snapshot; the additions are all 2026-04-22 / -23 harness scaffolding (see the commit list above).
- Baseline §Runtime Model (`scheduler = cores`, `gc = conservative`) is unchanged.
- Baseline §Kernel Preemption Path is unchanged at the ISR level — the preempt IPI still goes through `handlePreemptIPI` → safe-point gates → `gooosSchedulerYield`. What changed is the *origination* gate in `handleLAPICTimer` — see `03_smp_preempt_phase_gating.md`.
- Baseline §Ring 3 Preemption and Signal Delivery Path is unchanged.
- Baseline §Scheduler-Visible Invariants are unchanged; additional invariants: `kernelReadyQueues[cpu]` head is only mutated on its own CPU (pop) or at init (spawn); since Phase 4.3 has no cross-CPU dequeue, no lock is needed.

## Open Questions / Known Gaps

- Phase 4.4 (context switching via `SavedContext` + a stack-swap stub) is not landed. Until it is, `kernelYield()` at `src/kernel_thread.go:143` is a one-shot drain — not a true yield.
- The `kernelThreadSpawn` allocation path is an `ISR-unsafe` allocation; if a future caller spawns from an ISR, the lint will catch it, but there is no runtime assertion.
- The "kernel thread" abstraction overlaps conceptually with TinyGo's own task queues. The intent (per commit message and in-source comments) is to provide **deterministic** per-CPU scheduling that work-stealing cannot perturb. Until Phase 4.4, this intent is unproven.
