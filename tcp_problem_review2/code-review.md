# TCP Late-Timing RX Stall: Bug-Focused Code Review (Round 2)

## 1) `src/afterticks.go` (primary suspect)

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

Reference: `src/afterticks.go:26-35`

### Why it is suspicious

- This function allocates a channel and spawns a goroutine **every time**.
- It is used as a core timing primitive across kernel and syscall loops.
- If goroutine lifetime/reclamation is delayed, this creates monotonic pressure and scheduling churn.

## 2) Repeating call sites that can amplify the issue

### `src/tcp_retx.go`

Reference: `src/tcp_retx.go:138-142`, period defined at `src/tcp_retx.go:113`

- The retransmission scanner continuously runs `<-afterTicks(5)` (50 ms).
- This can continuously create timer worker goroutines during normal operation.

### `src/tcp.go`

References: `src/tcp.go:889-894`, `src/tcp.go:1351-1357`

- Kernel echo service also uses `<-afterTicks(5)` while idle.
- This lives directly on the affected TCP kernel path.

### `src/netsock.go` / `src/userspace.go`

References: `src/netsock.go:593`, `src/netsock.go:648`, `src/netsock.go:784`, `src/userspace.go:433-437`

- TCP accept/connect/recv wait loops and `sys_sleep` also use `afterTicks`.
- These add more timing goroutine churn once Ring-3 is active.

## 3) RX path dependency on continuous scheduler progress

### `src/net.go`

References: `src/net.go:72-99`, `src/net.go:64-69`

- `netRxLoop()` is the loop that drains descriptors and dispatches frames.
- Loss of scheduling for this goroutine is sufficient to explain “ISR-level activity without network-layer progress”.
- File-local diagnostics already describe the same behavioral pattern (timer-based periodic goroutines stop; `netRxLoop` survives longer).

### `src/e1000_irq.go`

Reference: `src/e1000_irq.go:42-71`

- ISR collects cause bits, updates diagnostics/flag, and sends EOI.
- It does not perform full RX drain/dispatch itself.
- Therefore scheduler stalls can surface as RX stalls even with IRQ delivery.

## 4) Runtime-side signals that support suspicion

### `~/.local/tinygo/src/internal/task/task_stack.go`

Reference: `~/.local/tinygo/src/internal/task/task_stack.go:92-135`

- Goroutine start allocates stack with `runtime_alloc(...)`.
- New `Task{}` is allocated and queued.
- Confirms that per-call `go` in `afterTicks` is real runtime allocation work.

### `~/.local/tinygo/src/runtime/scheduler.go`

Reference: `~/.local/tinygo/src/runtime/scheduler.go:195-309`

- Cooperative loop with `Gosched()` as requeue+pause.
- No preemptive enforcement that would automatically rescue starved service goroutines.

### `~/.local/tinygo/src/runtime/runtime_gooos.go`

Reference: `~/.local/tinygo/src/runtime/runtime_gooos.go:27-35`

- `sleepTicks` is a busy `sti;hlt;cli` loop.
- This explains why gooos routes sleeps through `afterTicks` and why `afterTicks` is heavily exercised.

## 5) Additional lifetime/reclamation warning signs in repo

### `src/ring3_pool.go` + `src/process.go`

References: `src/ring3_pool.go:4-14`, `src/process.go:428-452`

- Repository explicitly documents that `scheduler=tasks` has no goroutine-reap primitive for this flow.
- Ring-3 wrappers park via `taskPause()` on exit.
- This is direct in-tree evidence that goroutine lifetime handling is already a known operational concern.

## 6) Secondary suspect path retained

### `src/goroutine_tss.go`

Reference: `src/goroutine_tss.go:162-214`

- `gooosOnResume` is a per-resume hook with Ring-3-specific CR3/TSS behavior.
- If timer-goroutine accumulation is disproven by instrumentation, resume-path starvation effects remain a valid second-line suspect.

## 7) Review verdict

Most suspicious cluster:

1. `src/afterticks.go`
2. `src/tcp_retx.go`
3. `src/tcp.go`
4. `src/net.go`
5. `~/.local/tinygo/src/internal/task/task_stack.go`
6. `~/.local/tinygo/src/runtime/scheduler.go`
7. `src/ring3_pool.go` / `src/process.go`

Current best bug hypothesis:

- repeated `afterTicks` goroutine creation + cooperative scheduler/runtime lifetime pressure eventually suppresses scheduler progress for critical kernel service loops (`netRxLoop`), producing the observed late-timing TCP no-echo symptom.

Open nuance:

- current runtime reading supports runtime-pressure/starvation risk strongly, but does not by itself prove a literal fixed small task-slot cap.

