# BusyBox Shell — Required Kernel Modifications

## 1. Filesystem Expansion (`src/fs.go`)

### Current State
- `maxFiles = 16`, `maxFileData = 4096` (4 KiB per file)
- No file deletion

### Changes

```
maxFiles    = 32
maxFileData = 65536   // 64 KiB per file
```

Add `fsDelete(name string) bool`:
- Finds the file entry by name, sets `used = false`, zeroes name and size
- Returns false if file not found

Add the `fsOpDelete` operation (op code 4) to the channel-based FS task, and a `fsSendDelete(name) bool` helper.

### Rationale
TinyGo-compiled binaries for simple programs are typically 10-40 KiB. With 6+ command binaries plus a shell binary, 32 files at 64 KiB provides sufficient capacity. Since the FS uses fixed-size arrays, increasing `maxFileData` to 64 KiB increases the `.bss` section by approximately `32 * 64 KiB = 2 MiB`. The kernel's 4 MiB heap and the linker script's `.bss` allocation must be verified to accommodate this.

**Alternative**: If 2 MiB of `.bss` is too large, use dynamically allocated file data buffers via `allocPage()` (16 pages = 64 KiB per file). This avoids static allocation but adds complexity.

## 2. Task Recycling (`src/scheduler.go`)

### Current State
- `maxTasks = 16`, all slots currently used
- `taskExited` tasks are never cleaned up; `taskCount` never decrements

### Changes

Increase `maxTasks` to 32.

Add `taskReclaim(id uint32)`:
1. Verify `tasks[id].State == taskExited`
2. Call `freePage(tasks[id].StackBase)` to reclaim the 4 KiB stack page
3. Zero the `Task` struct fields
4. Mark the slot as available (e.g., `State = taskFree`, a new state value 4)

Modify `createTask()` to reuse slots with `State == taskFree` before appending to the end:
```go
func createTask(entryAddr uintptr) uint32 {
    // First, try to reuse a free slot.
    for i := uint32(1); i < taskCount; i++ {
        if tasks[i].State == taskFree {
            // Initialize this slot (allocate stack, set entry, etc.)...
            return i
        }
    }
    // Fall back to appending at taskCount (high-water mark).
    id := taskCount
    taskCount++
    // Initialize...
    return id
}
```

Add `StackBase uintptr` field to `Task` struct so `taskReclaim` knows which page to free.

**Important**: `taskCount` is a high-water mark — it only increases. The scheduler's `schedule()` function scans from 1 to `taskCount` and skips any task with `State == taskFree` or `State == taskExited`. This avoids the complexity of shrinking the scan range.

### New Task States

| Value | Name | Description |
|---|---|---|
| 0 | `taskRunning` | Currently executing on CPU |
| 1 | `taskReady` | Eligible to be scheduled |
| 2 | `taskBlocked` | Waiting on channel, sleep, or child exit |
| 3 | `taskExited` | Finished execution, awaiting reclamation |
| 4 | `taskFree` | Slot available for reuse |

## 3. Process Lifecycle (`src/process.go` — new file)

This module manages parent-child relationships and the `sys_exec` / `sys_exit` lifecycle.

### Data Structures

```go
type Process struct {
    TaskID       uint32
    ParentTaskID uint32      // Task that called sys_exec (0xFFFFFFFF = no parent)
    ExitCode     uintptr     // Set by sys_exit
    ArgString    [256]byte   // Command-line arguments (fixed buffer, no heap)
    ArgLen       int
    UserPages    [64]uintptr // Virtual addresses of mapped user pages (for cleanup)
    UserPaddrs   [64]uintptr // Corresponding physical addresses
    UserPageCnt  int
    HeapBreak    uintptr     // Current heap break (for sys_sbrk)
    EntryPoint   uintptr     // ELF entry point (for trampoline)
    StackTop     uintptr     // User stack top (for trampoline)
    Used         bool
}

// SavedMapping stores a parent's page mappings during child exec.
type SavedMapping struct {
    Vaddrs [64]uintptr
    Paddrs [64]uintptr
    Count  int
}

var (
    processes    [32]Process
    savedParent  SavedMapping // Only one level of exec nesting (shell → child)
)
```

**Design note**: `ParentTaskID` uses `0xFFFFFFFF` (not 0) as the sentinel for "no parent" because task 0 is a valid task ID (the shell). Only one level of exec nesting is supported (the shell executes a child; the child cannot exec further children).

### `elfExec(filename, args string, parentTaskID uint32) (childTaskID uint32, ok bool)`

1. Read ELF binary from FS via `fsSendRead(filename)`
2. Parse ELF headers via `elfParse(data)`
3. **Save the parent's page mappings**: copy `processes[parentIdx].UserPages` and `UserPaddrs` into `savedParent`
4. **Unmap the parent's user pages**: for each page in `savedParent`, call `unmapPage(vaddr)` — do NOT free physical pages
5. Find or create a free task slot via `createTask(elfExecTrampolineAddr())`
6. For each PT_LOAD segment:
   - Allocate pages, map with user flags, copy file data
   - Record virtual addresses and physical addresses in child's `Process.UserPages[]` and `UserPaddrs[]`
7. Allocate argument page at `0x40300000`, copy args string, record in UserPages
8. Allocate user stack (2 pages at `0x7FFF0000`), record in UserPages
9. Set `Process.HeapBreak` to end of last PT_LOAD segment (page-aligned up)
10. Set `Process.ParentTaskID = parentTaskID`
11. Copy args into `Process.ArgString`
12. Set `Process.EntryPoint` and `Process.StackTop` (for the trampoline)
13. Set child task state to `taskReady`
14. Set parent task state to `taskBlocked`
15. Call `schedule()` — parent will not run until child exits

### `elfExecTrampoline` — Ring 3 entry for child tasks

The child task is created with `createTask(elfExecTrampolineAddr())`, so it starts in Ring 0. The trampoline function:

```go
//export elfExecTrampoline
func elfExecTrampoline() {
    sti()  // Re-enable interrupts (task starts from timer ISR with IF=0)
    // Find the current process's entry point and stack top.
    proc := &processes[currentTask]
    jumpToRing3(proc.EntryPoint, proc.StackTop)
    // Does not return.
}
```

An assembly address helper (`elfExecTrampolineAddr`) provides the function pointer, following the same pattern as `demoTaskAAddr()` in `switch.S`.

### `processExit(exitCode uintptr)`

Called by `sys_exit` handler:
1. Find the `Process` for the current task
2. **Unmap and free** all child pages in `UserPages[]`:
   ```go
   for i := 0; i < proc.UserPageCnt; i++ {
       unmapPage(proc.UserPages[i])
       freePage(proc.UserPaddrs[i])
   }
   ```
3. Set `Process.ExitCode = exitCode`
4. If `ParentTaskID != 0xFFFFFFFF` (has a parent):
   - **Restore the parent's saved page mappings**: for each entry in `savedParent`, call `mapPage(vaddr, paddr, userFlags)` to re-create the parent's PTEs
   - Copy restored mappings back to the parent's `Process.UserPages/UserPaddrs`
   - Find the parent's saved `SyscallFrame` and write `exitCode` to `frame.RAX`
   - Set parent task state to `taskReady`
5. If `ParentTaskID == 0xFFFFFFFF` (no parent, e.g., the shell itself exiting):
   - Execute `cli; hlt` loop to halt the CPU
6. Call `taskReclaim(currentTask)` — frees task slot and kernel stack
7. Call `schedule()` — switches to the parent (which resumes in sys_exec handler)

### Scheduling Note: Blocking inside `int 0x80`

When a syscall handler calls `schedule()` (e.g., `sys_read` blocking for keyboard input, `sys_exec` blocking for child completion), the task switch saves the ISR stack frame. Other tasks run with interrupts enabled (they call `sti()` on entry). Timer and keyboard IRQs continue to fire. When the scheduler eventually switches back to the blocked task, execution resumes inside the ISR handler, which completes and executes `iretq` to return to Ring 3. This is the same pattern used by the existing `sysRecvHandler` and `sysYieldHandler` — it works because `switchContext` preserves the callee-saved registers that `isr_common` uses.

## 4. Syscall Dispatch Redesign (`src/userspace.go`)

Replace the current 6-syscall dispatch with the 12-syscall table defined in `busybox_syscall_abi.md`.

### Key Implementation Notes

**`sys_read` (2) — Line-buffered keyboard input**:

The kernel must implement line editing internally:
1. Allocate a kernel-side line buffer (128 bytes)
2. Loop: receive KeyEvent from `userKeyboardChannel` via `chanRecv`
3. On printable character: append to buffer, echo to VGA+serial
4. On backspace: remove last char, redraw
5. On enter: copy buffer to user memory, return byte count
6. The calling task blocks inside the syscall until enter is pressed

This replaces the current `keyboardConsumerTask` for the user-facing input path. The kernel-side keyboard consumer task remains for kernel-level echo, but the shell's `sys_read` takes priority.

**`sys_exec` (3) — Process execution**:

See `elfExec()` in section 3 above. The syscall handler:
1. Copies filename and args from user memory to kernel strings
2. Calls `elfExec(filename, args, currentTask)`
3. The current task is blocked inside `elfExec` until the child exits
4. When the child calls `sys_exit`, the parent is unblocked and `RAX` contains the exit code

**`sys_sbrk` (10) — Heap growth**:

Each process has a `HeapBreak` pointer initialized to the end of its ELF segments (page-aligned). `sys_sbrk(increment)`:
1. Save `oldBreak = HeapBreak`
2. `newBreak = HeapBreak + increment`
3. Allocate and map pages for any new pages between `oldBreak` and `newBreak`
4. Record new pages in `Process.UserPages[]`
5. `HeapBreak = newBreak`
6. Return `oldBreak`

## 5. User Page Tracking (`src/vm.go`)

### Current State
Pages are allocated and mapped but never tracked per-process. There is no way to unmap all pages belonging to a user process.

### Changes

The `Process.UserPages[64]` array (section 3) tracks every virtual address mapped for a user process. When `processExit()` is called:

```go
for i := 0; i < proc.UserPageCnt; i++ {
    vaddr := proc.UserPages[i]
    // Walk page table to find physical address
    paddr := walkAndGetPaddr(vaddr)
    unmapPage(vaddr)
    if paddr != 0 {
        freePage(paddr)
    }
}
```

Add `walkAndGetPaddr(vaddr uintptr) uintptr` to `vm.go`:
- Walks the 4-level page table like `walkExisting`
- Returns the physical address from the leaf PTE (masked to page frame), or 0 if not mapped

## 6. VGA Console (`src/vga.go` — new file)

### Current State
VGA output uses fixed line assignments (`vgaWriteLine(row, str)`). There is no scrolling or cursor management.

### Changes

Implement a simple VGA console for user output:

```go
var (
    vgaCursorRow int  // Current cursor row (0-24)
    vgaCursorCol int  // Current cursor column (0-79)
)
```

- `vgaConsolePutChar(c byte)`: Write character at cursor, advance cursor. Handle `\n` (newline → move to next row, col 0), `\r` (carriage return → col 0), `\b` (backspace → move back one).
- `vgaConsoleScroll()`: When cursor reaches row 25, shift all rows up by one (memcpy row N+1 → row N), clear row 24.
- `vgaConsolePrint(s string)`: Write each character via `vgaConsolePutChar`.
- `vgaConsoleClear()`: Fill all cells with space, reset cursor to (0, 0).

The kernel's boot status messages continue to use `vgaWriteLine()` during early init. Once the shell starts, all user output goes through the console.

## 7. Embedded User Binaries (`src/user_binaries.go` — generated)

### Current State
The user ELF binary is hand-crafted in `buildUserElf()` within `userspace.go`.

### Changes

A build script (`scripts/embed_elfs.sh`) converts compiled user ELF files to Go byte-array source:

```bash
#!/bin/bash
# scripts/embed_elfs.sh
# Converts user ELF binaries in user/build/ to src/user_binaries.go

echo "package main" > src/user_binaries.go
echo "" >> src/user_binaries.go

for elf in user/build/*.elf; do
    name=$(basename "$elf" .elf)
    varname="userElf_${name}"
    echo "var ${varname} = [...]byte{" >> src/user_binaries.go
    xxd -i < "$elf" >> src/user_binaries.go
    echo "}" >> src/user_binaries.go
    echo "" >> src/user_binaries.go
done
```

In `main.go`, at boot time:
```go
// Store user ELF binaries in the filesystem.
fsCreate("sh.elf")
fsWrite("sh.elf", userElf_sh[:])
fsCreate("cat.elf")
fsWrite("cat.elf", userElf_cat[:])
// ... etc.
```

Remove `buildUserElf()` and the `userElfBinary` array from `userspace.go`.

## 8. Boot Sequence Changes (`src/main.go`)

### Revised Task Allocation

With `maxTasks = 32` and task recycling, the boot-time task allocation becomes:

| Task | Purpose |
|---|---|
| 0 | main/boot → becomes shell (Ring 3) |
| 1-3 | Demo tasks A/B/C (removed for shell — optional, can be kept for debug) |
| 4 | Keyboard consumer task |
| 5 | Serial output task |
| 6 | FS task |
| 7+ | Available for sys_exec child processes |

The channel demo tasks, select demo tasks, unbuffered rendezvous tasks, user print task, and FS demo task are **removed** in the shell build — they were demonstrations, not required services.

### Revised Boot Flow

1. Hardware init (serial, IDT, PIC, PIT, keyboard, VM, SMP, GDT) — unchanged
2. Init scheduler
3. Spawn essential kernel service tasks: keyboard consumer, serial output, FS task
4. Store all user ELF binaries in filesystem
5. Enable scheduling
6. `elfExec("sh.elf", "")` — loads shell, jumps to Ring 3

## 9. Keyboard Input Redesign

### Current State
- `userKeyboardChannel` (channel ID 0): receives packed KeyEvent from IRQ1
- User program does `sys_recv(0, buf, 8)` to receive raw KeyEvents

### Changes for `sys_read`

The `sys_read` syscall needs **line-buffered** input, not raw KeyEvents. Two approaches:

**Approach A (recommended): Kernel-side line editing**

The `sys_read` handler implements line editing directly:
```
sys_read_handler:
    1. Use a kernel-side 128-byte static line buffer (global, not on stack)
    2. Loop:
        a. chanRecv(userKeyboardChannel) → get KeyEvent (scancode, ascii)
        b. If ascii == '\n' (enter): break
        c. If ascii == '\b' (backspace): remove last char, echo backspace to VGA
        d. If printable: append to buffer, echo to VGA+serial
    3. Copy buffer to user memory (up to buf_max bytes)
    4. Return bytes written
```

This keeps the shell simple — it just calls `sys_read` and gets a complete line.

**Keyboard consumer task**: The existing `keyboardConsumerTask` (which echoes to VGA line 12) is **removed** in the shell build. The `sys_read` handler takes over all keyboard echo responsibilities via the VGA console. Only the `userKeyboardChannel` is published to by the IRQ handler. The `keyboardChannel` (kernel-only) is retained for potential kernel-level use but has no consumer task.

**Channel pool exhaustion**: The `fsSend*` helpers each create a reply channel via `chanCreate(1)`. These are never freed. To prevent pool exhaustion, add `chanFree(ch *Channel)` to `channel.go` that marks the channel's pool slot as unused (`ch.used = false`). Each `fsSend*` helper calls `chanFree(replyCh)` after receiving the response. The same applies to any per-syscall reply channels.

## 10. Summary of New/Modified Files

| File | Status | Description |
|---|---|---|
| `src/fs.go` | Modified | Increase limits, add fsDelete |
| `src/scheduler.go` | Modified | Task recycling, taskFree state, reuse slots |
| `src/userspace.go` | Modified | New 12-syscall dispatch, remove old user ELF |
| `src/process.go` | **New** | Process lifecycle, elfExec, processExit, arg passing |
| `src/vga.go` | **New** | VGA console with cursor and scrolling |
| `src/vm.go` | Modified | Add walkAndGetPaddr for page cleanup |
| `src/main.go` | Modified | Revised boot sequence, store user ELFs, spawn shell |
| `src/user_binaries.go` | **New (generated)** | Embedded user ELF byte arrays |
| `src/keyboard.go` | Modified | Adapt for sys_read line buffering |
| `src/interrupt.go` | Modified | Pass register frame for new syscall ABI |
| `scripts/embed_elfs.sh` | **New** | Script to convert ELF binaries to Go source |
