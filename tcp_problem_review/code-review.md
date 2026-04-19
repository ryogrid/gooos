# Bug-Focused Code Review

## 1. `src/afterticks.go` — primary suspected cause

```go
func afterTicks(d uint64) <-chan struct{} {
    ch := make(chan struct{}, 1)
    go func() {
        deadline := pitTicks + d
        for pitTicks < deadline {
            runtime.Gosched()
        }
        ch <- struct{}{}
    }()
    return ch
}
```

**Reference:** `src/afterticks.go:26-35`

### Why this is suspicious

- Each call creates a **new goroutine**.
- That goroutine stays alive until its deadline is reached.
- In gooos, `afterTicks()` is used as the replacement for safe delayed waiting because the runtime's `sleepTicks()` path is a busy `sti; hlt; cli` loop, not a normal goroutine-parking primitive (`~/.local/tinygo/src/runtime/runtime_gooos.go:27-35`).
- That makes `afterTicks()` heavily used in exactly the parts of the system that are supposed to remain alive for the full session.

### Bug implication

This function is not just "a timer helper"; it is a **goroutine factory**. If finished timer goroutines are not reclaimed quickly enough, or if the scheduler becomes unfair under steady timer churn, this function is a direct candidate for the late-timing failure.

## 2. `src/tcp_retx.go` and `src/tcp.go` — steady timer-goroutine producers

### `src/tcp_retx.go`

**Reference:** `src/tcp_retx.go:130-142`

The retransmission scanner is implemented as:

```go
for {
    <-afterTicks(tcpRetxScanTicks)
    tcpRTOScanPass()
}
```

With `tcpRetxScanTicks = 5` (`src/tcp_retx.go:108-114`), this creates one new `afterTicks()` worker every **50 ms**.

### `src/tcp.go`

**References:** `src/tcp.go:889-894`, `src/tcp.go:1351-1357`

The kernel TCP echo service does the same thing while idle:

```go
if !work {
    <-afterTicks(tcpEchoPollTicks)
}
```

`tcpEchoPollTicks` is also `5`, i.e. another **50 ms** timer source.

### Why these files matter

The late-timing bug is expressed through the **kernel TCP echo path**. These two loops create a plausible background rate of new timer goroutines even when the machine is mostly idle at the shell prompt.

## 3. TinyGo runtime task creation — heap allocation, not a visible fixed slot table

### `~/.local/tinygo/src/internal/task/task_stack.go`

**Reference:** `~/.local/tinygo/src/internal/task/task_stack.go:92-135`

The patched TinyGo runtime shows:

- each goroutine stack is allocated with `runtime_alloc(stackSize, nil)`
- each goroutine gets a fresh `Task{}`
- the new task is then queued with `runqueuePushBack(t)`

### Review conclusion

I did **not** find a hard-coded small task-slot table in the current runtime sources. That weakens the narrower "fixed slot cap" version of the handoff theory.

However, it does **not** weaken the broader scheduler/runtime suspicion. It redirects it:

- away from a literal fixed-size slot array
- toward **heap growth, delayed reclamation, or scheduler unfairness under repeated short-lived goroutine creation**

## 4. Goroutine exit / non-reap behavior — corroborating evidence from gooos itself

### `src/ring3_pool.go`

**Reference:** `src/ring3_pool.go:4-14`

This file explicitly says:

- `processExit` parks the goroutine via `taskPause()`
- the TinyGo runtime "gives us no goroutine-reap primitive in `scheduler=tasks`"
- without a workaround, long shell sessions exhaust the heap

### `src/process.go`

**Reference:** `src/process.go:428-452`

`processExit()` really does end by parking the goroutine forever with `taskPause()`.

### Why this matters

This is the best repo-local evidence that **task lifetime and reclamation behavior is already a known runtime limitation in this codebase**. The current TCP late-timing failure fits the same family of problems.

## 5. `src/net.go` — why the symptom appears as an RX stall

### `netRxLoop()`

**Reference:** `src/net.go:72-84`

`netRxLoop()` is a plain:

```go
for {
    drainRxRing()
    ...
    runtime.Gosched()
}
```

It is the ordinary path that drains RX descriptors and forwards frames into `ethernetDispatch()`.

### Review significance

- The e1000 IRQ handler does **not** drain the RX ring itself.
- If `netRxLoop()` stops being scheduled, RX stops even if interrupts still arrive.
- `src/net.go:64-69` already documents that `afterTicks`-based periodic goroutines stopped firing while `netRxLoop()` lasted longer specifically because it does **not** depend on `afterTicks()`.

That comment is one of the most valuable pieces of evidence in the tree.

## 6. `~/.local/tinygo/src/runtime/scheduler.go` — cooperative scheduler behavior

**Reference:** `~/.local/tinygo/src/runtime/scheduler.go:195-309`

Important properties of the active scheduler:

1. It is **cooperative**.
2. `Gosched()` simply pushes the current task back onto the runqueue and pauses it.
3. There is no preemptive correction if one class of goroutines becomes unfairly favored or if scheduler progress degrades under task churn.

### Bug implication

If `afterTicks()` produces enough allocation/reclamation pressure, or if post-Ring-3 execution biases the runnable set in a bad way, the scheduler structure here provides no obvious safety net to keep `netRxLoop()` making progress.

## 7. Alternate suspect kept open: post-Ring-3 starvation

### Relevant files

- `src/process.go:194-217`
- `src/goroutine_tss.go:162-214`
- `current_impl_doc/scheduler.md:57-87`

### Why it stays on the list

The late-timing symptom starts **after** Ring-3 shell startup, and the kernel uses a `gooosOnResume()` hook on every goroutine switch for Ring-3-aware resume behavior.

### Why it is still secondary

I did not find a concrete code path in these files that directly explains the documented "after a small number of timer-driven firings, kernel goroutines stop making progress" pattern as well as `afterTicks()` does.

So these files remain **secondary suspects**, not primary ones.

## 8. Review verdict

### Most likely bug-bearing cluster

1. `src/afterticks.go`
2. `src/tcp_retx.go`
3. `src/tcp.go`
4. `~/.local/tinygo/src/internal/task/task_stack.go`
5. `src/ring3_pool.go`
6. `~/.local/tinygo/src/runtime/scheduler.go`

### Current best explanation

The late-timing TCP RX stall is most plausibly caused by **repeated creation of short-lived timer goroutines that the current TinyGo runtime / gooos combination does not reclaim or schedule robustly enough over time**, eventually starving or suppressing `netRxLoop()` and thereby preventing RX descriptors from being drained.

### Important nuance

What the current source supports best is **runtime-side task accumulation / lifetime pressure**, not a proven literal fixed-size task-slot cap. The distinction matters when choosing where to instrument or fix the issue, but it does **not** change the main suspicious code locations.
