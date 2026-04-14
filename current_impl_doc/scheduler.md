# Scheduler and Process Lifecycle

## Task Table (`src/scheduler.go`)

```go
type Task struct {
    SP             uintptr  // saved RSP (set by switchContext assembly)
    State          uint8    // one of: Running(0), Ready(1), Blocked(2), Exited(3), Free(4)
    ID             uint32
    StackBase      uintptr  // base of 4 KiB context-switch stack
    KernelStackTop uintptr  // top of 8 KiB per-task kernel stack (for TSS RSP0)
    WakeupTick     uint64   // PIT tick for sleep queue wakeup
}
```

- `maxTasks = 32`, `taskStackSize = 4096`
- `taskCount` is a high-water mark (never decremented)
- `currentTask` tracks the currently running task ID

## Task Creation (`createTask`)

1. Scans for reusable slots (`State == taskFree || taskExited`); frees old stack if present
2. Falls back to appending at `taskCount++`
3. Allocates **4 KiB** context-switch stack via `allocPage()`
4. Allocates **8 KiB** (2 pages) per-task kernel stack for Ring 3 -> Ring 0 transitions
5. Builds initial stack frame (growing downward):
   - `taskReturnHaltAddr()` (safety net)
   - `entryAddr` (return address for `switchContext`'s `ret`)
   - Six zero-initialized callee-saved registers (rbx, rbp, r12-r15)

## Round-Robin Scheduling (`schedule`)

Called from PIT timer handler (`handleTimer` at 100 Hz) and from `waitQueueSleep`.

1. Returns immediately if `!schedReady` or `taskCount <= 1`
2. Scans tasks `(currentTask+1) % taskCount` through `currentTask` for `State == taskReady`
3. **If no ready task found**: returns immediately (no `sti+hlt` here to avoid ISR stack recursion)
4. **If ready task found**:
   - Old task: set to `taskReady` if was `taskRunning`
   - New task: set to `taskRunning`
   - Update TSS RSP0: `tssSetRSP0(tasks[next].KernelStackTop)`
   - `switchContext(&tasks[old].SP, tasks[next].SP)`

## Context Switch (`src/switch.S:switchContext`)

```asm
switchContext(oldSP *uintptr, newSP uintptr):
    push rbx, rbp, r12, r13, r14, r15   (save callee-saved)
    mov  [rdi], rsp                      (save current RSP to *oldSP)
    mov  rsp, rsi                        (load new task's RSP)
    pop  r15, r14, r13, r12, rbp, rbx   (restore callee-saved)
    ret                                  (resume at saved return address)
```

## WaitQueue and Idle Loop

```go
type WaitQueue struct {
    ids   [16]uint32  // FIFO task ID list
    count int
}
```

**`waitQueueSleep(wq)`** — The idle loop:
```go
func waitQueueSleep(wq *WaitQueue) {
    wq.ids[wq.count] = currentTask
    wq.count++
    tasks[currentTask].State = taskBlocked

    for tasks[tid].State == taskBlocked {
        schedule()                    // try to switch to a ready task
        if tasks[tid].State == taskBlocked {
            sti()                     // enable interrupts briefly
            hlt()                     // wait for one IRQ (timer/keyboard)
        }
    }
}
```

This design avoids ISR stack recursion: `schedule()` returns immediately when idle (no `sti+hlt` inside schedule), and `waitQueueSleep` handles the idle wait. `handleTimer` -> `schedule()` -> return is always shallow.

**`waitQueueWakeOne(wq)`**: dequeues first task, sets `taskReady` (safe from interrupt context)

## Sleep Queue

Separate from WaitQueue. `taskSleep(ticks)` inserts the task into a sorted array, and `sleepQueueWakeExpired(now)` is called from the timer handler to wake expired tasks.

## Per-Task Kernel Stacks and TSS RSP0

Each task has its own 8 KiB kernel stack for Ring 3 -> Ring 0 transitions. When a Ring 3 task calls `int 0x80`, the CPU loads RSP from `TSS.RSP0`. Before every context switch, `schedule()` updates TSS RSP0 to the next task's `KernelStackTop` via `tssSetRSP0()` (`src/gdt.go`).

This prevents ISR frame corruption: when the shell (task 0) blocks inside `sys_exec` and a child (task 3) runs in Ring 3, each task's `int 0x80` pushes its ISR frame on its own kernel stack.

## Process Lifecycle (`src/process.go`)

### Process Struct
```go
type Process struct {
    TaskID       uint32
    ParentTaskID uint32              // 0xFFFFFFFF = no parent
    ExitCode     uintptr
    ArgString    [256]byte
    ArgLen       int
    UserPages    [512]uintptr        // mapped virtual addresses
    UserPaddrs   [512]uintptr        // corresponding physical addresses
    UserPageCnt  int
    HeapBreak    uintptr             // current heap break (sys_sbrk)
    EntryPoint   uintptr             // ELF entry point
    StackTop     uintptr             // user stack top
    Used         bool
}
```

### elfExec Flow

1. Read ELF from FS via `fsSendRead` (channel-based, blocks via schedule)
2. Parse ELF headers (`elfParse`)
3. **Save parent's page mappings** to `savedParent` (single global — one nesting level)
4. **Unmap parent's pages** (PTEs cleared, physical pages NOT freed)
5. Create child task via `createTask(elfExecTrampolineAddr())`
6. Map child's PT_LOAD segments (skip already-mapped pages via `walkAndGetPaddr`)
7. Map argument page at `0x40300000`, copy args
8. Map user stack (2 pages at `0x7FFF0000`)
9. Set `EntryPoint`, `StackTop`, `HeapBreak`
10. Block parent (`taskBlocked`), call `schedule()`

### elfExecTrampoline

```go
func elfExecTrampoline() {
    sti()
    proc := &processes[currentTask]
    jumpToRing3(proc.EntryPoint, proc.StackTop)
}
```

### processExit Flow

1. Unmap and free (no-op) all child user pages
2. Store exit code
3. **Restore parent's saved page mappings** via `mapPage` for each entry in `savedParent`
4. Set parent to `taskReady`
5. Mark child process unused, task `taskExited`
6. Call `schedule()` (switches to parent)

Parent resumes in `elfExec` at `schedule()` return → `sysExecHandler` reads exit code → `iretq` back to Ring 3.
