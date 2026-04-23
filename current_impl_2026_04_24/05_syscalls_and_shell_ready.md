# Syscalls, Shell-Ready Event, and `processExit` Serialization — Delta

**Scope:** extends `current_impl_0421_night/05_process_elf_ring3_syscalls_signals.md`. Baseline's §Process Object, §Ring 3 Wrapper Lifecycle, §ELF Loading Paths, §Signal Delivery Mechanics, and §Ring 3/Pointer-Safety Invariants are all still current. This file adds three deltas: syscall `#38 sys_shell_ready`, `processExit` lock serialization, and the foreground-restore rule in `processWait`.

## Summary of Changes Since `a384b1a`

1. **Syscall #38 `sys_shell_ready`** — new deterministic startup-gate syscall. Commit `7826548`. Kernel handler `sysShellReadyHandler` in `src/userspace.go:617`; Ring-3 wrapper `ShellReady()` in `user/gooos/proc.go:134`.
2. **`processExit` lock serialization** — `processExit` now acquires `procLock` around `freePage` loop to prevent concurrent cleanup from racing the page allocator. Commit `9cbe862`.
3. **`processWait` foreground restore** — `setForegroundProc(prevForeground)` happens immediately after `<-proc.exitCh`, **before** the PID map teardown under `procLock`. Commits `873410c` `f758f9b`.

## Current Design

### Syscall #38: `sys_shell_ready`

Semantic: "The shell has reached the interactive-ready state — preempt fanout may now transition to operational."

Kernel-side:

- `src/userspace.go:92` — `sysShellReady = 38` added to the const block between `sysListprocs = 37` and the closing paren. Extends the baseline's 0..37 syscall range to 0..38.
- `src/userspace.go:181–182` — `case sysShellReady: sysShellReadyHandler(frame)` added to the `syscallDispatch` switch.
- `src/userspace.go:614–620` — handler body:
  ```go
  func sysShellReadyHandler(frame *SyscallFrame) {
      bootActivatePostShellReady()
      frame.RAX = 0
  }
  ```
  Delegates the heavy lifting to `bootActivatePostShellReady()` (`src/main.go:604`, see `01_boot_and_init_delta.md`). Returns 0 unconditionally. No arguments, no error path.

Ring-3 side:

- `user/gooos/proc.go:107` — `const sysShellReady = 38`.
- `user/gooos/proc.go:134` — `func ShellReady() { syscall0(sysShellReady) }`.
- `user/cmd/sh/main.go:26` and `:29` — the shell calls `gooos.ShellReady()` in its very first actions after the banner, on both the `--autorun` and interactive paths.

Dispatch-table completeness: the README lists 39 syscalls (0..38); `src/userspace.go:syscallDispatch` has 39 explicit `case` entries (baseline 38 + the new `#38`). The `default` branch returns `-1` as unsigned `0xFFFFFFFFFFFFFFFF` — unchanged.

### `processExit` page-free serialization (`src/process.go:490–578`)

The baseline-era flow was:

1. Take the current process.
2. Loop over `proc.UserPaddrs[0..UserPageCnt]` calling `freePage(...)`.
3. Set `Exited = 1`, push `ExitCode` on `proc.exitCh`.
4. Swap `CR3` back to `bootPML4`, free per-process PML4.
5. Decrement per-CPU `InterruptDepth` / `SyscallDepth` and `taskPause()`.

The current flow wraps steps 2–3 in `procLock`:

```go
flags := procLock.Acquire()
serialPrintln("MARKER: M2 processExit pre-freePage")
// (optional runSMPShellPreemptProbe diagnostic dump)
for i := 0; i < proc.UserPageCnt; i++ { freePage(proc.UserPaddrs[i]) }
proc.UserPageCnt = 0
proc.ExitCode = exitCode
proc.Exited = 1
serialPrintln("MARKER: M3 processExit post-freePage")
procLock.Release(flags)

if proc.parent != nil {
    proc.exitCh <- exitCode
}
```

Rationale: `freePage` walks the page-allocator free stack under `pageAllocLock` (rank 1 — see baseline 03 lock table). When multiple `smpprobe` workers exit concurrently on different CPUs, `pageAllocLock` became a hot contention point. Serializing page-free across all exits under `procLock` (rank 2) removes that contention with a single-writer discipline at no correctness cost — `procLock` is already the intended serializer for `procByTask` / `procByPID` / `nextPID` mutations, which this function also needs.

**Lock-order note:** `procLock.Acquire` disables interrupts (it's a `Spinlock.Acquire` that returns the pre-acquire flags). The `serialPrintln` calls inside the critical section are therefore ISR-safe. The `exitCh` channel send happens **after** `procLock.Release` — critical, because a buffered send may block and cannot be done under a disabled-interrupts spinlock.

### `processWait` foreground restore (`src/process.go:441–443`)

```go
setForegroundProc(proc)
exitCode := <-proc.exitCh
setForegroundProc(prevForeground)
```

This sits inside `processWait(child)`. The restore happens **synchronously** on return from the blocking channel receive, before the PID-map teardown. This rule is repeated explicitly in the comment at `src/userspace.go:784` on `sysWaitpidHandler`: the non-blocking `waitpid` path does **not** transfer foreground, because it's used by backgrounded shell jobs that must not steal the keyboard.

## Current Implementation Details

- **`procLock` type:** `Spinlock` (`src/process.go:114`) — the same lock baseline references under "protected by `procLock`" for `procByTask`, `procByPID`, `nextPID`, and `foregroundProc`.
- **Lock-disabling vs. interrupt-depth:** `processExit` intentionally decrements per-CPU `InterruptDepth` / `SyscallDepth` **after** `taskPause`-preparatory work, because the goroutine was entered from an `int 0x80` ISR and the epilogue will never run. This logic is unchanged from baseline; only the `procLock`-guarded section around `freePage` is new.
- **Diagnostic dumps inside `processExit`:** `runSMPShellPreemptProbe` gates the `APIDSTAT cpu=N apicid=N` loop and `dumpPreemptCounters()` call inside the critical section (`src/process.go:504–510`). These run with interrupts disabled under `procLock` — terse `serialPrintln` calls only.
- **PID map teardown in `processWait`:** the `procByPID`/`clearProcName`/`processStartTick` delete block at `src/process.go:458–464` still takes `procLock.Acquire/Release` separately from the exit path. This is a second-phase cleanup done by the *parent* after reaping the child.

## Diff-from-Baseline Notes

- Baseline §Syscall Dispatch already lists `#38 sysShellReady` in its Notable numbers (last row at `current_impl_0421_night/05_process_elf_ring3_syscalls_signals.md:78`) — this delta doc adds the *implementation* details (handler body, Ring-3 call chain, tie-in to preempt-phase state machine) that baseline's terse listing doesn't cover. Baseline itself was amended by commit `7826548` to land the `#38` row in the same change that introduced the syscall, which is why both baseline and HEAD are numerically consistent at 0..38.
- Baseline §Process Object claim "All protected by `procLock` where required" is still correct; the *new* work under `procLock` is the `freePage` loop in `processExit`, previously unlocked.
- Baseline §Current Edge Cases bullet on "Process teardown must clear pool-slot and task mappings in correct order" is still the exact rule; the new `procLock` coverage makes the rule easier to follow (single critical section for the entire free-pages + exit-flag-set block).
- Baseline §Signal Delivery Mechanics is unchanged. No new signal-path syscalls in this range.

## Open Questions / Known Gaps

- `procLock` is rank 2. Holding it across `freePage` (which acquires `pageAllocLock` at rank 1) is fine (higher-rank holder acquires lower-rank lock = OK). But a *future* caller that holds `pageAllocLock` and tries to take `procLock` would invert the rank order. Nothing in the current tree does this; the invariant is asserted only informally in comments.
- `sys_shell_ready` has no authentication: any Ring-3 process can call it. Current shell is the *only* Ring-3 binary that does (`user/cmd/sh/main.go:26`/`:29`), but a rogue program could also call it. `bootActivatePostShellReady` is idempotent (guarded by `bootPostShellReadyDone`), so multiple calls are harmless — but an attacker could call it *before* the shell does if the ELF load order ever changes. Currently the shell is always launched first via `elfLoad("sh.elf")` in `setupUserspace`, so this is OK.
- `processExit`'s diagnostic dump inside the critical section adds serial-print latency to every exit when `runSMPShellPreemptProbe` is on. Off by default; no production impact.
