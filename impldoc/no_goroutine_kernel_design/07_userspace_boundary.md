# 07 — User-space boundary

Route C changes nothing about the user-side TinyGo runtime. User
programs keep `go func()`, `chan`, `select`, `time.Sleep`, and the
cooperative `scheduler=tasks` TinyGo runtime that already lives
under `user/` and in the user-side half of
`scripts/tinygo_runtime.patch`. **INVARIANT K5** (§01): the user-side
runtime is not modified by this cycle.

What does change is the Ring-0 host of each Ring-3 process: today
`ring3Wrapper` is a TinyGo goroutine (spawned from
`src/process.go:415` and `src/elf.go:250`); tomorrow it is a kernel
thread (§02). This doc covers the surgery at the Ring 0 / 3
boundary: exec syscall, PML4 ownership, TSS.RSP0 update, syscall
dispatch, SIGALRM delivery, and exit handshake.

## Current state (STATUS-QUO)

Key files:

- `src/process.go:253..278` — `ring3Wrapper`. Acquires a kernel-
  stack slot, registers the TSS.RSP0, calls `writeCR3(proc.pml4)`,
  jumps into Ring 3 via `jumpToRing3`.
- `src/process.go:280..417` — `elfSpawn`. Allocates child PML4,
  maps the ELF, sets up the argument + stack pages, `go ring3Wrapper(child)`.
- `src/elf.go:~250` — second `go ring3Wrapper(proc)` site (for the
  boot shell; `elfSpawn` handles subsequent children).
- `src/userspace.go:454` — `sys_sleep` uses
  `<-afterTicks(ticks)` (inventory L in §06).
- `src/goroutine_tss.go` — `gInfoByTask` map; `registerRing3G`,
  `unregisterRing3G`, `gInfoLock` (rank 3). The map's job: given
  `task.Current()`, find the owning `Process` so `gooosOnResume`
  can update CR3 + TSS.RSP0.
- TinyGo patch: `gooosOnResume` hook is called from
  `~/.local/tinygo/src/internal/task/task_stack*.go` (see the hunks
  that install the hook near `task.resume`). See `src/goroutine_tss.go:195`
  for the gooos-side handler today. The hook reads the next-resumed
  task from TinyGo's scheduler and writes its PML4 / RSP0 to CR3 /
  TSS.RSP0.

The `ring3Wrapper` lifecycle today:

1. `go ring3Wrapper(child)` enqueues a TinyGo task.
2. TinyGo's scheduler eventually picks it; `task.resume` calls
   `gooosOnResume`, which looks up `child` via `gInfoByTask` and
   writes CR3 + TSS.RSP0.
3. `ring3Wrapper` runs, calls `jumpToRing3`.
4. In Ring 3 the program executes until `sys_exit` (→ `processExit`)
   or a fault.
5. `processExit` sends `exitCode` on `proc.exitCh`; the goroutine
   halts.
6. Parent's `processWait` reads the channel.

## Route C state (PROPOSED)

### Exec syscall

The existing `sys_exec` (syscall #4 per
`current_impl_0421_night/05_process_elf_ring3_syscalls_signals.md`)
semantically does "replace my Ring-3 pages with ELF X, reset RIP /
RSP, stay in the same kernel host". Route C keeps the surface API
identical:

- User call: `exec(path, args)` in the user SDK.
- Kernel side: the host kernel thread (the `ring3Wrapper` for this
  process) tears down the old PML4, allocates a new one, loads the
  new ELF, and `jumpToRing3` again — without exiting the kernel
  thread. Exactly as today, only with a kernel thread as host
  instead of a TinyGo goroutine.

**No new syscall introduced**. `sys_exec` (#4) + `sys_spawn` (#16)
+ `sys_wait` (#17) + `sys_waitpid` (#34) keep their numbers and arg
layouts.

### `ring3Wrapper` as a kernel thread

`ring3Wrapper` body rewrites minimally:

- Current:
  ```
  func ring3Wrapper(proc *Process) {
      ring3WrapperHandle = taskCurrent()
      idx, kernelStackTop := ring3StackAcquire()
      …
      registerRing3GWithStack(kernelStackTop, proc)
      tssSetRSP0ForCurrentG()
      …
      writeCR3(proc.pml4)
      jumpToRing3(proc.EntryPoint, proc.StackTop)
  }
  ```
- Route C:
  ```
  func ring3Wrapper(proc *Process) {
      // Hosting kernel thread is kschedRunning[cpuID()].
      // Stack comes from kernel-thread pool (16 KiB), not the
      // Ring-3 pool (8 KiB). ring3StackAcquire is no longer
      // called — §06 Service 8 re-uses the pool for wrappers,
      // but the wrapper's stack IS the kernel thread's stack.
      t := kschedRunning[cpuID()]
      proc.poolIdx = -1  // no separate pool slot; stack is the thread's
      setProcByPoolSlot(…) …  // feature 2.2 ISR-safe lookup keeps working
                              //    (the slot is now t's stack slot)
      setCurrentProc(proc)
      tssSetRSP0ForKernelThread(t)   // NEW helper; replaces
                                      // tssSetRSP0ForCurrentG
      writeCR3(proc.pml4)
      jumpToRing3(proc.EntryPoint, proc.StackTop)
      // unreachable
  }
  ```

`tssSetRSP0ForKernelThread(t)` writes `tss.RSP0 = t.Stack.Top`
(vs. today's `gInfo.kernelStackTop`). The helper lives in
`src/kthread_ring3.go` (PROPOSED, NEW).

### `gInfoByTask` lookup rewrite

The map lives to support `gooosOnResume` (for CR3 updates on every
goroutine context switch). Under Route C there are no kernel
goroutines switching, so the map loses its original job. Two
options:

1. **Delete the map entirely.** CR3 updates now happen only at the
   Ring 3 entry path (`writeCR3` inside `ring3Wrapper`) and at
   Ring 3 return path (kernel-thread host transitions back to
   bootPML4 on the way to `kschedYield`). `gInfoByTask` becomes
   vestigial.
2. **Repurpose as `gInfoByThread *KernelThread → *Process`.** If
   some other code (e.g. signal delivery) still wants a
   thread-to-process lookup that's faster than walking
   `procByPID`, this becomes a Route-C-native table.

Route C prefers option 1 for cleanliness. The current uses of
`gInfoByTask` grep out to:

- `gooosOnResume` — deleted (reason above).
- `registerRing3G` / `unregisterRing3G` — entry/exit points for the
  map; deleted.
- Various diagnostic dumps — rewire to iterate `procByPID` directly.

`gInfoLock` (rank 3) disappears from the lock-order table.

### Per-CPU `CurrentPoolIdx`

`src/percpu.go:30` has `CurrentPoolIdx int32` = "ring3 pool slot
(-1 if kernel)". Today set in `ring3Wrapper` at
`src/process.go:260`. Under Route C the same slot semantics are
kept because feature 2.2 (ISR-safe process lookup) uses it:
`src/lapic_timer.go:121..128` reads `lastFramePtrs[cpuID()]`, checks
`frame.CS & 3 == 3`, and delivers SIGALRM; on the deliver path it
needs the process. Today that's via `getProcByPoolSlot`. Route C
keeps the same API — the "pool slot" just means "the stack slot
allocated for this kernel-thread-that-hosts-ring3". §06 Service 8
repoints `ring3StackPool` to draw from the new kthread stack pool.

### Entry into Ring 3

`jumpToRing3(entry uintptr, stack uintptr)` (asm in `stubs.S`) —
unchanged. Sets up the iretq frame and returns to user space.

**INVARIANT entry-1**: at the moment of `jumpToRing3`, TSS.RSP0 is
the calling kernel thread's stack top. The `iretq` switches to
user stack + user CS/RIP; on the next ring transition (int 0x80 or
IRQ), the CPU loads TSS.RSP0 as the new kernel RSP, which is the
same thread's stack — so the syscall handler runs on the same
kernel stack that the wrapper is using.

This is **already the invariant today** (feature 2.2 assumes it);
Route C preserves it via the new `tssSetRSP0ForKernelThread` helper.

### Return from Ring 3 — syscall path

Current flow:

1. Ring 3 issues `int 0x80`.
2. ISR trampoline saves registers onto the kernel stack (which is
   TSS.RSP0 = this goroutine's kernel stack).
3. Dispatcher reads `rax` = syscall number, calls the handler.
4. Handler runs in kernel context; may sleep (`sys_sleep`), park on
   a channel, etc.
5. Handler returns; dispatcher restores registers; `iretq` back to
   Ring 3.

Under Route C the only changes are in handlers that formerly used
channels:

- `sys_sleep` (`src/userspace.go:454`, inventory L) → replaces
  `<-afterTicks(ticks)` with `kschedTimedPark(ticks)` or
  `KEventAfter(ticks).Wait()`. The kernel thread parks; when the
  wheel fires, it re-enters Ring 3 via iretq.
- `sys_recvfrom` with timeout (`src/netsock.go:593..593+`, inventory
  G) → bounded poll in §06.
- `sys_wait` / `sys_waitpid` — call `proc.ExitEv.Wait()` (per §06
  Service 11). No other change.

**INVARIANT syscall-1**: syscall handlers may park (on any §03
primitive or on `kschedTimedPark`). Parking saves the thread's
state via `kschedSwitch`; when resumed, the handler continues. The
iretq frame on the thread's stack is preserved across park (it's
just stack data).

### Return from Ring 3 — ISR path (preempt / IRQ)

Already covered by §04. Ring-3 SIGALRM delivery via iretq-frame
rewrite (`src/goroutine_irq.go:138`) is unchanged.

### Exit

Current: `processExit` sends on `proc.exitCh`; the goroutine halts.
Under Route C:

```
func processExit(code uintptr) {
    proc := currentProc()
    proc.ExitCode = code
    proc.ExitEv.Signal()
    // … cleanup (unmap pages, release pool slot, etc.) …
    kschedExit(0)  // never returns; scheduler reaps the stack
}
```

`kschedExit` (§02) is the analogue of `return` from a goroutine —
it transitions the thread to `KStateExiting`, yields, and the
scheduler recycles the stack slot.

### `processWait` / `processWaitByPID` rewire

Parent side:

```
func processWait(proc *Process) uintptr {
    proc.ExitEv.Wait()
    code := proc.ExitCode
    // … reap procByPID entry …
    return code
}
```

The existing foreground-transfer logic (`prevForeground`, keyboard
ownership handoff, `getForegroundProc`/`setForegroundProc`) stays
verbatim — it's about who owns stdin, not about the channel.

### Per-process PML4 ownership

Each Ring-3 process owns a PML4 (`Process.pml4`) sharing the kernel
half with `bootPML4`. INVARIANT K2 (§01) says kernel threads stay
on `bootPML4`. How do we reconcile?

- **Before `jumpToRing3`**: kernel thread writes `proc.pml4` to CR3.
- **Inside Ring 3**: CR3 is `proc.pml4` — user pages are mapped,
  kernel half is shared so `int 0x80` can land in kernel code.
- **Inside syscall handler**: CR3 is still `proc.pml4`. Kernel
  code runs because the kernel half is shared. The kernel thread
  never *touches* kernel memory that requires `bootPML4` exclusively
  — kernel data / BSS / text are all in the shared half.
- **Before parking**: the kernel thread does NOT switch CR3 back
  to `bootPML4`. Rationale: the CR3 write is expensive (TLB flush)
  and another Ring-3 entry by the same process on resume is fast
  because CR3 is already `proc.pml4`. The only time we need `bootPML4`
  is if a different process's wrapper runs on the same CPU — at
  which point `kschedSwitch` invokes a hook that writes the newly-
  scheduled thread's PML4.

**PROPOSED hook `kschedSwitchPostCR3(new *KernelThread)`** called
from the Go-wrapper side of `kschedSwitch` after the asm stub
returns (so after the switch completes and `new` is running):

- If `new` is a Ring-3 host (i.e. has a `*Process` bound), write
  `CR3 = proc.pml4` and update `TSS.RSP0`.
- Else if `new` is a pure kernel thread, write `CR3 = bootPML4` if
  the previous CR3 wasn't already `bootPML4`. (Diagnostic only;
  the branch is usually no-op because most kernel threads are pure
  and don't touch CR3 outside boot.)

This consolidates the `gooosOnResume` logic into the scheduler's
swap path. It is the last remaining piece of the gInfoByTask-era
hook.

**Performance note**: two consecutive Ring-3 hosts for the same
process do NOT incur a CR3 write. The hook compares `kschedRunning[cpu].pml4`
to the previous host's `pml4`.

### Signal delivery interaction

Feature 2.2's SIGALRM path (`src/goroutine_irq.go:138`,
`src/lapic_timer.go:121..128`) rewrites the iretq frame in place.
That frame lives on the kernel thread's stack (TSS.RSP0's target).
After rewrite the `iretq` returns to the user SIGALRM handler. No
change needed for Route C — the kernel thread's stack is the frame
host, same as the goroutine's stack was.

`sys_sigreturn` (#36) restores the iretq frame and continues the
user program. Also no change.

## User-side build stays

- `user/target.json` — `scheduler=tasks`, `gc=conservative`.
  **Untouched**.
- `user/rt0.S`, `user/task_stack_amd64.S`, `user/runtime_asm_amd64.S`,
  `user/linker_user.ld` — all untouched.
- `user/gooos/*.go` — the user SDK (`Socket`, `TCPListen`, `go` / `chan`
  demos). Untouched.
- The user-side half of `scripts/tinygo_runtime.patch` (the
  `_user.go` files + every hunk tagged `!kernelspace`) is NOT
  flipped by Route C. See §08 for the per-hunk audit.

## Summary of Ring-0-side touch-points

| File | What changes |
|------|-------------|
| `src/process.go` | `ring3Wrapper` body (remove `ring3StackAcquire` call, remove `registerRing3GWithStack`, swap `tssSetRSP0ForCurrentG` → `tssSetRSP0ForKernelThread`); `processExit` rewire (channel → `KEvent`); `processWait` rewire |
| `src/elf.go` | `go ring3Wrapper(proc)` site at ~line 250 → `kschedSpawn("ring3Wrapper", …)`; `elfSpawn` rewires `make(chan uintptr, 1)` → `proc.ExitEv = KEvent{}`; same for the boot-shell exit-chan around line 190 |
| `src/userspace.go` | `sys_sleep` at line 454 — `<-afterTicks(…)` → `kschedTimedPark(…)` |
| `src/netsock.go` | `sys_recvfrom` / `sys_recv` / `sys_listen` timeouts → bounded poll per §06 |
| `src/goroutine_tss.go` | Delete `gInfoByTask` map + lock; delete `registerRing3G`, `unregisterRing3G`, `gooosOnResume`; keep `checkTaskOffset` safety net (§06 marked it keep) |
| `src/kthread_ring3.go` (NEW) | `tssSetRSP0ForKernelThread`, `kschedSwitchPostCR3` hook |
| `src/ring3_pool.go` | Rewire the channel-based free-list to `KQueue[int32]` per §06 Service 8 |
| `src/percpu.go` | `CurrentPoolIdx` semantics unchanged |

Removed entirely:

- Every usage of `taskCurrent()`, `ring3WrapperHandle`, `fsTaskHandle`
  as "the TinyGo task ID of this service" — these were used for
  scheduler introspection. Route C's equivalent is
  `kschedRunning[cpuID()]` or the `kthreadAll` table.

## Reviewer gates

- User-side runtime untouched: **yes** (INVARIANT K5; `user/target.json`
  / `user/*.S` / `user/gooos/*.go` all untouched).
- Per-process PML4 / TSS.RSP0 owner named per phase: **yes**
  (`ring3Wrapper` on entry, `kschedSwitchPostCR3` on thread switch,
  unchanged on syscall/IRQ).
- Ring-3 SIGALRM path preserved: **yes** (§04 cross-ref; iretq-frame
  rewrite unchanged).
- No user-side syscall numbers change: **yes**.
- `gInfoByTask` disposition explicit: **yes** (deleted; rank 3 lock
  gone).
