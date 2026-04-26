# 01 — Overview and motivation

## Problem statement

The gooos kernel currently runs every long-lived service as a TinyGo
goroutine cooperating via Go channels, scheduled by the TinyGo
`scheduler=cores` runtime. This worked well enough to get the system
to its current feature set — multi-core preemptive scheduling (`src/lapic_timer.go:88`,
`src/goroutine_irq.go:89`), TCP/IP with retransmission (`src/tcp_retx.go:138`),
Ring-3 shell plus 15 user programs — but the goroutine+channel model
has produced a durable class of flakes whose root cause is that the
TinyGo scheduler and gooos's bare-metal requirements pull in opposite
directions.

Two hazards dominate the current flake backlog:

### F1 residual Sleep-3 flake

After the Option G revert (`scripts/test_sleeptest_postrevert.sh` S2
baseline = 25 / 50 PASS = 50 %), roughly 42 % of runs still hang on
one of the three `sys_sleep` calls in the sleeptest user program.
Analysis in
`current_impl_2026_04_24/fix_plan_deferred_1_5/03_sleep_cross_cpu_channel_wakeup_audit.md`
ranks seven hypotheses (H1 @ 65 % → H7 @ 3 %). The dominant
hypothesis (H1) is an SMP channel-wakeup race: `scheduleTask(waiter)`
pushes a woken task onto the *waker's* CPU queue
(`scripts/tinygo_runtime.patch:1004`), `schedulerWake` broadcasts an
IPI, but whichever AP reaches `stealWork` first may have its local
queue non-empty and so never steals the stranded waiter off BSP. The
wake round-trip only closes when BSP itself next reaches its
scheduler loop — which it may not, because BSP may itself be parked on
the same channel/afterTicks primitive.

The F1 audit instrumentation landed as commit `6690b54` on branch
`smp-no-goroutine-in-kernel`:

- `src/percpu.go:62` — `SchedTasksPushed / SchedPopOk / SchedPopNil`
  per-CPU counters.
- `src/percpu.go:68`, `src/pit.go:73` — ISR-side `sleepAuditISRDump`
  that bypasses the scheduler and the netDiag goroutine, so the audit
  survives exactly the flake the audit is trying to observe.
- `src/preempt_config.go:71` — `runSleepAudit = false` gate.

The F1 audit proves the flake is structural to the
goroutine+channel+cross-CPU-wake chain. Fixing it on the existing
substrate requires either patching TinyGo's `scheduleTask` push policy
(Route B) or giving up the chain entirely (Route C).

### H-01 "kernel thread on own stack" hazard

Documented at `pasttodos/TODO_SCHED.md:347`. Plan-01's proposed design
— kernel threads run on their own stacks and yield via
`kernelThreadSwap` — cannot safely host any service that calls
`runtime.Gosched()`, channel send/recv, or `<-afterTicks(...)` from
inside the kernel thread. Root cause: `runtime.Gosched` reaches
`task.PauseLocked`, which saves the current stack pointer into the
current TinyGo task's state. When the host TinyGo task `H` has been
"borrowed" by a kernel thread running on stack `SK`, `task.Current()`
still returns `H`, so `PauseLocked` writes `SK` into `H.state.sp`.
When `H` is later resumed by the TinyGo scheduler, TinyGo's
`task.resume` loads `SK` as `H`'s stack and runs on the kernel
thread's stack — both the host task and the kernel thread get
corrupted simultaneously.

Every current long-lived service uses at least one prohibited
primitive: `timerDispatcher` and `netRxLoop` call `runtime.Gosched()`
(`src/afterticks.go:117`, `src/net.go:78`); `fsTask` does
`for req := range fsReqCh` (`src/fs.go:198`); `tcpRTOScannerLoop`
parks on `<-afterTicks` (`src/tcp_retx.go:140`); `udpEchoServer` /
`tcpEchoServer` park on channel recv. Partial kernel-thread migration
is therefore not a path forward; the hazard is all-or-nothing.

## Why Route C, not A or B

The user's prior evaluation (referenced in hoge.md Background)
considered three routes:

### Route A — keep status quo

Preserve the TinyGo goroutine+channel model and try to fix F1 with a
targeted patch (e.g. H1's "wake to receiver's last CPU" fix in
`task.Task`). Pros: smallest diff. Cons: (a) F1 has seven hypotheses
and the winning one is probabilistic, not certain; (b) H-01 still
blocks every future kernel-thread design (Plan-01, Plan-04,
`bootFinalizeThread`, all deferred under H-01 / H-03 / H-04); (c) the
cooperative-yield model keeps gooos tied to TinyGo scheduler
internals, which churn across TinyGo versions (0.33.0 → 0.40.1
already required substantial patch rework). Route A leaves the
substrate fragile.

### Route B — patch TinyGo to park on custom scheduler primitives

Keep `go func()` and `chan` syntactically, but make TinyGo call into
gooos-owned park/wake primitives so `task.PauseLocked` no longer
collides with kernel-thread stacks. This is the H-01 "options 1 and
3" direction. Pros: preserves Go idioms in kernel code. Cons: (a) the
patch surface grows significantly — `internal/task/task_stack*.go`,
`runtime/scheduler_*.go`, `runtime/chan.go` all grow gooos-specific
branches; (b) the channel runtime still depends on
`task.Pause / Resume` and on a global channel wakeup path that must
correctly fence cross-CPU; (c) every TinyGo upgrade becomes a
multi-day port. Route B is the heaviest H-01 fix and does not fully
eliminate the F1 class of cross-CPU wakeup races — the channel
runtime still decides *where* to resume a waiter, and getting that
right under SMP is exactly the hypothesis set we have not yet been
able to nail.

### Route C — drop goroutines and channels from the kernel (chosen)

Replace `go func()` with `kschedSpawn(func, args)` and replace `chan`
with gooos-owned queues / semaphores / event primitives backed by
spinlocks. Kernel threads run on their own stacks, scheduled by a
gooos-owned ready-queue loop. The TinyGo runtime stays linked in for
the language runtime (type info, interface dispatch, map, slice
growth, GC) but the scheduler and channel runtime do not run on the
kernel side.

Pros:

1. **F1 dies mechanically.** There is no channel wakeup, no
   cross-CPU `scheduleTask` push, no TinyGo stealWork step. Wakeups
   go through a semaphore whose wait queue lives on the semaphore
   itself (no global runnable set).
2. **H-01 dissolves.** Kernel threads own their stacks outright;
   `task.PauseLocked` never runs on the kernel side, so the stack-
   pointer-aliasing hazard cannot occur.
3. **Preemption survives.** The current 100 Hz LAPIC-timer +
   preempt-IPI substrate (§04) keeps working; it now yields kernel
   threads instead of TinyGo tasks. Ring-3 SIGALRM delivery via
   iretq-frame rewrite (`src/goroutine_irq.go:138`) is unaffected.
4. **User side untouched.** The user-side TinyGo runtime runs on its
   own `scheduler=tasks` — user binaries keep `go func()`, `chan`,
   `select`, `time.Sleep`. The split lives cleanly at the Ring 0 / 3
   boundary; see §07.
5. **TinyGo-version drift shrinks.** §08 enumerates the hunks to
   keep vs. drop. The kernel-side patch surface shrinks from ~900
   lines to a few dozen (`runtime_gooos.go` entry, idle loop,
   interrupt.In).

Cons / risks:

1. **Large one-time migration.** 15 spawn sites + 21 channel make
   sites + 12 `<-afterTicks(...)` consumers all rewrite. §06 is the
   inventory; §09 is the milestone order that makes the migration
   testable.
2. **Lose Go-idiomatic `select`.** Kernel callers that want
   multi-waiter semantics must hand-write a bounded poll loop over
   two or more event primitives. §03 gives the pattern.
3. **Boot-ordering re-sequencing.** `fsTask`, `timerDispatcher`,
   `netRxLoop`, `tcpRTOScannerLoop` today are spawned from
   `src/main.go` by `go name()`. Under Route C these become
   `kschedSpawn(name)` calls; the spawn order matters more because
   kernel-thread *creation* is synchronous (it sets up a stack and
   enqueues) rather than Go's goroutine-creation-is-a-queue-push.
4. **GC integration needs rework.** The conservative collector today
   walks TinyGo task stacks via the patched `task.stackTop` field.
   §05 replaces that discovery mechanism with a kernel-thread-table
   walk, plus a broadcast freeze IPI for STW.
5. **Route C is a direction change.** Prior impldoc/* docs that
   assume "service loops are goroutines" become stale. §11 catalogs
   the README diff; individual impldoc/ files can age naturally or be
   superseded by `current_impl_2026_04_24/` successors.

## What this cycle gains us

Capabilities we get when M4 / M5 of §09 lands:

- **True kernel multitasking under gooos control.** `kschedYield()`,
  `kschedPark(sem)`, `kschedTimedPark(deadline)` are all gooos
  functions whose behaviour does not depend on TinyGo's scheduler
  internals.
- **Deterministic preemption semantics.** Spinlock hold → preempt
  disabled (already the current contract at `src/percpu.go:47` via
  `PreemptDisable` %gs:48 — §04 reuses it). No more "the runtime may
  or may not repark depending on channel state".
- **GC STW becomes explicit.** §05 proposes a broadcast freeze IPI
  that parks every AP in a known safe-point spin; the collector then
  walks the kernel-thread table directly. This cleans up
  `scripts/tinygo_runtime.patch`'s `gc_blocks.go` "BSP-only allocates"
  workaround.
- **Service-debuggability.** Each kernel thread is a table entry with
  a name and a state; `ps`-equivalent dump for the kernel side is a
  one-loop print (cf. feature 2.5's user-side `sys_listprocs`,
  `impldoc/shell_ps_command.md`).

## What this cycle explicitly does not gain us

- No intra-user-process preemption. The user-side TinyGo runtime
  stays `scheduler=tasks` (cooperative). Ring-3 SIGALRM delivery
  continues to handle fairness at process granularity
  (feature 2.2 / `impldoc/preempt_user_goroutines.md`).
- No change to the Ring-3 syscall ABI. The 39 syscalls at
  `current_impl_0421_night/05_process_elf_ring3_syscalls_signals.md`
  keep their numbers and arg layouts; only a Ring-3 `ring3Wrapper`'s
  *hosting* shifts from TinyGo goroutine to kernel thread (§07).
- No change to the user-side build (`user/target.json` keeps
  `scheduler=tasks`).
- No regression fix to anything outside the scheduler/IPC substrate.

## Scope contract for §§02 through §§11

The remaining docs treat the following as fixed:

- **Invariant K1** — kernel threads run on their own kernel stacks,
  allocated from a gooos-owned pool (§02).
- **Invariant K2** — kernel threads stay on `bootPML4`. CR3 only
  changes at the Ring 0 ↔ Ring 3 boundary (§07).
- **Invariant K3** — no allocation from ISR context. Heap growth and
  channel-like primitives are callable only from thread context
  (§03, §05).
- **Invariant K4** — spinlock acquire bumps `PreemptDisable`; release
  drops it. Preempt is disabled iff at least one spinlock is held
  (§04). This is already the current contract; Route C preserves it
  verbatim.
- **Invariant K5** — the user-side TinyGo runtime is not modified
  by Route C (§07, §08).

Any finding that would violate one of K1..K5 is a BLOCKING reviewer
finding.
