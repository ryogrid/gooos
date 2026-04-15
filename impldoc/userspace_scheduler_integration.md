# Userspace Scheduler Integration

This document specifies how TinyGo's `scheduler=tasks` loop
inside a Ring-3 user program cooperates with the gooos kernel
through the existing syscall ABI. It resolves blocker U9 from
`userspace_goroutines_overview.md §2` (the blocking-syscall
limitation, documented as accepted).

Depends on `userspace_tinygo_runtime.md` for the build-tag
convention and the new `runtime_gooos_user.go` file that
provides `sleepTicks` / `ticks` / `ticksToNanoseconds` /
`nanosecondsToTicks`.

## 1. Problem statement

TinyGo's `scheduler=tasks` runtime, once enabled, runs a
cooperative scheduler loop per process (`scheduler_any.go`,
`scheduler_tasks.go`, plus `internal/task/*` context-switch
machinery). It needs two primitives from the host OS:

- **`runtime.Gosched`** — voluntary yield; put the current
  task at the back of the runqueue and dispatch another.
- **`sleepTicks(d timeUnit)`** — wake after `d` ticks. In a
  baremetal kernel this is the `sti/hlt/cli` idle loop; in
  userspace it must block until the kernel is ready to
  resume this process.

The kernel already exposes these as syscalls:

- `sys_yield` = number 7 (`src/userspace.go:54`,
  handler `sysYieldHandler`).
- `sys_sleep` = number 8 (`src/userspace.go:55`,
  handler `sysSleepHandler`).

No new syscall is strictly needed. This document spells out
how the userspace runtime wires each primitive, what happens
under idle / deadlock, and the implications of the
"blocking-syscall freezes the whole process" limitation.

## 2. `runtime.Gosched` → `sys_yield`

### 2.1 Wiring

TinyGo's `runtime.Gosched` in `scheduler_any.go` is:

```go
func Gosched() {
    runqueue.Push(task.Current())
    task.Pause()
}
```

That's pure-userspace: put the running task on the runqueue,
then call `task.Pause()` which invokes
`internal/task/task_stack_amd64.go:resume()` and yields to
whatever is next. No syscall is required — the userspace
scheduler keeps CPU time on its own as long as any task is
runnable.

**When does the kernel get involved at all?** Only when
there are no runnable tasks. At that point TinyGo calls
`sleepTicks(minWait)` via the scheduler idle path in
`scheduler.go`. Our `sleepTicks` impl in
`runtime_gooos_user.go` (§3) delegates to `sys_sleep`.

### 2.2 Why we don't rewire `Gosched` to call `sys_yield`

Calling `sys_yield` inside `Gosched` would be redundant: it
would ask the kernel to swap this user process out, which
kicks in only useful work when there are OTHER user
processes waiting. If every user goroutine yield triggered
a kernel rescheduling, the TinyGo scheduler could never
batch multiple task switches inside one kernel quantum.

Leaving `Gosched` as pure-userspace is the performant
choice. `sys_yield` is retained as a userland-callable
syscall for user programs that explicitly want to give up
the CPU to another process (e.g., a CPU-bound loop
cooperatively sharing with the shell).

## 3. `sleepTicks` → `sys_sleep`

### 3.1 Wiring

Per `userspace_tinygo_runtime.md §3`,
`runtime_gooos_user.go:sleepTicks` is:

```go
func sleepTicks(d timeUnit) {
    if d <= 0 { return }
    userSyscall1(8, uintptr(d)) // sys_sleep
    userTicks += uint64(d)
}
```

### 3.2 Kernel side

`sysSleepHandler` in `src/userspace.go:338-344` already
does what we need:

```go
func sysSleepHandler(frame *SyscallFrame) {
    ticks := uint64(frame.RDI)
    if ticks > 0 {
        time.Sleep(time.Duration(ticks) * 10 * time.Millisecond)
    }
    frame.RAX = 0
}
```

The kernel's `time.Sleep` runs on top of TinyGo's patched
kernel runtime (see `src/runtime_gooos.go`'s `sleepTicks`
→ `sti/hlt/cli` idle dance plus the runtime's `sleepQueue`
wake-up logic). While this sleep is in flight, the calling
`ring3Wrapper` goroutine is parked on the timer queue, so
other kernel goroutines — `fsTask`, `keyboardPump`, and
sibling Ring-3 processes' own ring3Wrappers — keep running.
The sleeping user process resumes once the timer fires and
the scheduler picks its goroutine back up.

### 3.3 TinyGo idle path

`scheduler.go`'s main loop (kernel-side and userspace
share this source):

1. Pop a task from runqueue; if non-nil, resume it.
2. If nil but `sleepQueue` has tasks, compute the
   shortest time-to-wake, call `sleepTicks(remaining)`,
   loop.
3. If nil AND `sleepQueue` empty, call `waitForEvents`.
4. `waitForEvents` default is no-op (`wait_other.go`); if
   the runtime panics "all goroutines are asleep" it
   means deadlock.

For userspace, step 2's `sleepTicks` goes to `sys_sleep`
which parks the whole user process for the requested
duration, then returns. The userspace scheduler loops,
re-checks runqueue (a task that was sleeping may have
expired), etc.

## 4. Blocking-syscall limitation

### 4.1 The trap

If ANY user goroutine issues a syscall whose kernel handler
parks (via chan op, `<-exitCh`, etc.), the entire user
process's `ring3Wrapper` goroutine parks in kernel. That
means the user's internal scheduler loop doesn't run
either — every goroutine in the process freezes.

Examples of user-observable blocking-syscall points:

| Syscall | Handler blocks because | Affects |
|---|---|---|
| `sys_read(0, …)` (stdin) | `<-keyboardCh` if empty | whole process |
| `sys_read(fd, …)` on a pipe read end | `<-pipe.ch` if empty | whole process |
| `sys_wait(pid)` | `<-child.exitCh` | whole process |
| `sys_fs_read(path, …)` | briefly — `fsReqCh` send + reply | whole process (usually fast) |
| `sys_exec(path, args)` | `<-child.exitCh` (legacy; `sys_spawn` + `sys_wait` split is the new way) | whole process |

### 4.2 Why this is accepted for v1

Fixing this properly requires either:

- **Non-blocking syscall variants** (`sys_poll`, etc.) so a
  userspace goroutine can opt-in to a "would block → return
  EAGAIN" path and the scheduler can keep running other
  goroutines. This is a kernel-side feature multiplier;
  every blocking-capable syscall needs a non-blocking twin.
- **Kernel-preemptive userspace** — the user goroutine
  isn't really a kernel thread; a timer-driven preemption
  inside the user process's ring3Wrapper could yield to the
  userspace scheduler. That requires signal-like machinery
  (SIGURG equivalent) we haven't designed.

Neither is in scope for this round. We accept the
limitation and document the usage pattern that avoids it.

### 4.3 Recommended userspace pattern

When concurrent I/O-plus-compute is desired, structure the
user program so the blocking I/O is the tail of the
goroutine pipeline:

```go
func main() {
    // Launch compute goroutines freely.
    go computeWorker(ch1)
    go computeWorker(ch2)
    // Do blocking I/O as the last thing in main.
    for {
        n := gooos.Read(gooos.Stdin, buf[:])
        if n <= 0 { break }
        // hand off to compute via chan
    }
}
```

While `main` is parked in `sys_read`, the compute
goroutines can't run either — but that's the same
limitation as running the whole program single-threaded, so
the user hasn't lost anything by introducing goroutines.
Goroutines pay off when multiple computations can overlap
between I/O events (e.g., `select` over multiple chan ends
with one of them driven by a pipe read end).

### 4.4 Sibling-process overlap (already works)

This is important: the blocking-syscall limitation is
**per-process**. Two USER PROCESSES, each single- or multi-
goroutine, still overlap because the kernel scheduler
dispatches between them. A pipe `cat hello.txt | wc` has
`cat` and `wc` as separate Ring-3 processes (per
kernel-side `elfSpawn` in `src/process.go`, each with its
own ring3Wrapper goroutine); they run concurrently through
the kernel's Gosched. Within each process, a single
goroutine (main) does its blocking I/O and the other process
progresses meanwhile.

So the limitation affects only "I want two things to
overlap INSIDE the same user process." Multi-process
pipelines are unaffected.

## 5. Deadlock detection

TinyGo's scheduler built-in: if the runqueue and sleepQueue
are both empty AND no timers are armed, `waitForEvents`
returns immediately and the runtime panics with a
"fatal error: all goroutines are asleep - deadlock!"
equivalent message.

In userspace, the panic message is produced via `putchar`
(→ `sys_write(1, …)` in our stub). The user sees it on
serial just like any other panic. Then `exit(1)` via our
`runtime_gooos_user.go:exit` drops into `sys_exit`, which
terminates the process cleanly.

No extra gooos-side work is needed. The TinyGo runtime's
existing deadlock panic is the correct user-visible signal.

## 6. Timer queue / `time.After`

`time.After` builds on `nanosecondsToTicks` +
`addSleepTask`. Our `nanosecondsToTicks(ns int64) timeUnit
{ return timeUnit(ns / 10_000_000) }` matches the kernel's
10-ms PIT granularity.

Sub-10-ms sleep values round to 1 tick (10 ms minimum). The
`R-sleep-granularity` risk from
`impldoc/deferred_hygiene.md §6` applies unchanged — the
floor is shared between kernel and userspace.

## 7. Files referenced

- `src/userspace.go` (no changes) — existing
  `sysYieldHandler`, `sysSleepHandler` entries in the
  dispatch table at roughly lines 93-96 and handlers further
  down.
- `~/.local/tinygo/src/runtime/scheduler_tasks.go:1` —
  `//go:build scheduler.tasks`; provides
  `getSystemStackPointer()`.
- `~/.local/tinygo/src/runtime/scheduler_any.go:1` —
  `//go:build !scheduler.none`; provides `runqueue`,
  `ready()`, `schedule()`, `Gosched()`.
- `~/.local/tinygo/src/runtime/scheduler.go` — the main
  scheduler loop and `waitForEvents` fallback.
- `~/.local/tinygo/src/internal/task/task_stack.go` (patched
  by `scripts/tinygo_runtime.patch`) — `Pause`, canary check.
- `impldoc/userspace_tinygo_runtime.md §3` — where the
  `sleepTicks` / `ticks` bodies live.
- `src/userspace.go:69-97` — canonical syscall dispatch
  switch (authoritative table).
- `impldoc/busybox_syscall_abi.md` — narrative on the
  syscall register ABI (RAX=nr; RDI/RSI/RDX/R10 args).

## 8. Dependencies

- `userspace_tinygo_runtime.md` for the runtime file that
  contains `sleepTicks` et al.

## 9. Verification

1. `user/cmd/goprobe/main.go` (defined in
   `userspace_verification.md §2`) spawns a goroutine that
   calls `time.Sleep(20 * time.Millisecond)` in a loop;
   serial observes the scheduler correctly yielding to the
   kernel via `sys_sleep` and resuming.
2. `runtime.Gosched()` inside a user program cycles through
   multiple goroutines without any kernel transition (count
   `sys_sleep` occurrences on serial and confirm they
   match only the `time.Sleep` call sites, not
   `Gosched`).
3. Blocking-syscall freeze probe: a user program with two
   goroutines where one calls `gooos.ReadLine()` (blocks
   on keyboard) and the other increments a counter.
   Confirm the counter stops incrementing while waiting
   for a keystroke (validates the documented limitation,
   not a bug).
4. Deadlock probe: a user program that `chan`-parks every
   goroutine with no wakeup source. Confirm TinyGo's
   built-in deadlock panic fires and `sys_exit` unwinds
   cleanly (process exit observed on serial).

## 10. Open questions

1. **Does `sys_sleep` behave correctly when passed
   `ticks=0`?** Current kernel handler (`src/userspace.go`)
   computes `deadline := pitTicks + 0` and loops while
   `pitTicks < deadline` — which is immediately false, so
   it returns without yielding. That's fine for our
   `sleepTicks` caller which skips `d <= 0`.
2. **Can `sys_sleep` be interrupted?** No — it's a tight
   loop inside the syscall handler with no cancellation.
   A user program sleeping can't be interrupted by the
   shell via Ctrl-C (no signals in scope). Documented.
3. **Should we expose `runtime.Gosched()` as
   `gooos.Yield()` in the userland API?** Already there
   (`user/gooos/proc.go:Yield` calls `sys_yield`). With
   the new runtime, `gooos.Yield()` triggers the kernel-
   level yield (across processes); `runtime.Gosched()` is
   the userspace-only yield (within process). Keep both
   and document the difference.

## 10.5 Reviewer follow-ups (MINOR, left as-is)

- **Deadlock panic stack safety**: the TinyGo deadlock
  panic path prints via `putchar` on the currently
  running goroutine's stack. If that goroutine's canary
  is already trashed (the scenario that triggers
  `gooosStackOverflow`), the panic message may not
  complete — but that's a different code path from the
  canary check itself. For v1 we accept the message may
  be partial in the overflow-then-deadlock edge case;
  no mitigation implemented.

## 11. Risk register delta

- **Retires**: `R-userspace-no-Gosched`,
  `R-userspace-no-sleep` (both resolved by the existing
  syscalls plus the new runtime file's wiring).
- **Adds**: `R-userspace-blocking-syscall-freeze` —
  documented, accepted, pattern workaround recommended.
