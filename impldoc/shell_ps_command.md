# Shell `ps` Command and `sys_listprocs` (feature 2.5)

## Scope

Add a `ps` user-space command to gooos that enumerates running processes. Backed by a new kernel syscall `sys_listprocs` (#36) that snapshots the process table into a user-supplied buffer of `ProcInfo` structs.

Out of scope: process filtering flags (`-e`, `-u`, etc.), tree view (`-f`), per-user/-tty columns. The 2.5 `ps` is a 4-column minimalist that parallels its Unix ancestor's first version.

## Cross-links

- `preempt_shell_overview.md` — Design Decisions; syscall-number allocation table (36 for `sys_listprocs`).
- `shell_background_jobs.md` — 2.4 claims #34; 2.2 claims #35; 2.5 claims #36.
- `preempt_shell_milestones_and_verification.md` — Entry/Exit gates.
- `preempt_shell_readme_update_plan.md` — README Progress-table Shell row gains a `ps` mention.
- `impldoc/shell_io_fd_table.md` — existing syscall-allocation convention.
- `workflow_spinlock_smp.md` (memory) — lock-ordering-rank discipline.

## 1. Current State

- Process table: `procByPID map[uint32]*Process` at `src/process.go:79`, populated by `elfSpawn` (`:256-258`), reaped by `processWait` (`:361`). PID allocator: `nextPID` at `:84`.
- Lock: `procLock Spinlock` at `src/process.go:68` with documented **lock ordering rank 2**. Protects `procByTask`, `procByPID`, `nextPID`, `foregroundProc`.
- Ring3 pool: `maxRing3Procs = 32` at `src/ring3_pool.go:20` — hard cap on concurrent user processes.
- `Process` struct at `src/process.go:32-64` holds per-process state. No existing `LastCpuID`, no existing "ticks since spawn". 2.5 extends the struct.
- No process-enumeration code exists today — confirmed by agent exploration of the tree.
- Per-CPU block at `src/percpu.go:22-33` has `CurrentPoolIdx int32` at offset 40 (the ring3 pool slot currently active on this CPU, -1 for kernel-only).
- Syscall numbers 0..33 used; 34/35 claimed by 2.4/2.2 in this batch; **36 is the first free for 2.5**.

## 2. Design

### 2.1 ProcInfo struct — exact layout

Fixed 64-byte struct, padded to cache-line boundary to match the style of `PerCPU` (`src/percpu.go:22-33`). Field order chosen so natural alignment holds without hidden compiler padding:

| Offset | Size | Field        | Meaning |
| ------ | ---- | ------------ | --- |
| 0      | 4    | `PID`        | Process ID. |
| 4      | 4    | `PPID`       | Parent PID; 0 for orphaned (parent is the dead shell root). |
| 8      | 1    | `State`      | Enum: `psRunning`/`psSleeping`/`psExited`/`psUnknown` (see §2.2). |
| 9      | 3    | `_pad1[3]`   | Explicit padding; zero on snapshot. |
| 12     | 4    | `LastCpuID`  | Last observed kernel CPU (0..maxCPUs-1). Populated per §2.3. |
| 16     | 8    | `Ticks`      | Elapsed `pitTicks` (100 Hz) since process start. |
| 24     | 8    | `StartTick`  | `pitTicks` at `elfSpawn` time. |
| 32     | 32   | `Name[32]`   | ELF name (e.g. `"hello.elf"`); NUL-terminated; excess truncated. |

Total: 64 bytes. `unsafe.Sizeof(ProcInfo{}) == 64` is a build-time invariant the reviewer must verify.

Go declaration (mirror in kernel `src/ps.go` and user `user/gooos/ps.go`):

```go
const (
    psRunning  uint8 = 0
    psSleeping uint8 = 1
    psExited   uint8 = 2
    psUnknown  uint8 = 3
)

type ProcInfo struct {
    PID       uint32
    PPID      uint32
    State     uint8
    _pad1     [3]byte
    LastCpuID uint32
    Ticks     uint64
    StartTick uint64
    Name      [32]byte
}
```

The `_pad1` / explicit zero-init in the snapshot code prevents uninitialized stack bytes leaking from kernel to user space — a class of info-leak that small kernels are routinely dinged on.

### 2.2 State enum

Decision: 2.5 distinguishes **Running / Sleeping / Exited / Unknown** only — no Zombie, because gooos' current model does not have zombies (a child that calls `sys_exit` sends on `exitCh` and is freed when the parent reaps; the window between exit and reap is short enough that a non-atomic snapshot may miss it entirely).

- `psRunning` — process has a live ring3Wrapper goroutine that is NOT waiting on a channel. Detected via `isCurrentlyRunning(proc)` helper (§2.4).
- `psSleeping` — process is parked on a channel receive (syscall handler, `Wait`, `ReadLine`, etc.). Default for any process not in `psRunning`.
- `psExited` — child has sent on `exitCh` but parent has not yet called `sys_wait`/`sys_waitpid`. Rare; included for completeness.
- `psUnknown` — snapshot saw the slot but could not classify; reserved.

**Pragmatic detection.** Distinguishing Running from Sleeping cheaply is hard without adding per-goroutine state tracking. Acceptable approximation for 2.5: any process whose ring3Wrapper goroutine's `task.Task` equals a per-CPU `currentTask` at snapshot time is `psRunning`. Others are `psSleeping`. The TinyGo scheduler exposes this via the per-CPU `cpuTasks[cpuID]` array introduced in M3; kernel already has the access via `runtime.gooosCurrentTaskOnCPU(i)` (linkname TBD in 2.5 commit 3). If the linkname cannot be added cleanly, fall back to "all live procs = Running" and document the loss of precision.

### 2.3 Last-CPU tracking

Add `LastCpuID uint32` field to `Process` struct (tail insertion so existing field offsets are unchanged):

```go
type Process struct {
    // ... existing fields (ExitCode, ArgString, ..., pid) ...

    // LastCpuID is updated on every gooosOnResume for this process's
    // ring3Wrapper goroutine (feature 2.5). Not protected by a lock
    // because reads are single-word and writers are always the CPU
    // currently resuming the wrapper — the value is naturally atomic
    // on x86-64 aligned u32 stores.
    LastCpuID uint32
}
```

Updated in the `gooosOnResume` hook in the patched runtime (`~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go`). The hook is already called per-resume (see `task_stack_amd64.go:59-62`). Addition:

```go
// existing gooosOnResume body (TSS.RSP0 update) ...
if p := gooosCurrentProc(); p != nil {
    p.LastCpuID = gooosCpuID()
}
```

`gooosCurrentProc()` is a new linkname that walks `procByTask` keyed by `task.Current()`. Candidate for inlining.

### 2.4 `sys_listprocs` — kernel ABI

Signature: `sys_listprocs(buf *ProcInfo, max uint32) → int32`.

Register ABI:
- `RAX = 36`
- `RDI = buf` (user vaddr; must be writable; NULL invalid)
- `RSI = max` (max number of entries to fill; capped at `maxRing3Procs = 32`)
- Returns in `RAX`:
  - `n >= 0` — number of `ProcInfo` entries written. Bounded by `min(max, count_of_live_procs)`.
  - `-fdErrBad` — NULL buf or max == 0.

Handler pseudocode (`src/userspace.go` tail; or new `src/ps.go`):

```go
// --- Syscall 36: sys_listprocs ---
// RDI = buf vaddr, RSI = max entries. Returns number of entries
// filled. Buffer fields: see impldoc/shell_ps_command.md §2.1.
func sysListprocsHandler(frame *SyscallFrame) {
    caller := currentProc()
    if caller == nil {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    bufVaddr := uintptr(frame.RDI)
    max := uint32(frame.RSI)
    if bufVaddr == 0 || max == 0 {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    if max > maxRing3Procs {
        max = maxRing3Procs
    }

    // Snapshot under procLock, into a kernel-side staging buffer.
    // We release the lock BEFORE writing through the user PML4 so a
    // user-page fault doesn't deadlock with a child exiting.
    var staging [maxRing3Procs]ProcInfo
    n := uint32(0)
    now := pitTicks
    fl := procLock.Acquire()
    for _, proc := range procByPID {
        if n >= max { break }
        fillProcInfo(&staging[n], proc, now) // 0-out then populate
        n++
    }
    procLock.Release(fl)

    // Write through user PML4 (same mechanism elfSpawn uses at
    // src/process.go:313, src/process.go:322). One 64-byte entry
    // at a time; bounded by the 32-entry cap, so at most 2 KiB.
    for i := uint32(0); i < n; i++ {
        writeStructThrough(caller.pml4, bufVaddr + uintptr(i)*64, &staging[i])
    }
    frame.RAX = uintptr(n)
}
```

`fillProcInfo` zero-inits the struct first (so `_pad1` is clean), then populates.

### 2.5 Lock-ordering and snapshot semantics

**Lock ordering.** `procLock` is rank 2. `sys_listprocs` takes it exclusively during the staging-snapshot phase (§2.4) and releases before the user-PML4 writes. No other lock is acquired inside the critical section.

**Snapshot atomicity.** The snapshot is atomic with respect to `procByPID` membership: a child that exits between snapshot and user-buffer write will be in the snapshot as `psRunning`/`psSleeping`, not `psExited`. That is acceptable (PS output is advisory).

**Race with `processExit` / `processWait`.** Both these functions call `delete(procByPID, pid)` under `procLock`. `sys_listprocs` already holds `procLock` during enumeration, so a concurrent exit waits or completes before we read. Between staging and user-buffer write, an exit can happen — we may report a freshly-dead PID, but we never report a stale pointer (the staging copy is by-value).

**Race with new spawns.** `elfSpawn` takes `procLock` to assign PID + insert into the map (`src/process.go:255-258`). Same ordering; fine.

**Read-side access to `LastCpuID`** (§2.3) is unlocked because it is an aligned u32. This is the same pattern as `PerCPU.WantReschedule` (`src/percpu.go:28`). Note: no `atomic.Store` needed on x86-64 for naturally-aligned u32.

## 3. SDK wrapper

`user/gooos/ps.go` (new file):

```go
package gooos

import "unsafe"

// ProcInfo mirrors the kernel-side layout at
// impldoc/shell_ps_command.md §2.1. MUST be 64 bytes.
type ProcInfo struct {
    PID       uint32
    PPID      uint32
    State     uint8
    _pad1     [3]byte
    LastCpuID uint32
    Ticks     uint64
    StartTick uint64
    Name      [32]byte
}

// Listprocs fills buf with ProcInfo entries for every live process.
// Returns (n, 0) on success where n is the count filled, or
// (-1, errno) on error. buf must have len() >= 1.
func Listprocs(buf []ProcInfo) (int, int) {
    if len(buf) == 0 {
        return -1, -1
    }
    r := syscall2(sysListprocs,
        uintptr(unsafe.Pointer(&buf[0])),
        uintptr(len(buf)))
    if int64(r) < 0 {
        return -1, int(int64(r))
    }
    return int(r), 0
}
```

New syscall-number const `sysListprocs = 36` in `user/gooos/syscall.go`.

Helper methods on `ProcInfo` for readable State:

```go
func (p *ProcInfo) StateString() string {
    switch p.State {
    case 0: return "R"
    case 1: return "S"
    case 2: return "Z"
    case 3: return "?"
    default: return "?"
    }
}
```

## 4. Frontend: `user/cmd/ps/main.go`

```go
package main

import (
    "strconv"
    "github.com/ryogrid/gooos/user/gooos"
)

const maxRows = 32

func main() {
    var buf [maxRows]gooos.ProcInfo
    n, errno := gooos.Listprocs(buf[:])
    if n < 0 {
        gooos.Println("ps: listprocs failed, errno=" + strconv.Itoa(errno))
        gooos.Exit(1)
        return
    }
    gooos.Println("  PID  PPID  S  CPU  TICKS  NAME")
    for i := 0; i < n; i++ {
        p := &buf[i]
        line := pad(strconv.Itoa(int(p.PID)), 5) +
            pad(strconv.Itoa(int(p.PPID)), 6) +
            "  " + p.StateString() +
            pad(strconv.Itoa(int(p.LastCpuID)), 5) +
            pad(strconv.FormatUint(p.Ticks, 10), 7) +
            "  " + nameString(p.Name[:])
        gooos.Println(line)
    }
}

func pad(s string, width int) string { /* right-align */ ... }
func nameString(b []byte) string     { /* NUL-terminated */ ... }
```

Output example:

```
  PID  PPID  S  CPU  TICKS  NAME
    0     0  S    0    950  sh.elf
    2     0  R    2     12  ps.elf
    3     0  R    1    180  cpuhog.elf
```

Register in `user/Makefile:21 CMDS`: append `ps`.

`scripts/embed_elfs.sh` automatically picks up `user/build/ps.elf` on next build.

## 5. Commit-per-edit Plan

1. `feat(proc): add Process.LastCpuID field + update in gooosOnResume` — `src/process.go` struct extension; patched runtime update. Build-only; no syscall yet.
2. `feat(syscall): sys_listprocs #36 handler + dispatch` — `src/userspace.go` number const and handler; `user/gooos/syscall.go` number const; `user/gooos/ps.go` (new) with `ProcInfo` + `Listprocs` wrapper. Kernel side copies through PML4.
3. `feat(user): ps command` — `user/cmd/ps/main.go` (new). Register in `user/Makefile:21 CMDS`.
4. `test(user): harness for ps` — `scripts/test_ps.sh` boots shell, runs `ps`, verifies at least the shell itself shows up in the output.

## 6. Per-File Edits

Kernel (`/home/ryo/work/gooos/src/`):
- `process.go:32-64 Process` — append `LastCpuID uint32` field.
- `ps.go` (NEW) — `ProcInfo` struct, `fillProcInfo`, `sysListprocsHandler`, `writeStructThrough` helper (or reuse an existing through-PML4 writer from `src/process.go:313`).
- `userspace.go:47-85` — add `sysListprocs = 36`.
- `userspace.go:95 syscallDispatch` — add `case sysListprocs: sysListprocsHandler(frame)`.

Patched TinyGo (`~/.local/tinygo0.40.1/src/runtime/`):
- `runtime_gooos.go` — update `gooosOnResume` to populate `p.LastCpuID = gooosCpuID()`. Requires a new linkname `gooosCurrentProc`.
- `scripts/tinygo_runtime.patch` regen.
- `scripts/patch_tinygo_runtime.sh` — add post-condition grep: `grep -q 'gooosCurrentProc' runtime_gooos.go`.

User SDK (`/home/ryo/work/gooos/user/gooos/`):
- `syscall.go:47-54` — add `sysListprocs = 36`.
- `ps.go` (NEW) — `ProcInfo` struct, `Listprocs` wrapper, `StateString` helper.

Shell (`/home/ryo/work/gooos/user/cmd/`):
- `ps/main.go` (NEW).
- `Makefile:21` — append `ps` to `CMDS`.

Scripts:
- `test_ps.sh` (NEW).

## 7. Entry Criteria

- `smp-take4` HEAD or later.
- `make build && make lint && make verify-globals` clean.
- Full regression matrix green under `-smp 1` and `-smp 4`.
- 2.1/2.2/2.4 landing not required — 2.5 is independent. If 2.4 has landed, `ps &` works (nice-to-have, not required for 2.5 acceptance).

## 8. Exit Criteria

- `scripts/test_ps.sh` PASS under `-smp 1` and `-smp 4`.
- Interactive acceptance:
  - `ps` at the shell prints a header + ≥ 1 row (the shell itself, PID 0).
  - After spawning `hello &`, a subsequent `ps` shows an extra row for `hello.elf` until it exits.
  - The `S` column shows `R` for currently-running procs.
- No kernel panic, no triple-fault under any harness.
- Full regression matrix remains PASS.
- `unsafe.Sizeof(ProcInfo{}) == 64` at build time (static assertion or reviewer check).

## 9. Rollback

- Primary: `git revert` commit 3 (frontend ELF). `ps` disappears from CMDS; shell: `"sh: command not found: ps"`. The `sys_listprocs` syscall still exists but has no caller.
- Secondary: revert commits 2 and 1 in reverse.

## 10. Risks

- **Struct-layout drift** between kernel and user definitions. If either side edits `ProcInfo` without the other, user reads garbage. Mitigation: single-source-of-truth section §2.1 in this doc; both kernel and user copies carry a comment `// MUST match impldoc/shell_ps_command.md §2.1`. Build-time `unsafe.Sizeof == 64` check on both sides.
- **Stale `LastCpuID`**. A long-sleeping process reports a CPU it hasn't actually run on for minutes. Documented as accepted; output column is advisory.
- **`Name[32]` truncation**. ELF names > 31 characters get truncated (31 bytes + NUL). Current ELFs are short (`smpprobe.elf` = 13 chars, longest). Cap is documented, not enforced at load time.
- **Info leak via `_pad1[3]`**. Uninit stack bytes could leak from kernel to user. Mitigation: `fillProcInfo` zeros the struct before populating. Reviewer check: confirm via grep `staging\[n\]\s*=\s*ProcInfo\{\}` or equivalent.
- **Lock held across map iteration**. `for _, proc := range procByPID` iterates under `procLock`. The map has ≤ 32 entries (`maxRing3Procs`), iteration bounded by hash-map walk overhead — well under 10 µs. Not a concern for latency; flagged here because reviewer briefs usually ask.
- **`gooosCurrentProc` linkname addition** is a new patch to upstream TinyGo. If the linkname conflicts with a future TinyGo upgrade, 2.5's patch must rebase. Already the accepted cost of the patch-set model.

## 11. Deliverables

- 4 commits per §5.
- New files: `src/ps.go`, `user/gooos/ps.go`, `user/cmd/ps/main.go`, `scripts/test_ps.sh`.
- Modified: `src/process.go`, `src/userspace.go`, `user/gooos/syscall.go`, `user/Makefile`, patched TinyGo `runtime_gooos.go`, patch artifacts.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
