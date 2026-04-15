# Phase B — Ring-3 Exec Migration (B9)

The trickiest migration. Converts `elfExec` (`src/process.go:87-193`)
and `processExit` (`src/process.go:198-244`) from the hand-written
scheduler's `createTask` / `tasks[]` state manipulation to a
goroutine + channel model. Every invariant the custom scheduler
guarantees to Ring-3 user pages must be preserved — or a concrete
replacement cited.

## 1. Current flow (inventory)

```
shell task (Task 0) calls sys_exec
   -> syscall dispatcher invokes elfExec("ls.elf", args, 0)
   -> elfExec:
      - read ELF bytes via fsSendRead
      - savedParent[0] = shell's UserPages/UserPaddrs
      - unmapPage each shell user page
      - childID = createTask(elfExecTrampolineAddr())
      - childProc set up (EntryPoint, StackTop, ArgString, etc.)
      - for each PT_LOAD: allocPage + mapPage + copy ELF data
      - allocate arg page + user stack (2 pages)
      - tasks[parent].State = taskBlocked
      - schedule()  // switches to child task via switchContext
         - child runs elfExecTrampoline -> sti() -> jumpToRing3
         - Ring 3 code runs until sys_exit triggers processExit
      - processExit:
         - unmap + free child user pages
         - restore shell's pages from savedParent
         - tasks[parent].State = taskReady
         - tasks[currentTask].State = taskExited
         - schedule()  // switches back to shell
      - shell resumes inside schedule() -> back in elfExec
      - elfExec returns childID, true
```

## 2. Target flow

```
shell goroutine (already spawned by B7) calls sys_exec
   -> syscall dispatcher invokes elfExec("ls.elf", args, 0)
   -> elfExec:
      - read ELF bytes via fsSendRead (B4 makes this native chan)
      - savedParent = shell's UserPages/UserPaddrs (as today)
      - unmapPage each shell user page (as today)
      - exitCh := make(chan uintptr, 1)
      - proc = allocate Process, fill EntryPoint/StackTop/ArgString
      - for each PT_LOAD: allocPage + mapPage + copy ELF data
      - allocate arg page + user stack
      - go ring3Wrapper(proc, exitCh)
      - exitCode := <-exitCh                         // parent blocks
      - processExit (now running on the child's goroutine before
        the wrapper returns) has set up the restore and sent exitCh
      - elfExec returns exitCode, true
```

```
ring3Wrapper(proc, exitCh):
   tssSetRSP0ForCurrentG()   // TSS per Ring-3 goroutine (§4)
   jumpToRing3(proc.EntryPoint, proc.StackTop)
   // jumpToRing3 never returns. The Ring 3 program runs until it
   // issues int 0x80 sys_exit, which lands in processExit(exitCode).
```

```
processExit(exitCode):
   // now runs in the child goroutine's syscall-handler context,
   // on the child goroutine's stack (the one tssSetRSP0 pointed to).
   unmap + free child user pages
   restore parent's pages from savedParent
   proc.exitCh <- exitCode   // wake the parent's <-exitCh
   // child goroutine then dies: its wrapper never returns, so
   // processExit must not return either — jump into an infinite
   // trap so the runtime doesn't hand control back to Ring-3.
   for { hlt() }
```

## 3. `ring3Wrapper` detailed design

```go
// src/process.go

func ring3Wrapper(proc *Process, exitCh chan<- uintptr) {
    // 1. Point TSS.RSP0 at this goroutine's own stack so that a
    //    Ring-3 → Ring-0 transition via int 0x80 lands safely.
    //    Must happen before the jumpToRing3 call below.
    tssSetRSP0ForCurrentG()

    // 2. Stash the exit channel on the Process struct so
    //    processExit (called later from this same goroutine's
    //    kernel stack after int 0x80) can reach it.
    proc.exitCh = exitCh

    // 3. Enter Ring 3. Never returns.
    jumpToRing3(proc.EntryPoint, proc.StackTop)
}
```

The `exitCh` is a *new field* on `Process`:

```go
// src/process.go

type Process struct {
    // existing fields (TaskID, ParentTaskID, ArgString, UserPages…)
    exitCh chan uintptr
}
```

### 3.1 Why `exitCh` is stored on `Process`

The `int 0x80` syscall dispatcher runs on the same goroutine's
kernel stack (TSS.RSP0 points there). `processExit` is called
from the dispatcher and needs to know which channel to send on.
The syscall dispatcher identifies the process via `currentTask`
today — under B7/B9 it instead uses `task.Current()` or a
goroutine-local via `proc` reachable through a process-table
lookup keyed on the current goroutine. The simplest and least
invasive approach: look up the process via `processes[idx]` where
`idx` is found by a side table keyed on `task.Current()`.

### 3.2 Alternative: store `exitCh` in a pkg-level map

Instead of a field on `Process`, use:

```go
var exitChByTask = make(map[*task.Task]chan uintptr)
```

Populated in `ring3Wrapper`, consumed in `processExit` via
`task.Current()`. This isolates the channel from the `Process`
struct.

Trade-off: map needs locking if multiple goroutines mutate it.
Single-CPU v1 doesn't require a mutex but future-proof design
would. Phase B v1 recommends the **field-on-Process** approach
because single-CPU + single-exec-at-a-time guarantees no
concurrent writers.

## 4. TSS.RSP0 per Ring-3 goroutine

This is the core difficulty. Ring 3 → Ring 0 transitions via
`int 0x80` / page fault / timer IRQ all use `TSS.RSP0` as the
new RSP. Different goroutines run on different stacks. So
TSS.RSP0 must point to the *currently executing goroutine's*
kernel stack while that goroutine is in Ring 3.

### 4.1 Recommended v1 implementation: side table keyed on `*task.Task`

TinyGo's per-goroutine stack top is **not** derivable from the
current RSP. `runtime.alloc` returns 16-byte-aligned but not
page-aligned stack allocations (see
`/home/ryo/.local/tinygo/src/internal/task/task_stack.go:73-92`
— `stack := runtime_alloc(stackSize, nil)`). Rounding an arbitrary
on-stack address to the next 4 KiB boundary is overwhelmingly
likely to land *outside* the goroutine's own stack — in a
neighboring heap object, another goroutine's stack, or unmapped
memory. An `int 0x80` that uses that value as RSP0 then corrupts
whatever lives there.

The correct source of truth is the `canaryPtr` stored on each
`task.Task` (`task_stack.go:81`) plus the stack size the
compiler assigned. `canaryPtr` is the bottom; `canaryPtr +
stackSize` is the top. Both fields must be accessed through the
runtime; Phase-B adds a small helper:

```go
// src/goroutine_tss.go

import (
    _ "unsafe"
)

// taskStackTop reads a goroutine's kernel-stack top. Implemented
// via //go:linkname to a new helper in the patched TinyGo
// runtime (runtime_gooos.go) that returns
// canaryPtr + getGoroutineStackSize for the argument task.
//
//go:linkname taskStackTop runtime.gooosTaskStackTop
func taskStackTop(t uintptr) uintptr

//go:linkname taskCurrent internal/task.Current
func taskCurrent() uintptr

type gInfo struct {
    stackTop uintptr
}

var gInfoByTask = make(map[uintptr]*gInfo)

// Called by ring3Wrapper exactly once before jumpToRing3.
func registerRing3G() {
    t := taskCurrent()
    gInfoByTask[t] = &gInfo{stackTop: taskStackTop(t)}
}

// Called on every Ring 3 → Ring 0 transition — directly by
// ring3Wrapper once, and by the goroutine-switch hook (see §4.3)
// every time scheduling resumes a Ring-3 goroutine.
func tssSetRSP0ForCurrentG() {
    t := taskCurrent()
    gi := gInfoByTask[t]
    if gi == nil {
        return // kernel-only goroutine; leave TSS.RSP0 alone
    }
    tssSetRSP0(gi.stackTop)
}

// Cleared by processExit so the map does not leak.
func unregisterRing3G() {
    delete(gInfoByTask, taskCurrent())
}
```

The runtime patch `scripts/patch_tinygo_runtime.sh` gains a new
export:

```go
// runtime_gooos.go (appended to the existing patch)

//go:linkname gooosTaskStackTop runtime.gooosTaskStackTop
func gooosTaskStackTop(t uintptr) uintptr {
    tk := (*internalTask)(unsafe.Pointer(t))
    // canary field offset verified against task_stack.go:21
    return tk.canaryPtr + tk.stackSize
}
```

This avoids any arithmetic guess. The stack top is exactly what
TinyGo recorded at goroutine creation.

### 4.2 Rejected alternative: local-variable-address trick

An earlier draft recommended computing RSP0 from the address of
a local variable:

```go
top := (uintptr(unsafe.Pointer(&marker)) + 4095) &^ 0xFFF
```

Reviewer finding: because TinyGo stacks are not page-aligned,
the rounded value lands outside the goroutine's stack in the
common case. **Do not use.** Documented here only so future
contributors know why §4.1 is the way it is.

### 4.3 TSS.RSP0 update cadence

Today: TSS.RSP0 is updated on every scheduler context switch
(`src/scheduler.go:213`, `tssSetRSP0(tasks[next].KernelStackTop)`).

Under B9 with §4.1 alone: TSS.RSP0 is updated only when
`ring3Wrapper` calls `tssSetRSP0ForCurrentG()`. So TSS.RSP0
points at whichever Ring-3 goroutine last did that. Kernel-only
goroutines (pump, fsTask) never call it.

**Race**: if goroutine A (Ring-3) sets TSS.RSP0, then gets
descheduled (via a channel op or `Gosched`), and goroutine B
(kernel-only) runs, an ISR from B would use A's RSP0. Since A
is descheduled, A's kernel stack area is part of A's saved
state. Writing a new ISR frame there overwrites whatever A had
saved — including its own stack pointer, return addresses, and
any Go locals still live on the suspended goroutine's stack.
The first time A resumes, it returns into garbage.

**Why an ISR-prologue update cannot fix this.** The CPU loads
RSP0 from the TSS *at the point it delivers the trap*, before
any instruction of the handler runs. By the time
`isr_common`'s prologue executes, the damage is done — the ISR
frame is already written to the old (stale) RSP0 location. An
ISR-prologue `movq %rax, 4(%rcx)` that updates TSS.RSP0
helps only a *future* ISR, not the current one.

**Real fix: hook the goroutine-switch path.** TinyGo's
`internal/task/task_stack_amd64.go` defines `Task.resume()`
(`task_stack.go:47`) which calls `swapTask` to enter a
goroutine. Patch that file (via an extension of
`scripts/patch_tinygo_runtime.sh`) to call a gooos-side hook
right before `swapTask`:

```go
// /home/ryo/.local/tinygo/src/internal/task/task_stack_amd64.go
// (patched addition)

//go:linkname gooosOnResume runtime.gooosOnResume
func gooosOnResume(t *Task)

func (t *Task) resume() {
    gooosOnResume(t)        // NEW: gooos-supplied hook
    swapTask(t.state.sp, &systemStack)
}
```

The gooos side implements:

```go
// src/goroutine_tss.go

//go:linkname gooosOnResume runtime.gooosOnResume
func gooosOnResume(t uintptr) {
    if gi := gInfoByTask[t]; gi != nil {
        tssSetRSP0(gi.stackTop)
    }
    // kernel-only goroutines have no Ring-3 concerns; leave
    // TSS.RSP0 at whatever the BSP-boot initScheduler set,
    // which is a dedicated 8 KiB kernel stack allocated in
    // main() for the original boot goroutine.
}
```

This guarantees TSS.RSP0 is the resumed goroutine's stack top
**before** the CPU is allowed to dispatch any trap to it. Races
resolved.

**Patch-script impact**: `scripts/patch_tinygo_runtime.sh` gains
a fifth installed file (the modified `task_stack_amd64.go`) or
an additive patch. Add to the patch script's "Next steps" echo
so implementers remember to re-run after TinyGo upgrades.

**Recommended v1**: §4.1 + §4.3. Do not ship §4.1 alone — the
race is real and surfaces the first time a kernel-only goroutine
preempts a Ring-3 goroutine.

## 5. Parent blocking change

Today (`src/process.go:190-191`):

```go
tasks[parentTaskID].State = taskBlocked
schedule()
```

After B9:

```go
exitCode := <-exitCh  // where exitCh came from exitCh := make(chan uintptr, 1)
```

The parent goroutine parks via TinyGo's channel-recv path
(`task.Pause()`). Any other goroutine runs. When the child
goroutine's `processExit` executes `proc.exitCh <- exitCode`,
the parent wakes.

### 5.1 Buffered vs unbuffered

Use `make(chan uintptr, 1)`. Buffered prevents the child's send
from blocking if (for some reason) the parent hasn't yet reached
`<-exitCh`. Single-CPU guarantees the parent is already parked
before the child runs, but the buffered channel is strictly
safer.

### 5.2 `processExit`'s no-parent case

Today (`src/process.go:228-234`): if no parent, halt forever.

After B9: same behavior. The `proc.exitCh` is `nil` if no parent
was set up (kernel task initialization). Guard:

```go
if proc.exitCh != nil {
    proc.exitCh <- exitCode
}
for { hlt() }
```

## 6. `savedParent` ownership

`savedParent` (`src/process.go:41-44`) is a single global:

```go
var savedParent SavedMapping
```

Used to save the parent's page mappings across an `exec`.
Single-global means only one `exec` can be in flight at a time.

### 6.1 Invariant under Phase B v1

- Only the shell goroutine (spawned by B7) ever calls `elfExec`.
- No kernel-only goroutine (`fsTask`, `keyboardPump`) calls
  `elfExec`.
- Therefore `savedParent` has a single live user — the shell —
  and the original invariant is preserved.

### 6.2 If a future feature adds a second exec'ing goroutine

Convert `savedParent` to `savedParentByTask
map[*task.Task]*SavedMapping`. Noted in `phase_b_overview.md`
risk register as `R-b9-savedparent-race`; not implemented in v1
because the invariant holds.

## 7. Ring-3 guarantees — invariant-by-invariant

| Guarantee                                                   | Today's mechanism                         | Replacement                                   |
|-------------------------------------------------------------|-------------------------------------------|-----------------------------------------------|
| Parent blocks until child exits                             | `tasks[].State = taskBlocked` + schedule()| `<-exitCh`                                    |
| Child runs with child's own user pages mapped               | Set up in `elfExec` before `schedule()`   | Same — mapped before `go ring3Wrapper`        |
| TSS.RSP0 points at child's kernel stack during Ring-3 exec  | `tssSetRSP0` in `schedule()` per switch   | `tssSetRSP0ForCurrentG` in `ring3Wrapper` (§4.1) plus `gooosOnResume` hook on every goroutine switch (§4.3) |
| Child's user pages freed on exit                            | `processExit` freePage loop               | Same — no change                              |
| Parent's user pages restored on exit                        | `processExit` mapPage loop over savedParent| Same — no change                              |
| No two processes run in Ring 3 simultaneously               | Custom scheduler single-core               | Preserved — single goroutine at a time on BSP |
| Child's ELF trampoline runs with interrupts enabled         | `elfExecTrampoline` calls `sti()`         | `ring3Wrapper` — TinyGo's scheduler already runs goroutines with interrupts on |
| `schedule()` hand-off never returns to caller frame         | switchContext jumps into child's stack    | `exitCh`'s recv parks the parent; goroutine scheduler returns the parent goroutine to live state later |

All guarantees preserved.

## 8. `elfExecTrampoline` and `switch.S` entries

`elfExecTrampoline` (`src/process.go:77-81`) still exists because
`ring3Wrapper` calls `jumpToRing3` (which is asm) — it isn't
literally the same function as the old trampoline, but the
purpose overlaps. **B9's commit** deletes:

- the Go body `elfExecTrampoline` in `src/process.go`, and
- the asm stub `elfExecTrampolineAddr` in `src/switch.S:147-150`.

`ring3Wrapper` replaces both. After B9, `src/switch.S` contains
only `taskReturnHaltAddr` (kept until B10 makes the final call
on whether to delete `switch.S` entirely).

`phase_b_teardown.md §4.1` explicitly defers the trampoline
deletion to this B9 commit so B8's diff stays minimal.

## 9. Verification

1. **Build**: `make build` clean.
2. **Sendkey**: 10/10 trials of `ls → cat hello.txt → ls → help`.
   This exercises: spawn, Ring-3 run, `sys_fs_read`,
   `sys_write`, `sys_exit`, parent resume.
3. **Stress**: 5× `ls` + `cat` in one session.
4. **Goroutine smoke tests** per
   `impldoc/goroutine_design_gc_and_smp.md §6.2` — especially
   the "GC under load" test. Add the suite once and run before
   final commit. Remove before landing.
5. **TSS.RSP0 debug** (one-time dev check): add a
   `serialPrintln` inside `tssSetRSP0ForCurrentG` printing the
   computed `top`. Verify after boot and one `ls` that the value
   is within a reasonable range and differs between the shell
   goroutine and the ls goroutine.
6. **Ring-3 PF smoke** (one-time): cause a known page fault
   inside the user program (e.g., dereference `0x1` from a
   modified `hello.elf`) and verify the fatal handler correctly
   prints CR2/RIP and halts — proves TSS.RSP0 pointed somewhere
   valid.

## 10. Dependencies

- **Predecessors**: B3, B4 (the custom channel uses are gone —
  `exitCh` is native), B6 (fatal handlers don't allocate — a
  fault during exec won't corrupt heap), B7 (`createTask` path
  no longer exists for the shell; `elfExec` is called from a
  goroutine).
- **Blocks**: B8 (the last `createTask` caller — `elfExec`'s own
  usage — is migrated; `src/scheduler.go` can be deleted).

## 11. Open questions

- **Can TSS.RSP0 be set only at `ring3Wrapper` entry (§4.1
  alone)?** Probably yes because `ring3Wrapper` is called on
  goroutine startup and the goroutine does not yield before
  `jumpToRing3`. After Ring 3 starts, the next time kernel code
  runs on this goroutine is via `int 0x80` → ISR prologue →
  syscall dispatcher. At that point TSS.RSP0 is still the value
  `ring3Wrapper` installed, and the ISR lands on the correct
  stack. The risk comes from a timer IRQ preempting the
  goroutine mid-Ring-0 work and resuming a *different*
  goroutine, then returning to this one — between those points
  TSS.RSP0 may be stale.
  - Resolution: the first time a kernel-only goroutine gets
    preempted and then a Ring-3 goroutine resumes, the
    `gooosOnResume` hook (§4.3) fires and resets RSP0 to the
    correct stack before any trap can reach the CPU. That hook
    is the v1 recommendation — §4.1 alone is insufficient.

- **Does `ring3Wrapper` goroutine exit need explicit cleanup?**
  After `jumpToRing3`, the goroutine is "stuck" in Ring 3 until
  `sys_exit`. `processExit` sends on `exitCh` then halts via
  `for { hlt() }`. The goroutine never returns from
  `processExit`, so its stack memory is leaked. Under heavy
  exec load (10 execs/sec), this is a slow leak.
  - v1 accepts the leak; sendkey harness is short enough that
    it doesn't matter.
  - v2 fix: `processExit` could set the goroutine to "dead"
    via a runtime hook and let the GC reclaim the stack after
    the next cycle. Requires a TinyGo runtime patch. Deferred.

## 12. Reviewer notes (to be populated after review pass)

(none yet)
