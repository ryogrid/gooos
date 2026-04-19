# TCP Late-Timing Review Summary

## Scope reviewed

This review was based on:

- `tcp_problem/` handoff documents, especially `01_problem_statement.md`, `02_evidence_and_hypotheses.md`, `03_gooos_design_map.md`, and `04_investigation_next_steps.md`
- `README.md` networking Path D / Path E description
- the in-repo kernel/runtime glue under `src/`
- the patched TinyGo runtime under `~/.local/tinygo/src/runtime/` and `~/.local/tinygo/src/internal/task/`

The review is intentionally limited to **bug cause and suspicious locations**. It does **not** comment on general implementation quality.

## Main conclusion

The strongest current suspect is still **`afterTicks()`-driven goroutine accumulation in the cooperative TinyGo runtime**, not the TCP state machine and not the e1000 RX logic itself.

The key chain is:

1. `afterTicks()` creates a **fresh goroutine on every call** (`src/afterticks.go:26-35`).
2. Important long-lived kernel loops call it repeatedly, especially:
   - `tcpRTOScannerLoop()` every 50 ms (`src/tcp_retx.go:138-142`)
   - `tcpEchoServer()` every 50 ms while idle (`src/tcp.go:1351-1357`, `src/tcp.go:889-894`)
3. In the patched TinyGo runtime, each new goroutine allocates a heap-backed `Task` plus stack (`~/.local/tinygo/src/internal/task/task_stack.go:92-135`).
4. gooos already documents that `scheduler=tasks` has **no goroutine-reap primitive** and that parked Ring-3 goroutines are not reclaimed promptly enough to avoid heap pressure (`src/ring3_pool.go:4-14`).
5. `netRxLoop()` is the only normal RX drain path, and it must keep getting scheduled to call `drainRxRing()` (`src/net.go:72-84`). Once it stops running, frames cannot reach `ethernetDispatch()`.

Taken together, this makes the documented late-timing stall consistent with **scheduler/runtime-side lifetime pressure or starvation caused by repeated timer goroutine creation**.

## Confidence and caveats

**Confidence: medium-high** that the bug lives in the scheduler/timer/runtime boundary around `afterTicks()`.

Why not higher:

- I did **not** find a hard-coded TinyGo task-slot cap in the current runtime sources. The older "fixed task table" idea appears to belong to the removed pre-TinyGo scheduler, not the current one.
- The current runtime investigation supports **allocation / reclamation / starvation** more strongly than a literal fixed-slot limit.
- The handoff's recommended confirmation step is still valid: explicit runtime instrumentation would be needed to prove whether the stall is caused by unreclaimed tasks, allocator pressure, or some more direct scheduling bias after Ring-3 startup.

## Strongest suspicious locations

| Location | Why it is suspicious |
| --- | --- |
| `src/afterticks.go:26-35` | Spawns a new goroutine per timer request. This is the most concentrated and repeatedly exercised suspect. |
| `src/tcp_retx.go:138-142` | Creates a new `afterTicks()` worker every 50 ms for the global retransmission scanner. |
| `src/tcp.go:1351-1357` and `src/tcp.go:889-894` | Creates a new `afterTicks()` worker every 50 ms when the kernel TCP echo server is idle. |
| `~/.local/tinygo/src/internal/task/task_stack.go:92-135` | Confirms that each goroutine creation allocates a heap-backed task state and stack. |
| `src/ring3_pool.go:4-14` | Repo-local evidence that gooos already compensates for TinyGo goroutine lifetime/reclaim limitations. |
| `src/net.go:64-84` | Explicitly documents that `netRxLoop()` kept running longer because it does **not** use `afterTicks()`, and that `afterTicks`-based periodic goroutines stop firing after a small number of iterations. |
| `~/.local/tinygo/src/runtime/scheduler.go:195-309` | Confirms the scheduler is cooperative and that `Gosched()` simply requeues the current task; there is no preemptive rescue path for starved kernel goroutines. |

## Secondary hypothesis that remains open

The fallback explanation is still **post-Ring-3 scheduler starvation**, not a network-layer bug:

- `current_impl_doc/scheduler.md` documents a fully cooperative scheduler and the `gooosOnResume` Ring-3 resume hook.
- `ring3Wrapper()` / `gooosOnResume()` remain worth watching if runtime instrumentation does **not** show growing task pressure.

At the moment, however, the source-level evidence points more strongly to `afterTicks()` than to CR3/TSS/Ring-3 resume logic.

## What I was able to confirm live

- `bash scripts/test_tcp_latetiming.sh` currently **fails** in this environment.
- `bash scripts/test_tcp_phase1.sh` currently **passes**, which supports the handoff claim that the issue is **time-dependent**, not a general TCP bring-up failure.

One nuance: my local late-timing repro confirmed the external failure, but its retained serial tail did not independently prove the post-stall IRQ-count behavior described in the handoff. That specific point still comes primarily from the documented prior investigation rather than from the short local rerun.
