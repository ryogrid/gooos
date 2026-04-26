# 05 — Garbage collection integration

TinyGo's conservative mark-sweep GC runs inside the linked runtime
(`gc_conservative.go`, not gooos-specific). Its two interaction
points with the scheduler are:

1. **Stop-the-world (STW)**: while marking, no mutator may
   concurrently write a pointer that the marker is about to scan.
   Today this relies on the goroutine-runtime's cooperative points
   (each goroutine hits a safe-point inside `runtime.Gosched` or
   allocator paths).
2. **Stack scanning**: the marker walks each goroutine's stack via
   `tinygo_scanCurrentStack` and the `task.Task.state.sp`-to-
   `stackTop` range (the `stackTop` field was added by
   `scripts/tinygo_runtime.patch` — see
   `src/runtime/interrupt/interrupt_gooos.go` + the task-stack
   hunks).

Route C removes the goroutine runtime from the kernel side. §05
replaces both interaction points with gooos-owned equivalents that
live in `src/kthread_*.go`. Correctness is preserved; the
"BSP-only allocates while APs park in `waitForEvents`" workaround
(currently documented at `scripts/tinygo_runtime.patch:279..313` /
`gc_blocks.go`) goes away in M5.

## Current state (STATUS-QUO)

Citing `scripts/tinygo_runtime.patch` (1168 lines total):

- **`src/runtime/gc_blocks.go`** — comment-only patch documenting
  that the upstream `gcLock task.PMutex` is a no-op under
  `tinygo.unicore`, and that gooos's cross-CPU correctness relies on
  "BSP-only allocates while APs park in `waitForEvents` until the
  future M5 `gcPauseCore` IPI lands." That "future M5 `gcPauseCore`"
  is **exactly the freeze IPI we design here**.
- **`src/internal/task/task_stack.go` + `task_stack_amd64.go` +
  `task_stack_unicore.go`** — add the `stackTop` field and the
  `gooosStackOverflow(t)` canary hook. Stack-scan range is
  `[task.state.sp, task.stackTop)`.
- **`src/runtime/scheduler_cooperative.go` / `scheduler_cores.go`**
  — per-CPU runqueues; `schedLock` over sleep/timer queues.
- **`src/runtime/wait_gooos.go`** — `waitForEvents()` = `sti; hlt;
  cli`. APs call this when their local queue is empty, so they're in
  a safe state for GC (no Go-stack mutation). This is the current
  approximation of an STW pause.
- **`src/runtime/runtime_gooos.go`** — `sleepTicks`, `ticks`,
  `putchar`, `exit`, `abort`, kernel `main`. No GC-specific body;
  but the `main()` entry sets up the globals that `findGlobals()`
  scans. Route C keeps this file.
- **Globals enumeration**: `_globals_start` / `_globals_end` are
  emitted by the linker; `findGlobals()` scans the range. The
  `make verify-globals` check (`scripts/verify_globals.sh`) asserts
  TinyGo runtime queues (`runqueue`, `sleepQueue`, `timerQueue`) are
  inside that range.

Under Route C:

- `runqueue` / `sleepQueue` / `timerQueue` no longer exist kernel-
  side (we've removed the scheduler hunks). The `make verify-globals`
  assertion is dropped for those three variables in M5 (§08).
- The kernel-thread pool (§02 `src/kthread_pool.go`) is `.bss`-
  resident and is within `_globals_start..end` by construction —
  the GC already scans `.bss` as part of the globals root set.
- `KernelThread.Stack.Pad` is the actual stack bytes. Since
  `KernelThread` lives in the heap (allocated during `kschedSpawn`)
  and the embedded `KernelStack` is part of the heap allocation,
  the stack IS heap-resident. This is fine for GC: heap objects
  are followed via the mark bitmap; the stack's live range is
  `[SavedRSP, &Stack.Top)`.

The critical subtlety: when a kernel thread is **currently running**,
its stack is mutating, so we cannot mark it while it runs. STW
pauses every thread first; then each thread's stack range is stable
and scannable.

## The STW freeze IPI

**PROPOSED** new IPI vector: `ipiFreezeVector = 0xFD` (the 0xF-series
aligns with the existing wakeup 0xFC and preempt 0xFB).

```
0xFB — preempt       (existing)
0xFC — wakeup        (existing)
0xFD — GC freeze     (PROPOSED, new)
0xFE — LAPIC timer   (existing)
0xFF — spurious      (existing, per LAPIC convention)
```

Handler body (new Go function `handleFreezeIPI(vector uint64)` in
`src/kthread_gc.go` PROPOSED):

1. `lapicSendEOI()` first (same shape as other ISRs).
2. Read `%gs:48` (`readPreemptDisable()`). If > 0, set per-CPU
   `WantSTWFreeze = 1` and return — the thread will freeze at its
   next `Spinlock.Release` (§04 added this hook).
3. Otherwise spin on `stwReleaseFlag` (a package-level `uint32`)
   until the GC clears it. Use `gooosPause()` in the spin
   (`src/percpu.go:227`, the x86 PAUSE hint).

**Freeze entry counter**: each CPU bumps `stwFrozenCount` atomically
as it enters the spin; the GC waits for `stwFrozenCount ==
numCoresOnline - 1` (all CPUs except the initiating one) before
starting the mark.

**Freeze exit**: the GC sets `stwReleaseFlag = 1`. Every spinning
CPU breaks out, decrements `stwFrozenCount`, and returns from the
ISR.

**Edge case — already at a safe point**: if a CPU is running the
idle thread (`kschedRunning[cpu] == &kschedIdle[cpu]`), it's
already in `sti; hlt; cli`. The freeze IPI wakes it from `hlt`;
the handler runs, enters the spin, the GC sees the count, marks,
releases, and the CPU returns to idle. No kernel thread's stack
was live during mark; idle has no interesting pointers.

**Edge case — frozen at spinlock release**: a thread that was
mid-critical-section at the IPI time sets `WantSTWFreeze = 1` and
returns from the IPI. It finishes its critical section, reaches
`Spinlock.Release`, checks `WantSTWFreeze`, enters the freeze spin
itself. The GC waits for the count. Progress is bounded because
the section length is bounded (no lock-held wait operations).

## Stack-bound discovery

STW is in place; now the marker needs to know where each frozen
thread's stack lives.

**PROPOSED** helper `kthreadStackRange(t *KernelThread) (lo, hi uintptr)`:

- `hi = t.Stack.Top` (one past end).
- `lo = t.SavedRSP`.

For the currently-running thread on the initiating CPU (the GC is
running in that CPU's context), `SavedRSP` isn't written yet — the
thread is still on its stack. The GC calls
`tinygo_scanCurrentStack` (`src/stubs.S` already exports this;
`user/runtime_asm_amd64.S` has the user-side copy) to walk the
running CPU's stack range. For frozen threads (every CPU in the
freeze spin), `SavedRSP` was written by `kschedSwitch` at park /
preempt time, so `lo` is accurate.

Wait — there's a subtler case. A CPU whose thread is **currently
running** (not parked, not in the idle thread) receives the freeze
IPI, enters the handler, and spins. The thread's stack between
where-it-was-interrupted and the IPI handler's top-of-stack IS the
mutator's stack — the ISR pushed a trap frame onto it. So for a
running-but-frozen thread:

- `hi = t.Stack.Top`.
- `lo = the RSP at which the freeze ISR is currently spinning`.

We capture this by having the freeze-IPI handler, on first entry,
write its own RSP into `KernelThread.SavedRSP` before spinning. On
release, it restores whatever — actually no restore is needed;
`kschedSwitch` never runs during STW (interrupts are enabled
briefly only in freeze spin, but the spin doesn't run scheduler
code). On IPI return, the thread resumes where it was and its
`SavedRSP` is overwritten on the next natural park.

So the protocol is:

1. IPI arrives at CPU X, thread T running.
2. Handler writes `T.SavedRSP = <current RSP>`.
3. Handler bumps `stwFrozenCount`; spins on `stwReleaseFlag`.
4. GC iterates `kthreadAll[]`, reads `SavedRSP` / `Stack.Top` for
   each thread, marks.
5. GC sets `stwReleaseFlag = 1`.
6. Handler decrements `stwFrozenCount`, returns; IPI epilogue
   restores regs; thread resumes.

The thread's natural next `kschedSwitch` (park/preempt) writes a
fresh `SavedRSP`. Between the IPI return and that next switch, the
old value is stale but harmless (no GC is running).

### Accessing the thread table

**PROPOSED** table: `kthreadAll []*KernelThread` — a slice holding
every live kernel thread. Updated under `kschedAllLock` at
`kschedSpawn` (append) and `kschedExit` (remove by swap-with-last).
Size cap = 32 (matching the stack pool). The slice itself lives in
`.bss` and is root-scanned as part of the globals.

The GC walks it under `kschedAllLock`. Because STW has already
frozen all mutators, the lock is uncontended at GC time (only the
GC-initiating CPU is live). We still take the lock for sanity — it's
cheap.

## Heap allocation from ISR context

**INVARIANT K3** (§01) — no heap allocation from ISR context. This
is already an audit gate in the current tree
(`scripts/lint_isr.go`: `make lint` rejects `make`, `new`, `append`
(growing), `&T{…}`-with-escape, and `go` inside ISR-rooted functions).

Route C preserves K3 verbatim. The new ISR handlers (freeze IPI) do
not allocate. `kschedWake` from an ISR... does not happen under
Route C: the only wake from inside an ISR is the `handleFreezeIPI`
path which doesn't wake anyone, and `handlePreemptIPI` which calls
`kschedYield` — yield is a park-then-switch, not an allocation.

If a future ISR path needs to publish data (e.g. e1000 RX DMA
completion → wake a blocked reader), the pattern is already in
place: `netRxLoop` drains the ring from **thread context**, and
the ISR only sets a flag (`rxReadyFlag` / counters; see
`src/net.go:50..82` comment describing the transition from a
channel-based RX to the poll-based RX). Route C's `netRxLoop`-as-
kernel-thread needs no ISR wake; it polls the ring + `kschedYield`,
same as today.

## GC-mid-context-switch

Question: what if the GC freeze IPI lands on CPU X *during*
`kschedSwitch`? The asm stub runs with interrupts disabled
(INVARIANT L1: `kschedSwitch` is only called from inside the
scheduler loop, which may or may not have interrupts off;
but §02's `kschedPark` releases the primitive lock and calls
`kschedSwitch` with interrupts *enabled* — otherwise we'd deadlock
the LAPIC-timer preempt).

Actually, re-examining: `kschedSwitch` is a leaf-level asm stub.
The Go wrapper that invokes it decides what interrupt state to be
in. Proposal for safety:

- `kschedSwitch` asm disables interrupts at entry (`cli`), re-enables
  at exit (`sti`) *iff the saved RFLAGS had IF set*. Same trick as
  `Spinlock.Acquire/Release`.
- The body of `kschedSwitch` is short (save 6 regs + RFLAGS, swap
  RSP, pop 6 regs + RFLAGS, ret). No ISR can fire during the swap.

This means the freeze IPI cannot arrive mid-swap. It arrives either
before the swap (thread A still running, frozen on A's stack) or
after (thread B running, frozen on B's stack). Either way the
`SavedRSP` field is consistent — either it hasn't been written yet
for the outgoing thread (it's running, RSP live) or it's been
written (thread parked, RSP stored).

**Edge case**: the freeze IPI arrives between `movq %rsp, (%rdi)`
and the first pop. Interrupts are disabled, so it doesn't actually
arrive — it's pended in the LAPIC and delivered on the next `sti`.
By that point we've fully switched to B's stack. Fine.

## What the patch loses

The current tree has these GC-adjacent patch hunks:

- `gc_blocks.go` comment (lines 279..313) — Route C deletes this
  comment; the "BSP-only allocates" caveat goes away because we
  have a real STW.
- `internal/task/task_stack_unicore.go` — adds `stackTop` field to
  `task.Task`. Route C keeps **the user-side** copy of this hunk
  (user-side GC still scans user-side tasks via stackTop). Kernel
  side: no `task.Task` exists, so the field is unreferenced on
  that side. §08 lists the patch lines that flip.
- `internal/task/task_stack.go` — the `gooosStackOverflow(t)` hook
  call-site. Same user-side vs kernel-side split: user stays,
  kernel loses. The kernel-side replacement is
  `KernelStack.Canary` checked inside `kschedExit` and also
  periodically via a helper in the scheduler loop (optional; §10
  parks).
- `runtime/wait_gooos.go` — `waitForEvents` stays as the idle-thread
  body. §08 keeps this hunk.

## Globals discovery stays

The linker-emitted `_globals_start` / `_globals_end` range covers
`.bss` + `.data`, both read-only once boot is done. All the new
`src/kthread_*.go` globals (`kschedQueues`, `kschedBootstrap`,
`kschedIdle`, `kschedRunning`, `kthreadAll`, `kthreadPool`,
`stwReleaseFlag`, `stwFrozenCount`) live in `.bss` and are scanned
automatically. The `make verify-globals` check
(`scripts/verify_globals.sh`) currently asserts `runqueue`,
`sleepQueue`, `timerQueue` are in range; §08 replaces those asserts
with the new kthread globals in M5.

## Reviewer gates

- STW pathway described end-to-end: **yes**.
- Deadlock-freedom argument: **yes** (§04 INVARIANT L1 + K4; §05
  the release-then-freeze sequencing).
- Stack bounds captured for running / frozen / parked threads:
  **yes**.
- ISR-no-alloc invariant respected: **yes** (K3 preserved; no new
  ISR allocates).
- GC-mid-switch safe: **yes** (cli/sti discipline).

Any BLOCKING finding that contradicts the above ends up in §10.
