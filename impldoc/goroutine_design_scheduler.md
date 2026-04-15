# Scheduler, Stacks, and Runtime Stubs

This document specifies how TinyGo's `scheduler=tasks` replaces the
hand-written scheduler in `src/scheduler.go`, how per-goroutine stacks are
allocated, how preemption interacts with the PIT timer IRQ, and what runtime
stubs gooos must provide. It complements `goroutine_design_overview.md`.

## 1. Current state (what is being replaced)

`src/scheduler.go` defines:

- A `Task` struct (`src/scheduler.go:28-35`) holding `SP`, `State`, `ID`,
  `StackBase`, `KernelStackTop`, `WakeupTick`.
- A fixed-size task table `tasks [maxTasks]Task` with `maxTasks = 32`
  (`src/scheduler.go:23`).
- Round-robin `schedule()` (`src/scheduler.go:164-218`) driven by the PIT
  IRQ in `handleTimer` and by explicit `yield()` / `taskSleep()` /
  `waitQueueSleep()` calls.
- `WaitQueue` (`src/scheduler.go:328-414`) with `waitQueueSleep`,
  `waitQueueWakeOne`, `waitQueueWakeAll`, `waitQueueAppend`,
  `waitQueueRemove` — the concurrency primitive used by the custom channel
  implementation.
- Sleep queue (`src/scheduler.go:268-325`) sorted by PIT tick.
- `sti; hlt` idle loop inside `waitQueueSleep` (`src/scheduler.go:357-364`)
  with a comment explaining why the same loop cannot live inside
  `schedule()`.

Context switch is `switchContext(oldSP *uintptr, newSP uintptr)` in
`src/switch.S:14`, saving/restoring `rbx, rbp, r12-r15`.

Per-task kernel stacks: each `Task` has its own 8 KiB (2 contiguous pages)
kernel stack allocated via `allocPagesContig(2)` (`src/scheduler.go:62,
120`). The TSS.RSP0 field is updated on every context switch in
`schedule()` (`src/scheduler.go:213`) so that Ring-3 → Ring-0 transitions
land on the correct stack.

All of the above is removed or gutted in the new design.

## 2. Coexistence strategy: replacement, not layering

`src/scheduler.go` is **deleted at the end state.** TinyGo's runtime
scheduler (`/usr/local/lib/tinygo/src/runtime/scheduler.go`) takes
over entirely. Layering a hand-written scheduler on top of the
runtime scheduler would double-manage the runqueue and ship two
context-switch paths; cleaner to commit to the runtime.

The recommended implementation ordering in
`goroutine_design_gc_and_smp.md §9` lets `src/scheduler.go` live
temporarily alongside the new runtime (with `scheduler=tasks`
already flipped) for several intermediate steps — the file is not
removed until step 8, after each channel-using caller has migrated
to native channels. This keeps the blast radius of each step small.

### 2.1 API removals (callers must migrate)

| Removed                                       | Replacement                                 |
|-----------------------------------------------|---------------------------------------------|
| `createTask(entry uintptr)` (`scheduler.go:87`) | `go func() { … }()`                        |
| `yield()` (`scheduler.go:261`)                | `runtime.Gosched()`                         |
| `taskSleep(ticks uint64)` (`scheduler.go:280`) | `time.Sleep(d time.Duration)`              |
| `waitQueueSleep/WakeOne/WakeAll`              | channel send/recv (the blocking primitive) |
| `Task` struct + `tasks[]` table               | TinyGo `task.Task` (in `internal/task`)    |
| `switchContext` (`switch.S:14`)               | `tinygo_swapTask` (`task_stack_amd64.S:40`) |

`src/switch.S` is not deleted outright — two pieces survive:
`elfExecTrampolineAddr` (referenced by `process.go`'s new
`ring3Wrapper`) and `taskReturnHaltAddr` (still used as a safety-net
return address on newly created stacks). Everything else in
`switch.S` — the `switchContext` routine itself and every
`*TaskAddr` entry-point stub — is removed.

Entry-point address stubs in `switch.S` disappear with their corresponding
Go functions (see §2.2). `elfExecTrampolineAddr` survives because Ring-3
launches still need it; see §5.

### 2.2 Service task migration

Today (`src/main.go:317-324`):

```
chanInit()
createTask(serialTaskEntryAddr())   // Task 1 — serial output
fsInit()
createTask(fsTaskEntryAddr())       // Task 2 — filesystem
```

After the migration, `main()` spawns each service as a goroutine:

```
go serialTask()
go fsTask()
// … shell bootstrap …
```

The entry-point address stubs in `src/switch.S` (`serialTaskEntryAddr`,
`fsTaskEntryAddr`) become dead and are removed along with `switch.S`'s
stubs for dead-code test tasks (`demoTaskA/B/C`, `chanProducerTask`,
`chanConsumerTask`, `chanRendezvousA/B`, `selectTestTask`,
`selectProducerA/B`, `keyboardConsumerTask`, `userPrintTask`). Those test
tasks are not spawned at boot and have been dead since the busybox
integration; deleting them costs nothing. Only `elfExecTrampolineAddr`
(`src/switch.S:147-150`) stays.

`serialTask` today (`src/serial.go:84-96`) calls `sti()` on entry then
loops on `chanRecv(serialChannel)`. Under the new design the `sti()` call
is removed (the runtime enables interrupts before any goroutine runs) and
`chanRecv` becomes `<-serialCh`. The body is otherwise unchanged.

`fsTask` (`src/fs.go:159-199`) migrates the same way: remove `sti()`,
replace `chanRecv(fsRequestChannel)` with `<-fsReqCh`, replace the custom
reply-channel send with `req.ReplyCh <- resp` on a native channel.

### 2.3 Ordering at boot

`main()` must complete hardware init (IDT, PIC, PIT, VGA, keyboard, VM,
GDT+TSS, SMP) *before* the goroutine runtime is entered. TinyGo's runtime
`callMain` calls the user-written `main()` from inside the scheduler loop,
so the standard pattern is:

1. `main()` runs hardware init synchronously (this executes on goroutine 0,
   the "main goroutine").
2. `main()` spawns service goroutines: `go serialTask(); go fsTask()`.
3. `main()` calls the shell bootstrap which `elfExec`s `sh.elf`. The current
   `elfExec` (`src/process.go:87`) blocks the parent via `schedule()`; under
   the new design it blocks via `<-exitCh` on a channel that
   `processExit` sends on. See §5.

## 3. Stack model

### 3.1 Per-goroutine stack

TinyGo allocates each goroutine's stack with a call to `runtime_alloc`
(`/usr/local/lib/tinygo/src/internal/task/task_stack.go:75`):

```go
stack := runtime_alloc(stackSize, nil)
```

`runtime_alloc` resolves via `//go:linkname runtime_alloc runtime.alloc` —
gooos's conservative GC already exports `runtime.alloc`. The call lands in
the kernel heap (`.heap`, 4 MiB, `src/linker.ld:61-66`). Stack size per
goroutine is determined by the TinyGo compiler intrinsic
`getGoroutineStackSize(fn)` (see `task.go:32` in the TinyGo runtime). The
compiler estimates a size based on callee analysis; for the kernel we must
verify — via the TinyGo spike mentioned in `goroutine_design_overview.md
§7` — that the estimate is safe for our service loops. If it is not, we
can force a larger size by adding dummy large local arrays, or patch the
spawner via the `-stack-size` flag if the build supports it.

**No stack growth in v1.** TinyGo's tasks scheduler uses fixed-size
stacks; overflow corrupts the next heap object. Guard pages would require
4 KiB-aligned stacks and VM manipulation — deferred to v2.

### 3.2 Per-physical-CPU kernel stack (TSS.RSP0)

Conceptually separate from the per-goroutine stack. The existing
per-*Task* 8 KiB kernel stack pointed at by `Task.KernelStackTop` was used
for Ring 3 → Ring 0 transitions via TSS.RSP0. Under goroutines there is
*one* CPU with one TSS; we only need *one* kernel stack for interrupt
entry (plus a per-goroutine state save area when a goroutine is mid-flight
during an interrupt).

Design:

- Allocate one 8-KiB contiguous kernel stack at boot via
  `allocPagesContig(2)` (unchanged utility from `src/vm.go:115`).
- `tssSetRSP0` (`src/gdt.go:123-128`) is called exactly once, with the
  top of that stack. No per-context-switch update.
- ISR entry lands there regardless of which goroutine was running.

The "current goroutine's stack is disjoint from the kernel ISR stack"
invariant is crucial: an ISR entered from Ring 0 continues on the
goroutine's own stack (no TSS load because no privilege change). This
means the goroutine's stack must be large enough to absorb the largest
ISR frame plus any Go runtime call the ISR makes. The
`goroutine_design_channels_and_isr.md` constrains ISR code to be
allocation-free, so the frame is small (register dump + small local
variables).

### 3.3 Ring-3 user processes and the kernel stack

`src/process.go:14,198` still models a Ring-3 user process as a `Process`
with `UserPages`, `UserPaddrs`, `EntryPoint`, `StackTop`, etc.
`elfExec` now:

1. Creates a native channel `exitCh := make(chan uintptr, 1)`.
2. Spawns `go ring3Wrapper(proc, exitCh)`.
3. Reads `exitCode := <-exitCh`, returns.

`ring3Wrapper` is new:

```go
func ring3Wrapper(proc *Process, exitCh chan<- uintptr) {
    tssSetRSP0ForCurrentG()
    jumpToRing3(proc.EntryPoint, proc.StackTop)
    // unreachable — iretq does not return
}
```

Because Ring-3 → Ring-0 transitions use TSS.RSP0, and a goroutine can
migrate across the single CPU's runqueue, the TSS must point at *this*
goroutine's stack top while Ring 3 is live. When the user program syscalls
(`int 0x80`), the CPU uses TSS.RSP0 to push the frame — landing on the
goroutine's own stack. The syscall handler then runs on the goroutine's
stack, yields or blocks as needed, and eventually `processExit` sends on
`exitCh`, the wrapper returns, and the goroutine dies.

`tssSetRSP0ForCurrentG()` is a new helper. Implementing it is
subtler than it looks. `task.state.sp` in TinyGo
(`/usr/local/lib/tinygo/src/internal/task/task_stack_amd64.go:12-21`)
is the *saved* stack pointer from the last pause, not the top of stack.
The canary pointer (`task_stack.go:81`) is the *bottom*. Neither gives
the top directly, and TinyGo does not persist the stack size on the
Task struct.

Two viable implementations:

1. **Side table keyed by goroutine**: every time a new goroutine is
   spawned via a gooos-provided wrapper (instead of raw `go`), record
   `(taskPtr, stackBase, stackSize)` in a hash map. `ring3Wrapper`
   looks itself up.
2. **Stack-address trick**: `ring3Wrapper` reads its own RSP, rounds
   up to the next 16-byte boundary, and uses that as the TSS.RSP0
   value. This works because Ring 3 → Ring 0 via `iretq` switches
   stacks *downward* from RSP0; as long as RSP0 points somewhere
   above the current frame within the same page, the ISR frame fits.

v1 picks option 2 for simplicity:

```go
func tssSetRSP0ForCurrentG() {
    var marker uintptr
    top := (uintptr(unsafe.Pointer(&marker)) + 15) &^ 15
    tssSetRSP0(top)
}
```

This lands in `src/goroutine_tss.go`. The caveat: the ISR frame then
overlaps the wrapper's own stack frame, which is fine because the
wrapper's only job is to call `jumpToRing3` (unreachable return) — no
live locals after the jump. Document the invariant in code comments.

## 4. Preemption

Hybrid model.

### 4.1 Goroutines: cooperative

Kernel-side goroutines yield only at function boundaries where Go's
compiler inserts preemption checks — effectively, at call sites — or via
explicit `runtime.Gosched()`, channel ops, `time.Sleep`, and `select`.
Tight loops in kernel goroutines that never call into the runtime will
block the runqueue.

Rationale: TinyGo's `scheduler=tasks` does *not* implement async
preemption. Adding forced preemption (e.g., stomping on a goroutine's PC
from the timer ISR) is out of scope for v1 — the service goroutines we
ship are all blocking on channels or sleeps, so tight loops are not an
issue in practice.

### 4.2 Ring-3 user tasks: preemptive via PIT + iretq

Unchanged from today. The PIT fires at 100 Hz, `handleTimer`
(`src/pit.go` / `src/interrupt.go`) runs in ISR context, calls the new
runtime-scheduler tick hook, and — because the interrupted code was in
Ring 3 — returns through `iretq` with the option of switching goroutines
if the scheduler decides. In practice this means: the currently running
`ring3Wrapper` goroutine can be paused at any timer tick, and another
goroutine scheduled. When the `ring3Wrapper` is resumed, it `iretq`s back
into user mode. This preserves the existing Ring-3 preemption behavior.

Concretely: `handleTimer` only *flags* a reschedule request and
advances the sleep queue; it never calls `task.Pause()` from ISR
context (that would trip `interrupt.In()` and panic — see §5.3, §5.4).
The actual goroutine switch happens after the ISR epilogue in
`src/isr.S`: if the flag is set and the interrupted frame was a Ring 3
or Ring 0 wrapper frame returning to scheduler, control dispatches to
the main scheduler loop instead of `iretq`ing back into the
interrupted context. The epilogue's reschedule check is new assembly
gluing `in_interrupt_depth--` to a conditional jump into the
runtime's scheduler entry point.

### 4.3 Idle behavior

When the runqueue is empty, TinyGo's `scheduler()`
(`runtime/scheduler.go:160-239`) calls `sleepTicks(timeLeft)` with the
time until the next sleeping goroutine is due. gooos's `sleepTicks`
implementation (see §5.2) runs `sti; hlt` until either the PIT advances
the tick counter past the deadline or an IRQ wakes an earlier goroutine.
This replaces the `sti; hlt` loop in the current
`waitQueueSleep` (`src/scheduler.go:357-364`).

## 5. Required runtime stubs — and why they are non-trivial

TinyGo's runtime expects the following functions to exist at link time:

| Symbol                                   | Source in TinyGo                            | Today's `goos=linux` body       |
|------------------------------------------|---------------------------------------------|---------------------------------|
| `runtime.sleepTicks(d timeUnit)`         | `runtime/runtime_unix.go:209`               | calls `libc.usleep`             |
| `runtime.ticks() timeUnit`               | `runtime/runtime_unix.go`                   | calls `clock_gettime`           |
| `runtime.ticksToNanoseconds` / `nanosecondsToTicks` | `runtime/runtime_unix.go`        | identity cast (timeUnit = ns)   |
| `runtime.deadlock()`                     | `runtime/scheduler.go:62`                   | prints + `panic("deadlock")`    |
| `tinygo_register_fatal_signals`          | `runtime/runtime_unix.go:129` (`//export`)  | signal setup via libc           |
| `runtime.alloc` / `runtime_alloc`        | `runtime/gc_blocks.go`                      | conservative GC bump (already works for gooos) |

### 5.1 The build-tag problem (CRITICAL — spike required)

`src/target.json` currently sets `"goos": "linux"`. That selects
TinyGo's `runtime_unix.go`, which *defines* `sleepTicks` / `ticks` /
`deadlock` / `tinygo_register_fatal_signals` with bodies that call libc
or OS facilities not available in Ring 0. **A plain
`//go:linkname sleepTicks runtime.sleepTicks` stub in `src/goroutine_stubs.go`
will not link — it produces a duplicate symbol, and even if it did not,
`runtime_unix.go`'s own calls into `usleep`/`clock_gettime` remain
compiled into the binary and pull in libc.**

Three resolution options, in increasing order of invasiveness:

1. **Change the target's `goos` to a name that no TinyGo runtime file
   claims.** Use `"goos": "none"` (or similar) in `src/target.json` and
   supply gooos-local replacements for the missing symbols. The catch:
   several other TinyGo runtime files key on `goos=linux` (e.g., heap
   initialization paths), so they must also be replaced.
2. **Add a build tag** such as `baremetal` in `target.json`'s
   `"build-tags"` array and vendor the minimum set of TinyGo runtime
   files that need different bodies. Requires either patching TinyGo
   locally or copying those files into gooos and telling the build to
   prefer the copies. TinyGo's own microcontroller ports (e.g.,
   `runtime_nrf_bare.go`) already use this pattern — study them first.
3. **Fork / vendor the TinyGo runtime entirely.** Heaviest but most
   transparent. Gives gooos a stable target that does not drift when
   TinyGo releases.

The design **mandates that a spike verify which option actually links
on `x86_64-unknown-linux-elf` before any other work begins.** The
spike deliverable is: a trivial `main()` that does `go func(){}()` and
builds to a valid ELF with no undefined symbols. Until that is done,
the rest of the plan is conjectural.

### 5.2 Once the build-tag problem is solved: the function bodies

After the chosen option opens a slot for gooos-local definitions, the
bodies look like:

```go
func sleepTicks(d timeUnit) {
    deadline := pitTicks + uint64(d)
    for pitTicks < deadline {
        sti()
        hlt()
        cli()
    }
}

func ticks() timeUnit { return timeUnit(pitTicks) }

func ticksToNanoseconds(t timeUnit) int64  { return int64(t) * 10_000_000 }
func nanosecondsToTicks(ns int64) timeUnit { return timeUnit(ns / 10_000_000) }

func deadlock() {
    serialPanicPrint("runtime: all goroutines are asleep - deadlock!")
    for { hlt() }
}

//export tinygo_register_fatal_signals
func tinygo_register_fatal_signals() {} // no-op bare-metal
```

Note: these are package-level in the `runtime` package (via vendoring)
**or** in a gooos file selected by the new build tag — *not* external
`//go:linkname` bridges in `src/goroutine_stubs.go`. The reviewer pass
on v1 of this design flagged that as the single largest omission.

Minimum sleep granularity is 10 ms (PIT at 100 Hz, `src/pit.go`).
Documented in `goroutine_design_gc_and_smp.md §8`.

### 5.3 Preemption hook from the timer ISR (NOT `task.Pause`)

Upstream TinyGo's `task.Pause()`
(`/usr/local/lib/tinygo/src/internal/task/task_stack.go:51-52`) panics
if called from inside an interrupt (`interrupt.In()` returns true).
Therefore **`handleTimer` must NOT call `task.Pause`.** v1 design:

- `handleTimer` increments `pitTicks`, wakes any expired sleepers *by
  updating the runtime's `sleepQueue` head timestamps* (a small helper
  `runtime_advanceSleepQueue()` provided by gooos — a vendored
  addition to TinyGo's runtime, or a hand-rolled substitute if
  vendoring is rejected), and sends EOI.
- Preemption of the currently running goroutine does **not** happen
  inside the ISR. Instead, a boolean `wantReschedule` is set, and
  cleared by the main scheduler loop on its next iteration. The
  scheduler loop runs on the system stack between `task.Pause` /
  `task.Resume` boundaries, which is an interrupt-free context.
- Ring-3 user tasks still get preempted on timer tick because the
  `iretq` epilogue that returns from the timer ISR first checks
  `wantReschedule` and, if set, calls a gooos-local trampoline
  (`runtime_reschedule_from_isr_epilogue`) that performs one
  `task.Pause()` from *non-interrupt context* (the epilogue
  decrements `in_interrupt_depth` to zero first) and then `iretq`s.
  Naming the concrete runtime entry is part of the R-runtime-collision
  spike — the runtime's own `scheduler()` function is not exported
  upstream, so either vendoring exposes it or the trampoline calls
  `task.Pause` on the current goroutine and lets the runtime's main
  loop pick the next runnable. Implementation lands in `src/isr.S`
  plus a small helper in the new `src/goroutine_irq.go`.

### 5.4 `interrupt.In()` for baremetal amd64

TinyGo ships `interrupt.In()` only for targets that provide a backend.
There is no amd64 baremetal provider today. v1 adds one:

- `src/isr.S`: the common ISR prologue increments a global
  `in_interrupt_depth uint32`; the epilogue decrements it.
- New Go file provides
  `//go:linkname interruptIn internal/task.interruptIn` (exact name
  adjusted to match what `task_stack.go:51` references) returning
  `in_interrupt_depth != 0`.

This is cheap (~3 instructions per ISR entry/exit) and makes
`task.Pause`'s check meaningful.

### 5.5 `runtime_alloc` bridge

The current conservative GC already exports `runtime.alloc` via
`//go:linkname runtime_alloc runtime.alloc`. Signature:
`(size uintptr, layout unsafe.Pointer) unsafe.Pointer` matches
`task_stack.go:75`. No change, but re-confirm after the build-tag fix
lands.

**Allocation reentrancy warning**: `go func(){}()` triggers
`task.start → initialize → runtime_alloc`. `go` **must not be called
from an ISR**. This already follows from the ISR safety rule in
`goroutine_design_channels_and_isr.md §3.1`; the design elevates it
here because `runtime_alloc` reentry during the GC mark phase
(`gc_blocks.go:444-451` wraps the runqueue scan in
`interrupt.Disable()`/`Restore`) can corrupt the collector. Document
in the `src/goroutine_stubs.go` comment block.

## 6. Observable differences from the current scheduler

| Property                                      | Today                         | After migration                  |
|-----------------------------------------------|-------------------------------|----------------------------------|
| Concurrent unit                               | `Task` slot (max 32)          | goroutine (limited by heap)      |
| Blocking primitive                            | `WaitQueue`                   | channel parked via `task.Pause`  |
| Kernel-side sleep                             | `taskSleep(ticks)`            | `time.Sleep(d time.Duration)`    |
| Preemption of kernel code                     | Timer-driven                  | Cooperative                      |
| Preemption of Ring-3 user code                | Timer-driven                  | Timer-driven (unchanged)         |
| Number of kernel stacks                       | 1 per Task (8 KiB)            | 1 for ISRs + 1 per goroutine     |
| TSS.RSP0 updates per context switch           | Yes (`tssSetRSP0`)            | Only when a Ring-3 goroutine resumes |
| Service "tasks"                               | `serialTask`, `fsTask`        | `go serialTask()`, `go fsTask()` |
