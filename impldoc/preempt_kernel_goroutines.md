# Kernel Goroutine Preemption (feature 2.1)

## Scope

Turn kernel goroutines from **cooperative** to **preemptive** so that a CPU-bound kernel goroutine cannot monopolize a core. Implementation targets the patched TinyGo 0.40.1 runtime already shipping on branch `smp-take3` (commit `dc58dbc`), where per-CPU runqueues and `stealWork()` are live but APs only yield at cooperative points or IPI-arrival boundaries.

Out of scope: user-space preemption (see `preempt_user_goroutines.md`); per-CPU AP LAPIC timer (see `## Future: per-CPU AP timer` at the tail of this doc); SMP-safe GC pausing (deferred to M5).

## Cross-links

- `preempt_shell_overview.md` — batch entry point + Design Decisions table.
- `preempt_user_goroutines.md` — feature 2.2 composes with this (kernel preemption pauses a hosting ring3Wrapper; user preemption rotates goroutines within one ring3Wrapper).
- `shell_multicore_preempt.md` — feature 2.3 integration harness depends on this for the anti-starvation sub-gate.
- `preempt_shell_milestones_and_verification.md` — Entry/Exit gates.
- `preempt_shell_readme_update_plan.md` — doc drift rules.
- Prior SMP work: `smp_unblock_overview.md`, `smp_deferred_and_known_issues.md` (§2.2 on the `blocked inside interrupt` regression class).

## 1. Current State

- BSP runs a 100 Hz LAPIC timer (`src/lapic_timer.go:69-80 handleLAPICTimer`, vector 0xFE). The handler sets `perCPUBlocks[idx].WantReschedule = 1` at `src/lapic_timer.go:78` and issues EOI. No yield is forced; the flag is *read* by nothing today.
- APs **do not** run their own LAPIC timer (`src/smp.go:257-273` — `lapicTimerInit()` commented out with rationale). They wake from `hlt` only when an IPI arrives.
- Cross-CPU wake is already wired: `runtime_gooos.go:235-249 schedulerWake` broadcasts `gooosWakeupCPU(i)` for every `numCoresOnline` entry → `src/ipi.go:46-55` fires vector 0xFC with self-skip → `src/ipi.go:33-35 handleWakeupIPI` issues EOI, CPU exits `hlt`, scheduler loop naturally pops its runqueue and falls through to `stealWork()` at `scheduler_cores.go:217-219`.
- Cooperative context switch saves only 7 words: 6 callee-saved GPRs (rbx, rbp, r12-r15) + pc (`task_stack_amd64.go:21-30 calleeSavedRegs`). Caller-saved GPRs, RFLAGS, segment registers, and the full Ring-3 trap frame are **not** part of `state.sp`.
- Full-frame save does exist, but only in the ISR path: `src/isr.S:90-104` pushes 15 GPRs; the CPU itself stacks RIP/CS/RFLAGS/RSP/SS on entry. The ISR prologue bumps `InterruptDepth` @`%gs:4` at `:115` and conditionally bumps `SyscallDepth` @`%gs:44` at `:116-119` when vector == 0x80; the epilogue mirrors at `:138-142`.
- `interrupt.In()` (`~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go:44-47`) returns `InterruptDepth != 0 && SyscallDepth == 0`. The `SyscallDepth == 0` side is what lets `task.Pause()` run inside syscall handlers; any new preemption path must preserve this invariant or `blocked inside interrupt` panics will return (the regression class called out in `smp_deferred_and_known_issues.md §2.2`).
- Spinlock primitives at `src/stubs.S:437-459` (`spinlockAcquire`/`spinlockRelease`); runtime-side declarations `//go:noescape` (necessary per the GC-hang fix — see `scripts/tinygo_runtime.patch` / `smp_unblock_overview.md §Risk Register`).
- `runGC()` lives at `~/.local/tinygo0.40.1/src/runtime/gc_blocks.go:533`. It runs under `schedulerLock` via its callers; there is no standalone `gcRunning`/`inGC` flag to probe.

## 2. Design

### 2.1 Trigger source — BSP timer + IPI broadcast (confirmed)

Approved in the overview's Design Decisions table: keep the existing BSP-only timer + IPI-broadcast path; do not land the AP LAPIC timer in this milestone. The BSP ISR sets a global "preempt-due" signal; the IPI broadcast nudges APs so they return from `hlt` and observe it.

Mechanism:

1. `handleLAPICTimer` (`src/lapic_timer.go:76`) already sets `perCPUBlocks[cpuID()].WantReschedule = 1`. Extend the handler to *also* broadcast the same wake IPI that `schedulerWake` issues, but only when preemption is actually enabled (see the rollback flag in §2.5). The BSP itself observes `WantReschedule` in the scheduler loop (see §2.3); every AP observes it on IPI-resume.
2. APs do NOT get their own timer in 2.1; their only trigger is IPI arrival from the BSP tick. This is coarser than per-CPU timers (quantum bounded by IPI latency, not tick granularity) but preserves the `blocked inside interrupt` invariants — the AP never takes a timer interrupt.

### 2.2 Trap-frame extension

`calleeSavedRegs` (`task_stack_amd64.go:21-30`) is insufficient for a preempted task: the 9 caller-saved GPRs (rax, rcx, rdx, rsi, rdi, r8, r9, r10, r11), RFLAGS, and — for a preempted Ring-3 wrapper — the full CS/SS/RSP interrupt frame are missing.

Design: introduce a `preemptedFrame` representation distinct from `calleeSavedRegs` and tag the task's saved state with a discriminator.

```
type savedContext struct {
    kind uint8            // 0 = cooperative (calleeSavedRegs); 1 = preempted (preemptedFrame)
    _pad [7]byte
    // One of the two is valid based on kind:
    coop calleeSavedRegs  // existing path
    pre  preemptedFrame
}

type preemptedFrame struct {
    // Same 15-GPR order as src/isr.S:90-104 pushes them.
    rax, rbx, rcx, rdx, rsi, rdi, rbp uintptr
    r8, r9, r10, r11, r12, r13, r14, r15 uintptr
    // CPU-stacked on ring transition (see isr.S):
    rip    uintptr
    cs     uintptr
    rflags uintptr
    rsp    uintptr
    ss     uintptr
}
```

`swapTask` (assembly in `task_stack_amd64.S`) must branch on `kind`:

- `kind == 0` — existing cooperative path; pop the 6 callee-saved GPRs + ret via `pc`.
- `kind == 1` — new preempted path; pop all 15 GPRs, then `iretq` consuming RIP/CS/RFLAGS/RSP/SS from the frame.

The preempt ISR writes `kind = 1` and fills `preemptedFrame` from the stacked ISR context (`src/isr.S:90-128` already has all 15 GPRs + vector/error on the kernel stack; the CPU-stacked 5-word interrupt frame sits just above). The ISR then calls a new runtime entry (see §2.3) which *does not return* — it calls the scheduler, which picks a runnable task and resumes it via the discriminator branch.

The cooperative path is unchanged, so existing goroutines that `Gosched()` / `task.Pause()` voluntarily pay zero extra cost.

### 2.3 Safe-point policy and `preemptDisable` counter

Preemption is inhibited ("preempt-disabled") in any of the following regions:

| Region | Disable mechanism |
| --- | --- |
| Inside `spinlockAcquire`/`spinlockRelease` critical section (`src/stubs.S:437-459`) | Each Spinlock.Acquire bumps `preemptDisable`; Release decrements. |
| Inside `runGC()` (`gc_blocks.go:533`) | `schedulerLock` is held transitively; schedulerLock.Lock bumps `preemptDisable`. |
| Between `cli` and `sti` (`interrupt.Disable()` / `Restore()`) | Hardware IF is 0, so the CPU itself cannot take the timer interrupt. `preemptDisable` not needed but kept symmetrical. |
| ISR prologue/epilogue (`src/isr.S:88-149`) | `InterruptDepth > 0` is the signal; the new preempt ISR returns without scheduling when it would re-enter itself. |
| Any `//go:nosplit` function boundary | The compiler guarantees no stack growth; preempt-disable is enforced at the caller boundary, not inside. |

New per-CPU field `PreemptDisable uint32` added to `PerCPU` (`src/percpu.go:22-33`) at offset **56** (after current `_pad [16]byte`; struct grows to 64-byte boundary naturally because the original pad was oversized). Assembly-visible offset constant `pcpuOffPreemptDisable = 56` appended to the const block at `src/percpu.go:36-46`.

The preempt ISR checks three conditions and returns early (no reschedule) if any is true:

1. `gs:InterruptDepth > 1` — nested interrupt; let the outer frame decide.
2. `gs:PreemptDisable > 0` — critical section; set `WantReschedule` and let the critical-section exit path observe it (see `Spinlock.Release` below).
3. current task is currently running an `//go:nosplit` frame — detected by the Go compiler emitting a `preemptFrame` symbol range; if saved RIP falls inside any such range, return early. (Ranges exported by TinyGo; requires a runtime linkname additions `runtime.gooosIsNosplitRIP(rip uintptr) bool` backed by a pre-built sorted table.)

`Spinlock.Acquire`/`Release` in `src/spinlock.go` gain `preemptDisable` bump/drop; `Release` additionally checks the flag and, if `WantReschedule == 1 && PreemptDisable == 0`, calls `runtime.Gosched()` (voluntary hand-off — the preempt trap frame representation is not needed on this path because the caller is at a natural Go-level boundary).

### 2.4 Fairness + interaction with `stealWork`

Fairness is per-CPU round-robin: the runqueue `Pop()` at `scheduler_cores.go:217` is already FIFO. A preempted task goes back to the *same* CPU's runqueue tail (`runqueues[gooosCpuID()].Push(t)`) — not to the global queue or a peer queue — so stealWork behaves identically.

Races to consider:

- **Self-steal after preempt**: A preempted task is pushed to the local tail; `stealWork` peers scan other CPUs first. No self-steal is possible because `stealWork` starts at `me+1` (`scheduler_cores.go:132-134`).
- **Peer steals during preempt ISR**: The ISR runs atomically with IF=0; the queue is unlocked only after the task's context is fully saved. A peer that wakes mid-ISR observes an empty local queue until the ISR completes.
- **Preempt ISR observes a queue that `stealWork` just emptied**: Benign — the ISR checks `runnable != nil` after Pop+stealWork and resumes the original task if both return nil.

### 2.5 Rollback flag

Single kernel-global constant gate: `preemptEnabled bool` in a new file `src/preempt_config.go`. When `false`, `handleLAPICTimer` still sets `WantReschedule` but does not broadcast the preempt-wake IPI; the preempt ISR vector is never raised on APs (because the IPI is not sent). BSP's own scheduler loop still observes `WantReschedule` on its next cooperative pass, so behavior degrades gracefully to "cooperative + reschedule hint on BSP" — the pre-preemption baseline.

Rollback commit: flip the constant in one line, rebuild, re-run regression matrix. No code changes elsewhere needed.

## 3. Commit-per-edit Plan

1. `feat(smp): add PreemptDisable per-CPU field at gs:56` — `src/percpu.go` struct + offset const. Build-only commit; no behavioral change.
2. `feat(smp): wire preemptDisable into Spinlock and schedulerLock` — `src/spinlock.go` + the patched `runtime_gooos.go` spinlock usage. Counter bumps with no ISR consumer yet.
3. `feat(smp): preemptedFrame + savedContext discriminator in TinyGo runtime` — `~/.local/tinygo0.40.1/src/internal/task/task_stack_amd64.go` + `task_stack_amd64.S`. Adds the new frame type and discriminator; cooperative path unchanged. Build-only; no preempt path fires yet.
4. `feat(smp): preempt ISR vector + handler` — new vector 0xFB (below `ipiWakeupVector=0xFC`), new IDT gate entry in `src/idt.go`, new handler skeleton in `src/goroutine_irq.go` that pushes a preempted frame and calls a new runtime entry `runtime.gooosPreempt(frame)`. Still gated off by `preemptEnabled=false`.
5. `feat(smp): BSP timer broadcasts preempt IPI` — `src/lapic_timer.go:76 handleLAPICTimer` gains an `if preemptEnabled { broadcastPreemptIPI() }` tail. `broadcastPreemptIPI` is modeled on `schedulerWake` but uses vector 0xFB. Still gated off.
6. `feat(smp): nosplit-RIP range table exported from TinyGo` — patched-runtime addition; `runtime.gooosIsNosplitRIP(rip) bool`. Extend `scripts/patch_tinygo_runtime.sh` post-conditions.
7. `feat(smp): enable kernel preemption` — flip `preemptEnabled = true` in `src/preempt_config.go`. **This is the risk commit.** Verify with `scripts/test_preempt_kernel.sh` + full regression matrix under `-smp 1` and `-smp 4`.
8. `test(smp): harness for kernel preempt` — `scripts/test_preempt_kernel.sh` spawns two BSP-scheduled kernel goroutines where goroutine A runs a tight `for {}` loop and goroutine B periodically prints a marker. PASS = ≥ 5 markers observed within 5 s; FAIL = none observed (goroutine A starved it).

## 4. Per-File Edits

Kernel (`/home/ryo/work/gooos/src/`):
- `percpu.go:22-33,36-46` — append `PreemptDisable uint32` field + `pcpuOffPreemptDisable = 56` offset const.
- `spinlock.go` — bump/drop `PreemptDisable` in `Acquire`/`Release`; in `Release`, check `WantReschedule && PreemptDisable == 0` → voluntary `runtime.Gosched()`.
- `lapic_timer.go:76-80` — append `if preemptEnabled { broadcastPreemptIPI() }` after `WantReschedule` set and before `lapicSendEOI`.
- `ipi.go` — add `broadcastPreemptIPI()` modeled on `schedulerWake` but using a new `ipiPreemptVector = 0xFB`.
- `idt.go` — IDT gate for vector 0xFB pointing at new ISR stub in `isr.S`.
- `isr.S` — new `isr_preempt` entry that pushes the 15 GPRs + discriminates from `isr_common`; branches to `handlePreemptIPI` in Go.
- `goroutine_irq.go` — `handlePreemptIPI(frame *preemptedFrame)`: check the 3 safe-point conditions, early-return if any true, else populate `preemptedFrame` on the task's saved-state slot, set `kind=1`, enqueue, call `runtime.Gosched()`.
- `preempt_config.go` (NEW) — `const preemptEnabled = true` (or `false` initially).

Patched TinyGo (`~/.local/tinygo0.40.1/src/`):
- `internal/task/task_stack_amd64.go:21-30` — introduce `savedContext` wrapping `calleeSavedRegs` + new `preemptedFrame`.
- `internal/task/task_stack_amd64.S` — branch in `swapTask` based on `kind`.
- `runtime/runtime_gooos.go` — add `gooosPreempt` linkname entry that delegates to the scheduler; add `gooosIsNosplitRIP` backed by the compiler-emitted range table.
- `runtime/scheduler_cores.go:217-220` — unchanged; preempted tasks self-enqueue via the ISR path.

Patch surface:
- `scripts/tinygo_runtime.patch` regen after above TinyGo edits.
- `scripts/patch_tinygo_runtime.sh` — new post-condition greps (`grep -q 'kind uint8' task_stack_amd64.go`, `grep -q 'gooosIsNosplitRIP' runtime_gooos.go`).

## 5. Entry Criteria

- `smp-take3` at or ahead of `dc58dbc` (scheduler=cores + stealWork live).
- `make build && make lint && make verify-globals` clean.
- Full regression matrix (`test_net.sh`, `test_tcp_phase{1..5}.sh`, `test_smp_basic.sh`, `test_gochan.sh`, `test_pipe_matrix.sh`) PASS under `-smp 1` and `-smp 4`.

## 6. Exit Criteria

- `scripts/test_preempt_kernel.sh` PASS under `-smp 1` and `-smp 4` (5+ marker observations within 5 s).
- Full regression matrix above remains PASS under both `-smp 1` and `-smp 4`.
- No `blocked inside interrupt` panic in any run of any harness.
- `grep -n 'preemptEnabled' src/preempt_config.go` shows `true`.

## 7. Rollback

Primary: `sed -i 's/preemptEnabled = true/preemptEnabled = false/' src/preempt_config.go && make build && make iso`. Timer still ticks, `WantReschedule` still set, preempt IPI not broadcast, ISR never fires. System returns to cooperative-plus-reschedule-hint baseline.

Secondary (if the rollback-flag flip is itself insufficient, e.g. a spinlock interaction is breaking even with the ISR disabled): `git revert` commits 7 → 5 → 4 in reverse order. Commits 1-3 are behavior-neutral and can stay.

## 8. Risks

- **Preempt-inside-spinlock re-entrancy (CRITICAL class)**. If `preemptDisable` bump/drop is missed in any spinlock callsite, the ISR can preempt a CPU holding `queueLock` or `schedulerLock`, resurrecting the GC-hang symptom from a different root cause than the original `//go:noescape` fix. Mitigation: audit every Spinlock callsite in a dedicated commit; reviewer bullet (m) is CRITICAL.
- **`SyscallDepth` invariant break**. The preempt ISR, if dispatched via `isr_common`, would bump `InterruptDepth` (not `SyscallDepth`), which is correct — but any routing change must preserve `interrupt.In() == InterruptDepth != 0 && SyscallDepth == 0`.
- **Nosplit-RIP range misses**. Preempting inside a `//go:nosplit` frame can corrupt the stack. The range table must be complete; missing entries = crashes. Mitigation: TinyGo compiler emits the table from the same pass that emits the nosplit check, so completeness is linked to the compiler, not hand-maintained.
- **`iretq` from a kernel-mode preempted task**. The CPU-stacked interrupt frame's CS/SS fields must be valid kernel selectors; a preempted kernel goroutine's CS is `kernelCodeSelector`, SS is `kernelDataSelector`. If a Ring-3 wrapper is the preempt target, CS/SS are the user selectors and `iretq` transitions rings — this *should* just work because the hardware-saved interrupt frame on ring transition is already compatible, but needs verification under QEMU.
- **Interaction with M4 Ring-3 fault-fix landed at commit `5aea173`**. The AP Ring-3 `iretq` path was surgery on the same hardware mechanism. 2.1's preempt path must not regress it.

## 9. Deliverables

- 8 commits per §3.
- New file `src/preempt_config.go`.
- New harness `scripts/test_preempt_kernel.sh`.
- Patch-surface updates in `scripts/tinygo_runtime.patch` + post-condition greps in `scripts/patch_tinygo_runtime.sh`.
- `TODO_SMP5.md` (new tracker file for this batch, modeled on `TODO_SMP4.md`) ticks 2.1-1..2.1-8 items as they land.

## Future: per-CPU AP timer

Preemption precision in 2.1 is bounded by IPI latency (BSP tick → IPI fanout → AP resumes from `hlt`). A future milestone can land a per-CPU AP LAPIC timer (re-enabling `lapicTimerInit()` at `src/smp.go:273`) so each AP preempts on its own 100 Hz tick. Gating: the `blocked inside interrupt` boot-hang documented in `smp_deferred_and_known_issues.md §2.2` must be root-caused first. That work is tracked separately (not in this batch) and does not block 2.1 from shipping.

Design notes for that future work:
- AP timer ISR would use the same `ipiPreemptVector` handler logic (unified code path).
- Eliminates the BSP-broadcast fanout — each AP is self-preempting — reducing IPI bus traffic under load.
- Re-opens the regression class only if the root-cause was in the ISR dispatch path itself; if root-cause was a pre-`syscall` race (now fixed by M2-2 retiring `gooos_in_interrupt_depth`), AP LAPIC timer should simply work.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
