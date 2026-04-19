# TCP Late-Timing Review Evidence

## External symptom and scope

| Claim | Evidence | Review note |
| --- | --- | --- |
| The failing path is the kernel TCP echo listener on guest port 8080 exposed as host port 10080. | `README.md:77-83` | This matches the handoff's "Path D" reproducer. |
| The bug is time-dependent: the same TCP path works early after boot but fails after the guest has been idle longer. | `tcp_problem/01_problem_statement.md:3-32` | This is the primary symptom to explain. |
| The current success target is `scripts/test_tcp_latetiming.sh` and the short-timing TCP scripts are expected to remain green. | `tcp_problem/01_problem_statement.md:34-82` | This frames the problem as a late-runtime failure, not an initial bring-up failure. |

## Direct code evidence supporting the main suspicion

| Claim | Evidence | Review note |
| --- | --- | --- |
| `afterTicks()` creates a new goroutine every time it is called. | `src/afterticks.go:26-35` | This is the highest-value source location in the investigation. |
| `netRxLoop()` itself does not use `afterTicks()` and explicitly calls out that `afterTicks`-based periodic goroutines stop firing after a few iterations. | `src/net.go:64-84` | This is a strong in-tree corroboration of the handoff's scheduler-side theory. |
| The retransmission scanner repeatedly creates new timer workers through `afterTicks()`. | `src/tcp_retx.go:130-142` | At 100 Hz PIT and `tcpRetxScanTicks = 5`, this loop creates one timer goroutine every 50 ms. |
| The kernel TCP echo loop also repeatedly creates new timer workers while idle. | `src/tcp.go:889-894`, `src/tcp.go:1351-1357` | This adds another steady 50 ms timer-driven source under exactly the TCP path that later stops responding. |
| The patched TinyGo runtime allocates a heap-backed stack for each new goroutine. | `~/.local/tinygo/src/internal/task/task_stack.go:92-112` | There is no fixed slot table visible here; goroutine creation is real heap allocation. |
| New goroutines are represented as newly allocated `Task` objects and then queued. | `~/.local/tinygo/src/internal/task/task_stack.go:130-135` | This supports a heap-pressure / reclamation / scheduler-pressure interpretation. |
| The scheduler is cooperative, not preemptive. | `~/.local/tinygo/src/runtime/scheduler.go:195-309`; `current_impl_doc/scheduler.md:143-157` | If a class of goroutines stops being scheduled fairly, there is no preemptive correction mechanism. |
| `Gosched()` only requeues the current task and pauses. | `~/.local/tinygo/src/runtime/scheduler.go:306-309` | This matters because `afterTicks()` workers and `netRxLoop()` both depend on cooperative re-entry. |

## Evidence that task/goroutine lifetime behavior is already a known runtime concern

| Claim | Evidence | Review note |
| --- | --- | --- |
| gooos already carries a workaround because TinyGo `scheduler=tasks` does not provide a goroutine-reap primitive for Ring-3 wrappers. | `src/ring3_pool.go:4-14` | This is one of the strongest repo-local clues that task lifetime / reclamation behavior is relevant to the current bug class. |
| `processExit()` parks the Ring-3 goroutine forever with `taskPause()`. | `src/process.go:428-452` | This matches the `ring3_pool.go` comment and shows parked goroutines are part of the real system model. |
| The boot shell path also blocks forever on process exit rather than letting main return. | `src/elf.go:237-246`, `src/userspace.go:755-769` | This reinforces that long-lived parked tasks are normal in this kernel/runtime combination. |

## Evidence that the network layer itself is a less likely root cause

| Claim | Evidence | Review note |
| --- | --- | --- |
| `netRxLoop()` is the normal RX drain path. | `src/net.go:72-84` | If this goroutine stops running, no ordinary path reaches `drainRxRing()`. |
| RX dispatch only happens through `drainRxRing()` -> `ethernetDispatch()`. | `src/net.go:87-130` | This makes scheduler starvation of `netRxLoop()` sufficient to explain the symptom. |
| The e1000 IRQ handler only sets a readiness flag and acknowledges the interrupt; it does not drain the ring itself. | `src/e1000_irq.go:38-71` | This means continued IRQ delivery alone is not enough to keep RX alive. |
| The short-timing TCP phase still works in the current environment. | Local run of `bash scripts/test_tcp_phase1.sh` in this review session | This weakens pure "NIC / RX path fundamentally broken" explanations. |

## What I did **not** confirm from current source alone

| Question | Current answer | Why it matters |
| --- | --- | --- |
| Is there a hard-coded TinyGo task-slot cap in the current runtime? | **Not found** in reviewed runtime sources. | The current code supports heap-backed task allocation more than a literal small task table. |
| Is the bug definitely caused by unreclaimed finished `afterTicks()` tasks? | **Not yet proven** | The code makes this plausible, but the final proof still requires instrumentation of task count / heap pressure / runnable state over time. |
| Is post-Ring-3 starvation the real cause instead? | **Still possible, but weaker** | If instrumentation does not show monotonic task accumulation or heap pressure, the Ring-3 scheduling path becomes the next strongest suspect. |

## Live validation notes

| Command | Result | Note |
| --- | --- | --- |
| `bash scripts/test_tcp_latetiming.sh` | FAIL | Confirms the late-timing bug is present in the current environment. |
| `bash scripts/test_tcp_phase1.sh` | PASS | Confirms the early TCP echo path still works. |

The late-timing rerun was useful as a symptom check, but its preserved serial tail did not independently capture the post-SYN IRQ progression discussed in the handoff. That part remains sourced from `tcp_problem/02_evidence_and_hypotheses.md` and `pasttodos/TODO_NET3.md:472-574`.
