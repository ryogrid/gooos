# User Programs — Sleep vs. Yield Status and New Diagnostics

**Scope:** extends `current_impl_0421_night/09_userland_abi_and_embedded_elves.md`. Baseline's User Build Target, Syscall ABI Contract, SDK Packages, Embed Pipeline, and Ring-3 Runtime Model are all unchanged. This file adds (a) syscall `#38 sys_shell_ready` to the syscall-number space, (b) new diagnostic programs `sleeptest.elf` / `yieldtest.elf`, and (c) the empirical finding that `sys_sleep` hangs at Ring 3 under SMP while `sys_yield` works, plus the `Yield`-loop workaround that `smpprobe` and `goprobe` now use.

## Summary of Changes Since `a384b1a`

1. Syscall number space extended by one: **`#38 sys_shell_ready`**. Kernel `src/userspace.go:92`; user wrapper `user/gooos/proc.go:134`. Commit `7826548`.
2. Two new diagnostic user programs:
   - `user/cmd/sleeptest/main.go` — three back-to-back `gooos.Sleep(10)` calls with before/after prints. Commit `af9cb8f`.
   - `user/cmd/yieldtest/main.go` — three back-to-back `gooos.Yield()` calls with before/after prints. Commit `4a0337c`.
3. `smpprobe` and `goprobe` now use `Yield`-loop blocking instead of `time.Sleep`. Commits `e6b79d3` (smpprobe), `f4bf75e` (goprobe).
4. Auto-run flags in `src/preempt_config.go` added for all four above programs (`runSleeputestTest`, `runYieldtestTest`, `runSMPProbeShellTest`, `runGoprobeTest`). See `04_scheduler_and_kernel_thread.md` for the full matrix.

## Current Design

### Updated Syscall Number Space

Extend baseline §Syscall Number Space with one row:

- `22..27`: UDP/socket + net config *(unchanged)*
- `28..33`: TCP sockets *(unchanged)*
- `34`: `sys_waitpid` *(unchanged)*
- `35`: `sys_sigaction` *(unchanged)*
- `36`: `sys_sigreturn` *(unchanged)*
- `37`: `sys_listprocs` *(unchanged)*
- **`38`: `sys_shell_ready` — deterministic startup gate; caller: shell (`user/cmd/sh/main.go:26`/`:29`); kernel effect: advances preempt phase to `SchedReady` (see `03_smp_preempt_phase_gating.md`).**

### Sleep vs. Yield status at Ring 3

Empirical finding from diagnostic runs (captured across commits `af9cb8f`, `4a0337c`, `e6b79d3`, `f4bf75e`):

- **`gooos.Sleep(ms)`** — wrapper in `user/gooos/proc.go:53` calls `syscall1(sysSleep, ticks)`. Kernel-side `sysSleepHandler` (`src/userspace.go:451`) parks on `<-afterTicks(ticks)`. Under `-smp 4` from Ring 3, the calling user process frequently does **not resume** after the channel fires — it hangs in what behaves like a scheduler/runtime defect (the process never returns from the syscall). Diagnosed by `sleeptest.elf` which prints "Sleep 1 OK" before looping on `Sleep 2` that never prints. Still unresolved at HEAD (see Open Questions).
- **`gooos.Yield()`** — wrapper in `user/gooos/proc.go:49` calls `syscall0(sysYield)`. Kernel-side `sysYieldHandler` (`src/userspace.go:434`) is a one-liner: `runtime.Gosched(); frame.RAX = 0`. Works reliably at Ring 3 under SMP. Diagnosed by `yieldtest.elf`.
- **Workaround adopted in `smpprobe` and `goprobe`**: replace every `time.Sleep`/`Sleep` with a tight `for j := 0; j < N; j++ { gooos.Yield() }` loop, tuned to the former sleep duration (e.g., 100 yields replaces `Sleep(10)` at the SMP sample site).

The earlier attempted kernel-side fix (`332a7a1 Fix sys_sleep hang on multi-CPU worker processes`, which synced TSS.RSP0 / CR3 after the `afterTicks` return) was **reverted** by `a3cc9c8` — it masked symptoms without fixing root cause and hid secondary bugs. **Do not** describe that fix as current reality.

### New diagnostic programs

#### `sleeptest.elf` (`user/cmd/sleeptest/main.go`)

Single-file, ~28 LOC. Minimal reproducer for the Sleep hang:

```go
func main() {
    gooos.Println("sleeptest: begin")
    gooos.Println("sleeptest: calling Sleep once...")
    gooos.Sleep(10); gooos.Println("sleeptest: Sleep 1 OK")
    gooos.Sleep(10); gooos.Println("sleeptest: Sleep 2 OK")
    gooos.Sleep(10); gooos.Println("sleeptest: Sleep 3 OK")
    gooos.Println("sleeptest: ALL SLEEPS PASS")
}
```

Under `-smp 4` via `scripts/test_sleeptest_shell.sh`, typical observed behavior: the first `Sleep` returns, the second or third does not. PASS banner rarely prints.

#### `yieldtest.elf` (`user/cmd/yieldtest/main.go`)

Same shape as `sleeptest` but substitutes `gooos.Yield()` for `gooos.Sleep(10)`. All three prints and the PASS banner always appear — confirms that the kernel-side dispatch path for `sys_yield` is healthy at Ring 3 under SMP; the Sleep hang is specific to `sys_sleep`'s `afterTicks` wait, not the syscall ABI.

#### Updated `smpprobe.elf` (`user/cmd/smpprobe/main.go`)

- Constants: `numWorkers = 4`, `iterationsPerWorker = 3`, `yieldsPerIteration = 100`.
- Worker path (line 39–52): 3 iterations each print `worker-N: cpuID=X` and then run 100 `gooos.Yield()` calls to give the work-stealing scheduler a chance to migrate the worker.
- Parent path (line 55–74): spawns 4 workers, waits for each.
- Expected output documented in the file's header comment: workers distributed across cores (`cpuID=0,1,2,3`) — but see `smp_preempt_problem/README.md §Confirmed Current Status`: this is **not reliably reproducing multi-core distribution today**, workers sometimes all report `cpuID=0`.

#### Updated `goprobe.elf` (`user/cmd/goprobe/main.go`)

Four sub-tests: go+chan round-trip, two-channel select, Yield-driven goroutine interleaving (3 iterations × 100 yields each), Yield-driven goroutine cycling (2 goroutines × 100 yield-increments). The former `time.Sleep(1ms)` pre-select warmup (commit `61b89d0`) is now a `for j := 0; j < 10; j++ { gooos.Yield() }` loop at `user/cmd/goprobe/main.go:39–41`. ALL TESTS PASS banner verified under `scripts/test_goprobe_shell.sh`.

## Current Implementation Details

- **Syscall wrapper additions in `user/gooos/proc.go`:**
  - `const sysShellReady = 38` at line 107.
  - `func ShellReady()` at line 134.
- **Kernel dispatch additions in `src/userspace.go`:**
  - `sysShellReady = 38` in the const block at `:92`.
  - `case sysShellReady: sysShellReadyHandler(frame)` at `:181–182`.
  - `func sysShellReadyHandler(frame *SyscallFrame)` at `:617`.
- **Kernel `sys_sleep` handler (unchanged — for reference):** `src/userspace.go:451–457` parks on `<-afterTicks(ticks)`. The kernel side is healthy — the bug surfaces only when the parked goroutine is a ring3Wrapper for a user process.
- **Embedded binary list:** `src/user_binaries.go` now contains ELF blobs for `sleeptest.elf`, `yieldtest.elf` (in addition to the baseline roster). Embed pipeline in `scripts/embed_elfs.sh` is unchanged.
- **`user/Makefile`:** builds `sleeptest` and `yieldtest` as first-class targets alongside the other `user/cmd/*` programs.

## Diff-from-Baseline Notes

- Baseline §Syscall Number Space stopped at `#37`. Append `#38`.
- Baseline §ABI Invariants bullet 1 ("User wrappers and kernel syscall numbers must remain synchronized") — still true and now exercised for `#38`.
- Baseline §Known Compatibility Risks bullet 2 ("Signal handler non-compliance...") — unchanged.
- The "user program suite" evolves: baseline existed with `sh`, `hello`, `ls`, `cat`, `wc`, `fdprobe`, `goprobe`, `gochan`, `tinyc`, `edit`, `smpprobe`, `udpecho`, `dhcp`, `tcpecho`, `tcpcli`, `ps`, `cpuhog`, `markerprint`, `userpreempt` (19 programs). Current tree adds `sleeptest` and `yieldtest` — **21 total**. Authoritative list: `CMDS :=` in `user/Makefile:21`.

## Open Questions / Known Gaps

- **Partially closed (F1)**: the dominant root cause of the
  Ring-3 `sys_sleep` hang was Phase 4.3's
  `kernelThreadSpawn(0, netRxLoop)` call — `kernelThreadSwitch`
  direct-invocation of that infinite loop stranded
  `timerDispatcher` and stopped every `afterTicks` deadline.
  Removed in the 2026-04-24 cycle (see
  `04_scheduler_and_kernel_thread.md` correction). Pass rate of
  `scripts/test_sleeptest_shell.sh` under `-smp 4` went from 0%
  to ~20% immediately, and further improved with B2 (AP LAPIC
  timer enable). Typical behavior now: Sleep 1 and Sleep 2
  complete reliably, Sleep 3 intermittently hangs.
- **Deferred (F1-follow-up)**: the residual Sleep-3 hang is
  suspected to live in the channel-wakeup cross-CPU path —
  `timerDispatcher` does `ch <- struct{}{}` on a buffered
  channel; `scheduleTask(waiter)` pushes the waiter to the
  dispatcher's CPU runqueue + `schedulerWake` IPIs all APs;
  under some timing the waiter is never re-scheduled. A proper
  fix likely requires auditing TinyGo's channel-wakeup primitive
  or replacing `afterTicks` with a direct per-CPU timer
  mechanism (needs Phase 4.4).
- **Deferred (F2)**: "is Yield-loop a sustainable workaround?"
  is moot once the Sleep-3 hang closes. `gooos.Sleep` remains
  usable with the known flakiness; programs that need strict
  wall-clock semantics should not rely on it yet.
- `smp_preempt_problem/README.md` remains the canonical open
  handoff for the broader post-shell SMP runtime boundary
  problem.
