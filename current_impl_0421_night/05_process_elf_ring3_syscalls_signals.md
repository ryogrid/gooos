# Process Model, ELF Loading, Ring 3 Lifecycle, Syscalls, and Signals

## Process Object (`src/process.go`)

`Process` is the kernel PCB for each user process.

Major fields:

- identity/lifecycle: `pid`, `parent`, `exitCh`, `ExitCode`
- address-space: `pml4`, `UserPages[]`, `UserPaddrs[]`, `UserPageCnt`
- execution state: `EntryPoint`, `StackTop`, `LastCpuID`
- heap controls: `HeapBreak`, `HeapLimit`
- descriptor table: `fds[procMaxFDs]`
- signal state:
  - `SigAlrmHandler`
  - `UserPreemptPending`
  - `UserQuantumTicks`
  - `UserQuantumCounter`
  - `SigInProgress`
  - `SigSavedRSP`

Global mappings:

- `procByTask` (current task pointer to Process)
- `procByPID` (pid to Process)
- `foregroundProc` (keyboard owner)

All protected by `procLock` where required.

## Ring 3 Wrapper Lifecycle

`ring3Wrapper(proc)` (`src/process.go`) performs:

1. acquires pool-backed kernel stack via `ring3StackAcquire()`
2. binds pool slot to process (`setProcByPoolSlot`)
3. records process as current task (`setCurrentProc`)
4. registers runtime hook metadata (`registerRing3GWithStack`)
5. sets per-task TSS RSP0
6. ensures syscall gate DPL3 (`setGateDPL3(0x80)`)
7. installs process CR3 if `proc.pml4 != 0`
8. transfers to Ring 3 with `jumpToRing3(proc.EntryPoint, proc.StackTop)`

Wrapper does not return in normal execution; process exits via syscall path.

## ELF Loading Paths

### Boot shell path (`src/elf.go`, `elfLoad`)

- Parses ELF and PT_LOAD segments.
- Creates Process with stdio and foreground ownership.
- Maps PT_LOAD pages and copies segment bytes.
- Maps argument page at `argPageVaddr`.
- Maps user stack as 4 pages from `userStackBase`.
- Uses effective initial stack top: `userStackBase + 3*pageSize - 8`.

### Child spawn path (`src/process.go`, `elfSpawn`)

- Parses ELF from in-memory FS data.
- Allocates fresh Process and per-process PML4.
- Allocates PID under `procLock` and registers in `procByPID`.
- Inherits non-socket FDs from parent (socket FDs intentionally not inherited).
- Maps PT_LOAD, arg page, and 4-page user stack.
- Sets `EntryPoint` and `StackTop`.
- Spawns `go ring3Wrapper(child)`.

## Syscall Dispatch (`src/userspace.go`)

- Syscall vector: `0x80`.
- Dispatch entry: `syscallDispatch(frame *SyscallFrame)`.
- Syscall range currently covers `0..37` with explicit handlers for each supported operation.

Notable numbers:

- `34`: `sysWaitpid`
- `35`: `sysSigaction`
- `36`: `sysSigreturn`
- `37`: `sysListprocs`

## Signal Delivery Mechanics (`src/user_signal.go`)

### Registration

- `sysSigactionHandler` installs user SIGALRM handler (`signum=14`).

### Delivery trigger

- `maybeSignalUserPreempt(cpuIdx)` increments per-process quantum counter.
- On quantum expiration, sets `UserPreemptPending=1`.

### Frame rewrite delivery

- `maybeDeliverSignal(frame)` checks:
  - process exists
  - handler registered
  - pending preempt set
  - no nested signal (`SigInProgress==0`)
- Pushes 13-word `sigFrame` onto user stack via page-table-aware writes.
- Rewrites trap frame:
  - `frame.RIP = proc.SigAlrmHandler`
  - `frame.RSP = new sigFrame base`
- Stores `SigSavedRSP`, sets `SigInProgress=1`, clears `UserPreemptPending`.

### Return path

- `sysSigreturnHandler` reads frame from `SigSavedRSP`, validates `sigMagic`, restores register state into `SyscallFrame`, clears `SigInProgress`.
- Corrupted signal frame path calls `processExit(^uintptr(0))`.

## Ring 3 and Pointer-Safety Invariants

1. Kernel must never trust user stack pointer as signal frame location; `SigSavedRSP` is kernel-tracked source of truth.
2. User frame push/read helpers (`pushU64Through`, `readU64Through`, `writeU64Through`) walk per-process page tables per byte and fail on unmapped addresses.
3. Process and FD map mutations must occur under `procLock`/FD helpers to maintain cross-CPU coherence.
4. Socket FD inheritance is intentionally disabled to avoid shared channel teardown races.

## Current Edge Cases

- If userspace signal handler never invokes `sys_sigreturn`, process remains in signal-in-progress state and normal continuation is blocked.
- Process teardown must clear pool-slot and task mappings in correct order to avoid stale ISR lookups.
