# TODO_NOGOTIN — Route C implementation tracker

This file tracks the implementation of the no-goroutine kernel
(Route C) per `no_goroutine_kernel_design/` and `hoge.md`. One
checkbox per commit; each commit lands with its matching check
mark in the same commit.

Branch: `smp-no-goroutine-in-kernel`
Starting SHA: `7f81f12` (design doc set).

## Baseline (pre-Route-C, HEAD = 7f81f12)

- [x] B0 — `make build` clean
- [x] B1 — `make lint` clean (runs as first phase of `make build`)
- [x] B2 — `make verify-globals` clean (runs as last phase of `make build`)
- [x] B3 — `scripts/test_smp_basic.sh` — PASS (ap_kernel_cpus=3 ring3_ap_hits=0)
- [ ] B4 — `scripts/test_net.sh` — deferred; the test is long and not needed to gate M0; rerun at M2 or earlier if regression suspected
- [ ] B5 — `scripts/test_sleeptest_postrevert.sh ITERATIONS=20` — deferred; the F1 baseline (~50 % PASS S2) is already recorded in commit `d2008c8`'s log and `tmp/sleep_s2_postrevert_summary.json` if present; rerun before M4 to confirm regression-free M2/M3 landings

## M0 — Context-switch stub in isolation

- [x] M0.1 — New files: `src/kthread.go`, `src/kthread_sched.go`, `src/kthread_pool.go`, `src/kthread_lifecycle.go`, `src/kthread_switch.S`, `src/kthread_smoke.go`, `scripts/test_kthread_smoke.sh`; Makefile wires `kthread_switch.S`; Phase 4.3 `src/kernel_thread.go` + `kernelYield`/`kernelThreadInit` call sites deleted (superseded); passes `checkKernelThreadOffset()` at boot; gate `scripts/test_kthread_smoke.sh` **PASS** (A=5 B=5 ok=1)

## M1 — Demo probes on kernel threads

- [ ] M1.1 — `kpHog` / `kpMarker` migrated to `kschedSpawn`; AP `kschedLoop` wired; `waitForEvents` co-existence hook; gate `scripts/test_preempt_kernel.sh runPreemptProbe=true` PASS

## M2 — `fsTask` on kernel thread

- [ ] M2.1 — `fsReqCh` → `fsReqQ KQueue[*fsRequest]`; per-request reply → embedded `KEvent` + `*fsResponse`; all five `fsSend*` callers rewired; `fsTask` via `kschedSpawn`; gate `scripts/test_sleeptest_postrevert.sh` ≥ 50 %; interactive FS works

## M3 — Timer wheel + kernel-context `afterTicks` consumers

- [ ] M3.1 — Land `KEvent` (`src/kthread_event.go`) + `KQueue[T]` (`src/kthread_queue.go`) standalone types
- [ ] M3.2 — `timerEntry.ch chan<- struct{}` → `timerEntry.ev *KEvent`; `KEventAfter(d)` + `kschedTimedPark(d)` land in `src/afterticks.go`; `afterTicks` channel shim retained for user-hosted callers
- [ ] M3.3 — `timerDispatcher` via `kschedSpawn`; body `runtime.Gosched` → `kschedYield`; drop `kernelYield()`; kernel-hosted `<-afterTicks` sites rewired; gate `test_sleeptest_postrevert.sh` S2 parity + `test_net.sh` PASS + `test_tcp_longidle.sh 60` PASS

## M4 — `ring3Wrapper` + net services + user-hosted sleep/recv

- [ ] M4.1 — `ring3Wrapper` kernel-thread rewrite; `tssSetRSP0ForKernelThread` helper; `kschedSwitchPostCR3` hook; `gInfoByTask` / `gooosOnResume` / `registerRing3G` / `unregisterRing3G` deleted; `Process.exitCh` → `ExitEv KEvent` + `ExitCode uintptr`; `processExit` + `processWait` rewired
- [ ] M4.2 — `netRxLoop`, `udpEchoServer`, `tcpRTOScannerLoop`, `tcpEchoServer` + per-connection workers migrated to `kschedSpawn`
- [ ] M4.3 — `sys_sleep` → `kschedTimedPark`; `sys_recvfrom` timeouts → bounded-poll; `afterTicks` channel shim deleted; `ring3StackPoolCh` replaced with `KQueue[int32]` or bitmap
- [ ] M4.4 — Gate: `test_sleeptest_postrevert.sh ITERATIONS=50` ≥ 80 % (F1 closure); `test_net.sh` + `test_tcp_longidle.sh 300` + `test_smp_shell_preempt.sh` + `test_smp_release_gate.sh` + `test_smp_basic.sh` + `test_ps.sh` all PASS

## M5 — TinyGo-patch trim + `scheduler=none` flip

- [ ] M5.1 — Trim `scripts/tinygo_runtime.patch` per §08 (delete ~510 lines across `queue.go`, `task_stack_multicore.go`, `gc_blocks.go`, `scheduler_cooperative.go`, `scheduler_cores.go`); adjust `runtime_gooos.go main` entry; trim `interrupt_gooos.go`; update `scripts/patch_tinygo_runtime.sh` sentinels; re-apply in `~/.local/tinygo0.40.1/`
- [ ] M5.2 — `src/target.json` `scheduler=cores` → `scheduler=none`; `scripts/verify_globals.sh` asserts updated to kthread globals; full regression sweep

## Post-M5 work

- [ ] P1 — Reviewer sub-agent pass; BLOCKING fixed; MINOR → `no_goroutine_kernel_design/12_implementation_notes.md`
- [ ] P2 — README.md §11 diff applied; `impldoc/` + `current_impl_*/` sweep for stale refs; `current_impl_<today>/` successor doc created
- [ ] P3 — Final sweep: `grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ user/ scripts/` clean for new-in-this-cycle; full M5 gate suite PASS; `make -C user all` clean

## Deferred

*(Items the cycle chose not to complete; surfaced in the final
report.)*

<!-- append deferred items here during execution -->

## Notes

- Pre-existing staged TODO.md renames are left alone; every
  Route C commit uses explicit pathspec to avoid them.
- `git push` and branch ops require explicit user instruction.
- Session-resumability: a future session reads this file +
  `git log --oneline` from `7f81f12..HEAD` and continues from the
  next unticked item.
