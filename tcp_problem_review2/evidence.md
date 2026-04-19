# TCP Late-Timing RX Stall: Evidence Map (Round 2)

## Symptom and reproducibility

| Claim | Evidence |
|---|---|
| Late-timing TCP access fails while early timing succeeds. | `tcp_problem/01_problem_statement.md:24-32` |
| Reproducer script for the bug is `scripts/test_tcp_latetiming.sh`. | `tcp_problem/01_problem_statement.md:36-55`, `scripts/test_tcp_latetiming.sh:1-81` |
| Handoff identifies scheduler/timer behavior as likely upstream cause. | `tcp_problem/02_evidence_and_hypotheses.md:116-151`, `pasttodos/TODO_NET3.md:478-543` |

## Code evidence for timer-goroutine accumulation risk

| Claim | Evidence |
|---|---|
| `afterTicks()` creates a new goroutine every call. | `src/afterticks.go:26-35` |
| RTO scanner repeatedly calls `afterTicks(5)` forever. | `src/tcp_retx.go:113`, `src/tcp_retx.go:138-142` |
| Kernel TCP echo loop repeatedly calls `afterTicks(5)` while idle. | `src/tcp.go:889-894`, `src/tcp.go:1351-1357` |
| Syscall paths also depend on repeated `afterTicks()` waits. | `src/userspace.go:433-437`, `src/netsock.go:593`, `src/netsock.go:648`, `src/netsock.go:784` |
| Runtime allocates task stack memory for each new goroutine start. | `~/.local/tinygo/src/internal/task/task_stack.go:92-135` |

## Code evidence that RX depends on scheduler progress

| Claim | Evidence |
|---|---|
| RX drain is driven by `netRxLoop()` repeatedly running `drainRxRing()`. | `src/net.go:72-99` |
| `net.go` itself documents `afterTicks`-based periodic goroutines stopping while `netRxLoop` lasts longer. | `src/net.go:64-69` |
| ISR does not drain RX ring directly; it only updates IRQ diagnostics/flag and ACKs interrupt controller. | `src/e1000_irq.go:42-71` |
| Net diagnostics expose `e1000IRQs`, `NetRxLoopWakes`, and `netRxFrames` for this failure class. | `src/net.go:197-200`, `src/netstats.go:36-43` |

## Evidence around runtime behavior and constraints

| Claim | Evidence |
|---|---|
| gooos kernel uses TinyGo cooperative scheduler (not custom scheduler). | `current_impl_doc/scheduler.md:3-8`, `current_impl_doc/scheduler.md:145-157` |
| Runtime scheduler is cooperative and `Gosched()` is queue-and-pause. | `~/.local/tinygo/src/runtime/scheduler.go:195-309` |
| Kernel `sleepTicks` is `sti;hlt;cli` busy wait and not a general goroutine parking primitive. | `~/.local/tinygo/src/runtime/runtime_gooos.go:27-35` |
| gooos explicitly states no goroutine-reap primitive for `scheduler=tasks` in Ring-3 wrapper context. | `src/ring3_pool.go:11-14` |
| Ring-3 wrapper goroutines are parked permanently on exit path (`taskPause`). | `src/process.go:428-452` |

## Items still unproven by code-only reading

| Question | Status |
|---|---|
| Is there a hard-coded small fixed task-slot cap in current runtime? | Not confirmed from current patched runtime files reviewed. |
| Is the sole root cause unreclaimed finished `afterTicks` goroutines? | Plausible and strongly suspected, but still requires direct runtime occupancy instrumentation to prove. |
| Is post-Ring-3 starvation (not timer churn) primary? | Still possible as secondary hypothesis (`src/goroutine_tss.go:162-214`, handoff notes), not strongest by current evidence. |

## This round’s run results

| Command | Result |
|---|---|
| `bash scripts/test_tcp_latetiming.sh` | FAIL |
| `bash scripts/test_tcp_phase1.sh` | PASS |

