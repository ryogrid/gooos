# 02 — Kernel-thread runtime (the central design)

This is the core Route C document. §03 builds IPC primitives on top
of what's here; §04 hooks the timer ISR into it; §05 makes the GC
aware of it; §06 migrates existing services onto it. Read this first.

## Overview

Under Route C the Ring 0 kernel runs a gooos-owned scheduler whose
unit of execution is a **kernel thread** (`KernelThread`), not a
TinyGo goroutine. Each kernel thread owns:

- a fixed 16 KiB kernel stack drawn from a gooos-managed pool;
- a saved-register block holding RBP, RBX, R12..R15, RSP, RFLAGS at
  every park point;
- a state word (runnable, parked, preempted, exiting);
- an optional `parkReason` pointer used by §03 sync primitives to
  find and wake this thread.

A **per-CPU ready queue** holds runnable threads. The scheduler loop
on each CPU pops the head and context-switches into it. When the
thread parks (on a semaphore, a timed wait, or the idle halt loop) it
re-enters the scheduler loop via the same asm stub. Preemption
(§04) works by setting `WantReschedule` on the target CPU and either
IPI-ing or letting the next safe point pick it up.

The asm context-switch stub is the single piece of new assembly.
Everything else is Go under the `//go:nosplit` discipline we already
use throughout `src/`.

## Relation to current asm

The existing asm baseline we preserve or extend:

- `src/switch.S` (`src/switch.S:1..40`) — Phase-B leftovers; only
  `taskReturnHalt` and `elfExecTrampolineAddr` survive. §02 adds a
  new context-switch stub in a *new file* (see below) and does not
  disturb switch.S, so its two remaining exports keep working for
  Ring-3 bring-up (§07).
- `src/task_stack_amd64.S` (`src/task_stack_amd64.S:1..61`) —
  byte-imported copy of TinyGo's `tinygo_startTask` /
  `tinygo_swapTask`. Route C keeps this file in place but it becomes
  kernel-linker-unused (the user-side link still needs it via
  `user/task_stack_amd64.S`; see §07). §08 describes whether to
  remove the kernel-side copy or keep it as dead link-time fodder.
  The Route C recommendation is *keep and mark dead*; a future
  cleanup pass can delete once M5 has soaked.

**PROPOSED** — new kernel-side asm file `src/kthread_switch.S`:

- Symbol `kschedSwitch(new *KernelThread, old *KernelThread)` —
  saves callee-saved registers + RFLAGS onto the current stack,
  writes the resulting RSP into `old.SavedRSP`, loads `new.SavedRSP`
  into RSP, pops the registers back, returns. Mirrors
  `tinygo_swapTask` (`src/task_stack_amd64.S:31..60`) but stores
  the saved SP in a Go struct field that gooos owns rather than in
  `task.Task.state.sp`.
- Symbol `kschedEnter(entry func(), arg uintptr) noreturn` — the
  trampoline used when a newly-spawned kernel thread is first
  scheduled. Loads RDI with `arg`, calls `entry`, then calls
  `kschedExitTrampoline` (a Go func) if `entry` ever returns. We
  adopt the same `.cfi_undefined rip` root-frame marker
  `src/task_stack_amd64.S:17..18` uses so the unwinder stops cleanly
  at the trampoline.

The exact register save set + calling convention details are in the
**Context-switch register contract** section below. The file goes
under `src/kthread_switch.S` to keep scheduler asm in one place.

## Type layouts

All three types belong in one new file, `src/kthread.go`. Field
layout is chosen so that asm reads are cache-line-friendly and
`//go:nosplit` scheduler code does not need to allocate.

```go
// src/kthread.go (PROPOSED)

// KState is the thread lifecycle state. Plain uint32 so asm stubs
// can CAS without a type alias. Ordering matters: Runnable < Parked
// < Exiting gives Running a strict-less sort key in debug dumps.
type KState uint32

const (
    KStateNew      KState = 0 // spawned, not yet enqueued
    KStateRunnable KState = 1 // on a ready queue
    KStateRunning  KState = 2 // currently executing on some CPU
    KStateParked   KState = 3 // waiting on a sync primitive (§03)
    KStatePreempted KState = 4 // timer ISR demanded a switch; see §04
    KStateExiting  KState = 5 // terminal; stack to be reclaimed
)

// KernelThread is one gooos-owned kernel thread. One instance per
// live service / per Ring-3 process wrapper.
type KernelThread struct {
    // ---- asm-visible (offsets pinned by kthread_switch.S) ----
    SavedRSP uintptr        // offset 0:  RSP parked by kschedSwitch
    State    uint32         // offset 8:  KState (atomic-compatible word)
    OwnerCPU uint32         // offset 12: current/last CPU index; scheduler bookkeeping
    // ---- Go-only (assembled stubs never read these) ----
    Stack     KernelStack   // fixed-size backing (see below)
    Name      [16]byte      // null-padded; used by the debug `kps` dump (§06)
    Entry     func()        // thread body; nil after first schedule
    WakeLink  *KernelThread // intrusive link for sync-primitive wait queues (§03)
    ParkLock  *Spinlock     // lock held by the primitive the thread is parked on (§03 §04)
    Quantum   uint32        // ticks remaining before preemption (§04)
    ExitCode  uintptr       // optional: ring3Wrapper reports Ring-3 exit here (§07)
}

// KernelStack is the fixed-size backing store. 16 KiB = 4 pages.
// Carries an 8-byte canary at the low end so an overrun hits a
// distinctive value before corrupting the next stack.
type KernelStack struct {
    Canary uintptr
    Pad    [ (16*1024 - 16) / 8 ]uintptr
    Top    uintptr // address-of-one-past-end; set at alloc time
}
```

The asm stub file only dereferences offsets 0..15 (`SavedRSP`, the
state/cpu words); every other field is Go-only. Offset 0 for
`SavedRSP` matches the `tinygo_swapTask` convention
(`src/task_stack_amd64.S:44`: `movq %rsp, (%rsi)`), which keeps the
new stub visually close to the imported one.

## Per-CPU ready queue

The scheduler owns one ready queue per online CPU:

```go
// src/kthread_sched.go (PROPOSED)

// kschedReadyQueue is a minimal doubly-linked list of runnable
// kernel threads. WakeLink doubles as a sync-primitive link because
// a thread is never on two lists at once (Runnable XOR Parked).
type kschedReadyQueue struct {
    head, tail *KernelThread
    lock       Spinlock
    _pad       [64 - 24]byte // pad to 64-byte cache line
}

// kschedQueues is indexed by CPU number. maxCPUs = 17 per
// src/percpu.go:14 (smpMaxAPs + BSP).
var kschedQueues [maxCPUs]kschedReadyQueue
```

Operations (all `//go:nosplit`, all taking `kschedQueues[i].lock`):

- `kschedPush(t *KernelThread, cpu uint32)` — tail-enqueue; set
  `t.State = KStateRunnable`, `t.OwnerCPU = cpu`. Called by spawn
  (§02 "Lifecycle"), by wake (§03), by preempted-self-enqueue (§04).
- `kschedPop(cpu uint32) *KernelThread` — head-dequeue; returns nil
  on empty. Called only by the scheduler loop on the owning CPU.
- `kschedSteal(from, to uint32) *KernelThread` — head-dequeue from
  `from`, return. Called by the idle path on `to`. Rank: acquires
  `from`'s queue lock first, no other lock held.

### Placement policy: sticky with work-stealing fallback

**Design choice, justified**: threads prefer the CPU they last ran
on, but an idle CPU will steal from a peer if its own queue is
empty. This is the Linux/CFS shape, and is already the
conceptual model of the current TinyGo `scheduler=cores` scheduler —
§09 M0 preserves the semantics observers already depend on.

Rules:

1. **Wake-from-parked**: push onto `t.OwnerCPU` (the CPU the thread
   last ran on). Rationale: cache locality, and it avoids F1's
   push-to-waker-queue pathology.
2. **Wake-from-new**: push onto the spawn-time round-robin counter
   target (§09 M0 implements the counter in `kschedQueues`-adjacent
   state; existing shape is mirrored from the Ring-3 round-robin in
   `src/elf.go:250` which `current_impl_2026_04_24/fix_plan_deferred_1_5/02_ring3wrapper_round_robin_distribution.md`
   already describes).
3. **Preempted self-enqueue**: push onto `cpuID()` (we keep the
   thread on the CPU that preempted it; good locality).
4. **Idle steal**: scan `(me + 1) % numCoresOnline .. (me + numCoresOnline - 1) % numCoresOnline`
   and take from the first non-empty queue. This matches the current
   `stealWork` shape in `scripts/tinygo_runtime.patch` (line 1088 ff.)
   and keeps observed migration patterns for harness regression.

**Rejected alternative — full round-robin push**: always push to
`(pushIndex++) % n`. Lower cache locality, no benefit over the
sticky+steal design under the current workload. Documented here so
future reviewers don't revisit.

### Per-CPU idle thread

Each CPU owns a hidden **idle kernel thread** that runs when the ready
queue is empty. The idle thread body is `sti; hlt; cli` in a loop
(same shape as the current `waitForEvents` at
`scripts/tinygo_runtime.patch:1120..1138`, which becomes the idle
body).

The idle thread is allocated at boot by `kschedInit(cpu uint32)` and
lives in `kschedIdle[maxCPUs]`. It is never enqueued (the scheduler
treats nil ready-queue head as "run idle").

## Lifecycle: spawn / park / wake / exit / preempt

### Spawn

```
kschedSpawn(name string, entry func()) *KernelThread
  1. Allocate KernelThread struct from the kernel heap.
  2. Allocate a KernelStack page-aligned (adapt the existing
     ring3StackPool allocator at src/ring3_pool.go — see
     below).
  3. Write an initial frame at the top of the stack so that the
     first kschedSwitch into this thread returns into
     kschedEnter(entry, 0). That frame contains: RFLAGS (IF=1 for
     preemption-enabled default) + RBX=0 + RBP=0 + R12=0 + R13=0 +
     R14=0 + R15=0 + RIP=kschedEnter. Matches the reverse of the
     save set in kthread_switch.S.
  4. State = KStateRunnable; push onto the round-robin CPU.
```

### Park

`kschedPark(lock *Spinlock)` — called while the caller holds `lock`;
it atomically releases `lock` and enqueues the caller on whatever
wait queue the lock protects (§03 defines the wait-queue shape), then
calls into the scheduler loop. On return the lock is *not* reacquired
— the waker does the wake, and the primitive re-acquires the lock on
behalf of the resumed thread if its contract requires that (e.g.
condvar semantics). For a pure semaphore Wait / Signal, the lock need
not be held at resume.

### Wake

`kschedWake(t *KernelThread)` — transitions `t.State`
`KStateParked → KStateRunnable`, `kschedPush(t, t.OwnerCPU)`. If the
target CPU is currently idle (i.e. running its idle thread), send a
wakeup IPI (we reuse `ipiWakeupVector` = 0xFC from
`src/pit.go:68..99` and the `lapicSendIPI` path; the existing IPI
machinery stays verbatim).

### Exit

A kernel thread rarely exits — services are long-lived — but
`ring3Wrapper` (§07) and any migrated one-shot boot probe need a
clean exit. `kschedExit(code uintptr)`:

1. Store `code` into `this.ExitCode`.
2. Set `this.State = KStateExiting`.
3. Wake any thread parked on `this.ExitCode` (used by
   `processWait`, §07).
4. Call `kschedYield()` — never returns; the scheduler sees State =
   Exiting on the next pop attempt and recycles the stack back into
   the pool.

### Preempt

Covered in §04. Briefly: the LAPIC-timer ISR sets
`perCPUBlocks[cpu].WantReschedule = 1` (already the current contract
at `src/percpu.go:43`). The safe-point check (currently scattered:
`src/goroutine_irq.go:89` handles kernel preempt; Ring-3 preempt is
via iretq-frame rewrite, `src/goroutine_irq.go:138`) is adjusted in
M3 / M4 to call `kschedYield()` instead of `runtime.Gosched()`.

## Context-switch register contract

`kschedSwitch` saves and restores the System V AMD64 **callee-saved
registers plus RFLAGS**:

| Register | Saved? | Rationale |
|---------|--------|-----------|
| RBP | yes | callee-saved |
| RBX | yes | callee-saved |
| R12 | yes | callee-saved |
| R13 | yes | callee-saved |
| R14 | yes | callee-saved |
| R15 | yes | callee-saved |
| RFLAGS | yes | Interrupt-enable must round-trip across the park so the resumed thread sees the IF bit the parker expected |
| RSP | yes | stored into the struct's SavedRSP field; the asm stores it via `movq %rsp, (%rdi)` analogously to `src/task_stack_amd64.S:44` |
| RAX .. R11 | no | caller-saved; the compiler will have flushed anything live to the stack across the `kschedSwitch` call site |
| RIP | implicit | saved as a normal `callq` return address onto the stack |
| FS / GS base | no | **INVARIANT K2a** — all kernel threads share %gs (set up at `src/percpu.go:234..264` on BSP / AP entry) and `bootPML4`. Kernel threads never touch FS/GS base, so the switch does not. A Ring 3 entry path (§07) is where %gs / CR3 round-trip, not here. |
| CR3 | no | **INVARIANT K2** — kernel threads stay on `bootPML4`. A thread that enters Ring 3 writes CR3 on the way in (`ring3Wrapper` in `src/process.go:274`) and again on the way out (`gooosOnResume`, the patched TinyGo runtime's hook; Route C replaces the hook with an explicit kschedSwitch-path write). |
| XSAVE (SSE/AVX) | no | `src/target.json:4` disables SSE/SSE2/…/AVX for the kernel build. No SIMD state exists to save. |
| TSS.RSP0 | no, set lazily | On Ring 3 entry, `ring3Wrapper` updates TSS.RSP0 for the hosting kernel thread (§07 replaces the current `tssSetRSP0ForCurrentG` at `src/process.go:263` with a kernel-thread-aware variant). `kschedSwitch` itself never needs to touch TSS.RSP0 because it only swaps kernel-side stacks. |

This is the complete save set. Reviewer check §B2 in §Phase-B
review (see `hoge.md §Workflow step 3`) grep-sanity-checks this list.

## TSS.RSP0 interaction

`src/percpu.go:26` already holds a per-CPU `TSSPtr`. Today, each
Ring-3 entry updates TSS.RSP0 via `tssSetRSP0ForCurrentG`
(`src/process.go:263`) to point at the kernel stack of the hosting
`ring3Wrapper` goroutine. Under Route C the hosting entity is a
kernel thread, not a goroutine; the update is one line changed —
`tssSetRSP0ForCurrentG` (and its helpers) reads from the currently-
scheduled `KernelThread.Stack.Top` instead of from `gInfoByTask[t]`.

**Invariant**: at any `iretq` into Ring 3, `TSS.RSP0[cpuID()]` points
at `kschedRunning[cpuID()].Stack.Top`.

## Scheduler loop shape

```go
// src/kthread_sched.go (PROPOSED)

// kschedLoop is the per-CPU scheduler body. Called once per CPU
// from the boot / AP entry code after kschedInit and the idle
// thread setup. Never returns.
//
//go:nosplit
func kschedLoop() {
    cpu := cpuID()
    for {
        t := kschedPop(cpu)
        if t == nil {
            // Try to steal from a peer.
            for i := uint32(1); i < numCoresOnline; i++ {
                t = kschedSteal((cpu + i) % numCoresOnline, cpu)
                if t != nil {
                    break
                }
            }
        }
        if t == nil {
            // Nothing to run: switch to idle thread, which will
            // sti; hlt; cli until a wakeup IPI lands.
            t = &kschedIdle[cpu]
        }
        kschedRunning[cpu] = t
        t.State = KStateRunning
        t.OwnerCPU = cpu
        kschedSwitch(t, kschedBootstrap[cpu]) // see below
        // Returning here means the thread parked / exited / was
        // preempted. The kschedBootstrap context is restored, and
        // we loop around to pick the next one.
    }
}
```

The `kschedBootstrap[cpu]` anchor is a pre-allocated `KernelThread`
representing "the scheduler loop itself". It lives on each CPU's
boot/system stack (`src/percpu.go:26` `SystemStack`). It is never
enqueued or popped; it is only the sink `kschedSwitch` writes
`SavedRSP` into on park and loads from on the next iteration.

## BSP bring-up and AP bring-up

### BSP

Currently `src/main.go` runs TinyGo's `main()` and spawns goroutines.
Route C splits `main()` into:

1. Pre-scheduler init: everything that currently happens before the
   first `go` keyword (percpu, GDT, IDT, PIT, keyboard, VM, SMP
   bring-up, filesystem root, user-binary embedding).
2. `kschedInit(maxCPUs)` — initializes all queues, allocates idle
   threads, pins `kschedBootstrap[0]` to the current RSP.
3. `kschedSpawn` each service in the boot-ordering dictated by §06.
4. `kschedLoop()` — BSP transitions into the scheduler.

There is no longer a "TinyGo scheduler takes over" handoff. The
initial Go stack `main` is running on becomes the BSP's bootstrap
stack; we do not reclaim it (it's also the NMI / double-fault
fallback).

### AP

Each AP currently ends its `apEntry` path in `runtime.apScheduler`
(patched into `scheduler_cores.go`). Under Route C the AP entry
instead ends in `kschedLoop()`. No TinyGo `runtime.apScheduler` is
linked. See §08 for the patch-side removal.

## Stack allocation

There is already a fixed-slot stack pool for Ring 3 kernel stacks at
`src/ring3_pool.go` (16 slots, 8 KiB each). Route C introduces a
parallel pool for **kernel threads**:

- New file: `src/kthread_pool.go` (PROPOSED).
- Size: 32 slots × 16 KiB = 512 KiB total. Sized to cover current
  kernel-goroutine count (15 spawn sites + a few spares) plus up to
  8 concurrent Ring-3 processes each having a wrapper thread.
- Allocation: on `kschedSpawn`, reserve the next free slot. If
  exhausted, **fatal panic** (the workload is fully known at boot;
  exhaustion is a misconfiguration, not a runtime condition).
- Reclaim: `kschedExit` returns the slot to the free list. The
  canary word at the low end of the stack is checked first.

Rationale for a new pool (not reusing ring3StackPool): the Ring-3
pool's 8 KiB size is tight for kernel-thread workloads that call TCP
or FS paths, and the lifetime pattern is different (Ring-3 wrappers
are short-lived, kernel threads live forever). Keeping them separate
makes §05 GC-range enumeration trivial (kthread_pool.go exports one
`.bss`-resident slab; §05 scans the N live slots).

## Relationship to the TinyGo patch

§08 is the full patch audit; a quick summary for §02's purposes:

- **Goes**: all `scheduler_cores.go` hunks except what
  `waitForEvents` needs; `internal/task/task_stack_*` changes
  (kernel side); the `stackTop` canary field (move it into
  `KernelStack.Canary`).
- **Stays**: `runtime_gooos.go` kernel-side bodies (`sleepTicks`,
  `ticks`, `putchar`, `exit`, `abort`, `main`) — §05 keeps the GC
  in place, §08 trims the scheduler-adjacent scaffolding.
- **Replaces**: `gooosOnResume` (the CR3 update hook). It now runs
  from inside `kschedSwitch`'s Go wrapper, not from inside TinyGo's
  `task.resume`. See §07 for the exact placement.

## Summary of proposed new/changed files (§02 scope only)

| File | Status | Content |
|------|--------|---------|
| `src/kthread.go` | NEW | `KernelThread`, `KernelStack`, `KState` |
| `src/kthread_sched.go` | NEW | `kschedQueues`, `kschedBootstrap`, `kschedIdle`, `kschedLoop`, `kschedPush`, `kschedPop`, `kschedSteal`, `kschedYield`, `kschedSwitch` Go wrapper |
| `src/kthread_pool.go` | NEW | 32-slot × 16 KiB stack pool + canary check |
| `src/kthread_switch.S` | NEW | asm stubs `kschedSwitch`, `kschedEnter` |
| `src/kthread_lifecycle.go` | NEW | `kschedSpawn`, `kschedPark`, `kschedWake`, `kschedExit`, `kschedExitTrampoline` |

Every existing file stays on its current path. §06 is the sole place
where `src/*.go` service files are touched to swap `go serviceName()`
for `kschedSpawn("serviceName", serviceName)`.

## Reviewer gates (applied from §hoge Phase B)

- All 8 registers + RFLAGS saved: **yes** (see table above).
- `bootPML4` invariant explicit: **yes** (INVARIANT K2).
- FS/GS base skipped with rationale: **yes**.
- TSS.RSP0 update path named: **yes** (`tssSetRSP0ForCurrentG`,
  §07 refactors).
- Stack-pool sizing stated: **yes** (32 × 16 KiB).
- Idle thread mechanism described: **yes**.

Any additional BLOCKING finding during review goes back into §10.
