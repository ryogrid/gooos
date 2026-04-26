# Traceability Matrix — Delta

**Scope:** extends `current_impl_0421_night/11_traceability_matrix.md`. Baseline rows remain correct; this file adds new files/symbols introduced in the `a384b1a..HEAD` range and updates the rows whose subsystem has a corresponding delta doc.

Delta rows below reference delta docs under `current_impl_2026_04_24/`. For any row not listed here, the baseline matrix is authoritative.

## 00_index.md (this set)

- files: this `current_impl_2026_04_24/` directory only (no source binding).
- symbols: n/a — index file.

## 01_boot_and_init_delta.md

- files: `src/main.go`, `src/smp.go`, `src/preempt_phase.go`, `src/kernel_thread.go`, `user/cmd/sh/main.go`, `user/gooos/proc.go`
- symbols:
  - boot additions: `bootActivatePostShellReady`, `bootPostShellReadyDone`, `kernelThreadInit`
  - virtual-wire restore: `restoreBSPVirtualWire`
  - AP scheduler-entered notify: `markAPSchedulerEntered`
  - Ring-3 shell-ready syscall wrapper: `gooos.ShellReady`

## 03_smp_preempt_phase_gating.md

- files: `src/preempt_phase.go`, `src/lapic_timer.go`, `src/ipi.go`, `src/smp.go`, `src/main.go`
- symbols:
  - phase state: `preemptPhaseBootInit`, `preemptPhaseSchedReady`, `preemptPhaseOperational`, `preemptPhase`
  - coordination: `preemptPhaseAdvance`, `maybeEnterOperational`, `markAPSchedulerEntered`, `apSchedEnteredCount`, `preemptPhaseLock`
  - fast-path read: `preemptPhaseIsOperational`
  - timer gate: `handleLAPICTimer`, `preemptStartupWarmupTicks`, `preemptProbeWarmupTicks`
  - IPI: `broadcastPreemptIPI`, `preemptTargetSnapshot`, `preemptTargetSnapshotN`, `lapicSendSelfIPI`

## 04_scheduler_and_kernel_thread.md

- files: `src/preempt_config.go`, `src/kernel_thread.go`, `src/afterticks.go`, `src/net.go`
- symbols:
  - configuration flags: `preemptEnabled`, `runPreemptProbe`, `runUserPreemptProbe`, `runSMPShellPreemptProbe`, `runSMPBasicProbe`, `runSMPProbeShellTest`, `runGoprobeTest`, `runSleeputestTest`, `runYieldtestTest`
  - kernel thread abstraction: `KernelThread`, `SavedContext`, `ThreadState`, `ThreadReady`, `ThreadRunning`, `ThreadBlocked`, `ThreadTerminated`
  - kernel thread API: `kernelThreadInit`, `kernelThreadSpawn`, `kernelThreadGetReady`, `kernelThreadPopReady`, `kernelThreadSwitch`, `kernelYield`, `kernelReadyQueues`, `currentKernelThread`
  - call sites: `timerDispatcher` (in `src/afterticks.go`), `netRxLoop` (in `src/net.go`)

## 05_syscalls_and_shell_ready.md

- files: `src/userspace.go`, `src/process.go`, `src/main.go`, `user/gooos/proc.go`
- symbols:
  - syscall #38: `sysShellReady`, `sysShellReadyHandler`, `bootActivatePostShellReady`, `ShellReady` (user)
  - processExit lock: `procLock` (preexisting var, new usage in `processExit`)
  - processWait foreground restore: `setForegroundProc`, `foregroundProc`
  - wait path: `processWait`, `sysWaitHandler`, `sysWaitpidHandler`

## 07_keyboard_irq_ring.md

- files: `src/keyboard_irq.go`, `src/keyboard.go`, `src/fd.go`, `src/pit.go`, `src/smp.go`, `src/net.go`
- symbols:
  - ring buffer: `gooosKbdRing`, `gooosKbdHead`, `gooosKbdTail`, `kbdRingSize`
  - producer: `keyboardIRQSend`, `handleKeyboard`, `processKeyboardScancode`
  - consumer: `keyboardIRQRecv`, `keyboardReadEventBlocking`, `markKeyboardDrainCPU`, `kbdPumpCpuSeen`
  - fallback: `pollKeyboardFallback`, `kbdIRQSeen`, `kbdPollSeen`
  - virtual-wire restore: `restoreBSPVirtualWire`

## 09_user_programs_sleep_vs_yield.md

- files: `user/cmd/sleeptest/main.go`, `user/cmd/yieldtest/main.go`, `user/cmd/smpprobe/main.go`, `user/cmd/goprobe/main.go`, `user/gooos/proc.go`, `src/userspace.go`, `user/Makefile`, `scripts/embed_elfs.sh`, `src/user_binaries.go`
- symbols:
  - user-side syscall constants: `sysShellReady` (38), `sysWaitpid`, `sysSleep`, `sysYield`
  - user wrappers: `gooos.Sleep`, `gooos.Yield`, `gooos.ShellReady`, `gooos.Spawn`, `gooos.Wait`, `gooos.Waitpid`, `gooos.GetCpuID`
  - kernel handlers (reference): `sysSleepHandler`, `sysYieldHandler`, `sysShellReadyHandler`

## 10_test_harnesses_delta.md

- files: `scripts/test_goprobe_shell.sh`, `scripts/test_goprobe_hmp.sh`, `scripts/test_sleeptest_shell.sh`, `scripts/test_smp_shell_smpprobe.sh`, `scripts/test_smp_multi_boot.sh`, `scripts/test_smp_stability_sample.sh`, `scripts/test_keyboard_reliability.sh`, `smp_preempt_problem/README.md`, plus baseline's `scripts/test_preempt_*.sh`, `scripts/test_smp_shell_preempt.sh`
- markers/patterns (new and carried-forward):
  - kernel preempt: `preempt_probe_marker=` *(baseline)*
  - user preempt: `userpreempt_marker=` *(baseline)*
  - shell preempt: `^marker [0-9]+ cpu=` *(baseline)*
  - smpprobe workers: `^worker-[0-9]+: cpuID=` and `smpprobe: done`
  - goprobe tests: `goprobe: ALL TESTS PASS`
  - sleeptest: `sleeptest: Sleep [123] OK` / `sleeptest: ALL SLEEPS PASS`
  - yieldtest: `yieldtest: ALL YIELDS PASS`
  - boot diagnostic markers (preserved): `MARKER: M2`, `M3`, `M8`, `M8P`, `M9`, `APIDSTAT cpu=…`, `PRESTAT cpu=…`

## Coverage Check

- All new files under `src/` since `a384b1a` (`src/preempt_phase.go`, `src/kernel_thread.go`, `src/keyboard_irq.go`) have a row above.
- All new user programs (`user/cmd/sleeptest`, `user/cmd/yieldtest`) have a row above.
- All new scripts under `scripts/` since `a384b1a` (7 added) have a row above.
- The `smp_preempt_problem/README.md` and `flaky_kbdproblem_fix/` corpora are referenced but not introduced to the code-symbol matrix (they are planning/handoff docs, not source).
- Baseline rows for 02, 06, 08 (CPU descriptors/traps, memory/VM/GC, network stack) have no corresponding delta doc — verified no material changes in those subsystems under `src/` during the documented range.
