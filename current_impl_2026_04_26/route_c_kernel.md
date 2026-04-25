# Route C kernel — as-built (2026-04-26)

This document describes the gooos kernel as built after the
Route C migration (M0..M5 + post-M5 P1 reviewer pass) under
TinyGo `scheduler=none`. It is the successor to
`current_impl_2026_04_24/` and supersedes the pre-Route C
descriptions in older `current_impl_*` dirs and in the README's
progress table.

For the per-milestone history, see
`no_goroutine_kernel_design/12_implementation_notes.md`. For the
post-M5 plan that this state implements, see
`no_goroutine_kernel_design/13_post_m5_completion.md`.

## Architectural shape

### Scheduler

- `src/target.json` `"scheduler": "none"`. No Go goroutines exist
  in the kernel (`grep -c '^[[:space:]]*go ' src/*.go` = 0).
- All kernel work runs on **kernel threads** (kthreads) managed
  by the gooos scheduler in `src/kthread_*.go`.
- Per-CPU FIFO ready queues `kschedQueues[maxCPUs]` with
  intrusive `KernelThread.WakeLink`.
- BSP boot: enters `kschedLoopOnce` from `elf.go`'s pump while
  waiting on the boot shell to exit.
- AP boot: `apSchedulerEntry()` calls `kschedLoop()` directly
  (`runtime.apScheduler` doesn't exist under scheduler=none).
- Cross-CPU wake: `kschedPush(t, cpu)` sends a wake IPI via
  `gooosWakeupCPU(cpu)` when target is remote; `kschedLoop`'s
  empty-queue path uses `sti; hlt; cli` so the AP halts and
  receives the IPI.
- Work-stealing: `kschedLoopOnce` and `kschedLoop` both attempt
  `kschedSteal` from peer queues when local pop returns nil.

### Sync primitives

- **Spinlock** (`src/spinlock.go`): unchanged from pre-Route C;
  rank table extended through 17 to cover the new locks.
- **KEvent** (`src/kthread_event.go`): single-shot edge-triggered
  event. `Wait` parks the calling kthread; `Signal` wakes all
  parked waiters via `kschedWake`. Rank 14.
- **`fsReqQueue`** (`src/kthread_queue.go`): bounded MPSC of
  `*fsRequest`. Producers from any context; consumer is
  `fsTask`. Rank 13a.
- **`udpDgramQueue`** (`src/kthread_queue.go`): bounded MPSC of
  `UDPDatagram` with `Push`/`TryPush`/`Pop`/`TryPop`. Used by
  UDP RX path (`udpHandle.TryPush`) + sys_recvfrom bounded-poll
  + `udpEchoServer`. Rank 13b.
- **Timer wheel** (`src/afterticks.go`): single-dispatcher
  `timerDispatcher` kthread serves both legacy `chan struct{}`
  and `KEvent` fires. `kschedTimedPark(d)` parks the calling
  kthread on a stack-local KEvent. Rank 12.
- **`serialLock`** (`src/serial.go`): full-line lock around
  COM1 writes so concurrent kthreads don't interleave output.
  Also held by `consoleStdout.Write` (the user-syscall sys_write
  path). Rank 17 leaf.

### Kernel threads spawned at boot

| Kthread | Spawn site | Pinning | Notes |
|---|---|---|---|
| `timerDispatcher` | `afterTicksInit` (early in main) | round-robin | drives KEvent fires + chan sends |
| `fsTask` | `main()` after `kschedInit` | CPU 0 (`kschedSpawnAt`) | so BSP elf.go pump can dispatch via local pop |
| `netRxLoop` | `netSpawnServices` | round-robin | e1000 RX dispatch (uses `kschedYield`) |
| `udpEchoServer` | `netSpawnServices` | round-robin | port 7 echo |
| `tcpRTOScanner` | `tcpStartRTOScanner` | round-robin | TCP retransmit scanner |
| `tcpEchoServer` | `tcpInit` | round-robin | port 8080 echo |
| `netDiagLoop` | `main()` | round-robin | periodic netDiag dump |
| `boot shell ring3WrapperKT` | `elf.go elfLoad` | CPU 0 (`kschedSpawnRing3WrapperOnBSP`) | exec'd children round-robin |
| `smpBasicProbe` (gated) | `bootActivatePostShellReady` | AP 1 (`kschedSpawnAt`) | SMP probe shows non-zero cpuID |
| `kpHog`, `kpMarker` (gated) | `bootActivatePostShellReady` | round-robin | preempt probe |

### Ring 3 process host

- `ring3WrapperKT()` (in `src/kthread_ring3.go`) is the kthread
  entry. Reads `kthreadHostedProc[t.Slot]` to resolve the
  `*Process`, installs CR3 + TSS.RSP0 (via
  `kthreadResumeRing3Ctx`), calls `setCurrentProc`, sets DPL3
  on int 0x80, jumps to Ring 3 via `jumpToRing3`.
- `kthreadResumeRing3Ctx()` re-installs CR3 + TSS.RSP0 +
  per-CPU `CurrentPoolIdx` after every park-then-resume
  (`kschedYield`, `kschedPark`, `KEvent.Wait`, `fsReqQueue.Push/Pop`,
  `udpDgramQueue.Push/Pop`).
- Per-CPU `currentProc()` (in `src/process.go`) primarily reads
  `procByPoolSlot[perCPUBlocks[cpu].CurrentPoolIdx]` because
  under scheduler=none `internal/task.Current()` returns a
  shared `mainTask` for all kthreads (so the legacy
  `procByTask[taskCurrent()]` map is inadequate).
- `processWait` parks the parent kthread via
  `kschedTimedPark(1)` polling `proc.Exited` (Go chan recv
  from kthread context is the H-01 hazard).
- `processExit` from kthread context calls `kschedExit(code)`
  after clearing `kthreadHostedProc[t.Slot]`.

### Syscalls + kthread-context discipline

Every syscall handler that previously called `runtime.Gosched()`
or recv'd from `afterTicks(d)` now branches on
`kschedRunning[cpuID()] != nil` and uses `kschedYield()` or
`kschedTimedPark(d)` respectively:

- `sysYieldHandler` (`src/userspace.go`): `kschedYield` from kthread.
- `sysSleepHandler`: `kschedTimedPark` from kthread.
- `sys_accept` / `sys_connect` / `sys_tcp_recv` poll loops
  (`src/netsock.go`): `kschedTimedPark(5)` from kthread.
- `sys_recvfrom` UDP timeout: bounded-poll with
  `udpDgramQueue.TryPop` + `kschedTimedPark(5)`.
- `keyboardReadEventBlocking` AP path (`src/keyboard_irq.go`):
  `kschedTimedPark(1)` from kthread.
- `arpResolve` (`src/arp.go`): `kschedTimedPark(1)` from kthread.

### TinyGo runtime patch

`scripts/tinygo_runtime.patch` still adds the gooos hooks
(`runtime_gooos.go`, `interrupt_gooos.go`, `wait_gooos.go`,
`gc_blocks.go gcLockWord` spinlock, etc.). Under
`scheduler=none` many of these hunks are inert (the patched
files aren't compiled). The split into
`runtime_gooos_sched_cores.go` (gated by `!scheduler.none`)
keeps the dead-under-scheduler-none `currentCPU` + `currentTask`
references out of the build.

`src/scheduler_none_stubs.go` provides `tinygo_task_exit` as a
halt stub (the kernel asm `task_stack_amd64.S` references it,
but the path is unreachable when no goroutines exist).

`scripts/verify_globals.sh` accepts EITHER pre-Route C runqueue
symbols OR post-M5.2 kthread globals
(`main.kschedQueues`, `kthreadPool`, `kschedRunning`,
`kthreadHostedProc`).

## Gates passing under scheduler=none

| Gate | Result |
|---|---|
| `make build` (lint + verify-globals) | clean |
| `scripts/test_kthread_smoke.sh` | PASS (A=5 B=5 ok=1) |
| `scripts/test_ps.sh` | PASS (header=1 row=1) |
| `scripts/test_net.sh` | PASS (UDP echo + netDiag) |
| `scripts/test_preempt_kernel.sh` | PASS (markers ≥ 5; 13 observed at HEAD `4802e8a`) |
| `scripts/test_smp_basic.sh` 50-iter | 96 % (above 95 % threshold) |
| `scripts/test_smp_shell_distribution.sh` 50-iter | 98 % |
| `scripts/test_sleeptest_postrevert.sh` 50-iter | 66 % (above M2 baseline 50 %; below M4.2.b-g 98 % under scheduler=cores; failures are scheduler timing jitter, not chan-recv hazards) |
| `scripts/test_tcp_longidle.sh 15` | PASS |

## Commit range

- Design base: `7f81f12` (the §00–§12 design doc set).
- Route C M0–M5 + P1 reviewer pass: through `4802e8a`.
- See `git log 7f81f12..HEAD` for the full per-milestone trail.

## Known follow-ups (post-P1 MINOR list)

See `no_goroutine_kernel_design/12_implementation_notes.md`
§ Open issues + risks for the full list. Headlines:

- `Process.exitCh` cap=1 chan: if any path ever resends to a
  full chan from kthread context, panic under scheduler=none.
  Consider conditioning the send on goroutine-context only.
- `procLock` + `pageAllocLock` rank inversion is pre-existing
  (D1 in `pasttodos/TODO_FIX.md`).
- `pipe.ch chan byte` (Ring-3 pipe fds): under scheduler=none
  CAN park on full/empty and panic. Document that scheduler=none
  does not yet support pipe-using user binaries; wire in
  `KQueue[byte]` when the first pipe-using program lands.
- **`make run-smp` keyboard-input crash** (Route C SMP race).
  Under `-smp 4`, keystrokes corrupt CPU state. 10-iter
  measurement at HEAD `a4cfe0d`: 0/10 successful commands,
  5/10 fatal traps (panic / `#DE` / PF in `kschedSwitch`
  / iretq onto bogus RIP). Use `make run` for interactive
  use until rooted. Scoped as post-Route-C M6 follow-up.
  Full report:
  `no_goroutine_kernel_design/12_implementation_notes.md`
  § Open issues + risks.
