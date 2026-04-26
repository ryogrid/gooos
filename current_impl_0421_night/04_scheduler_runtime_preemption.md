# Scheduler, Runtime Integration, and Preemption

## Runtime Model

Kernel target (`src/target.json`):

- `scheduler = cores`
- `gc = conservative`

Execution model is TinyGo task scheduler with gooos-specific runtime hooks.

## Runtime-Kernel Integration Points

### Task identity and per-goroutine metadata (`src/goroutine_tss.go`)

- `taskCurrent()` linkname bridge to runtime task pointer.
- `gInfoByTask map[uintptr]*gInfo` stores:
  - kernel stack top (`stackTop`)
  - cached `*Process` pointer (`proc`)
- `registerRing3GWithStack()` installs mapping for ring3Wrapper goroutines.
- `gooosOnResume()` (linkname `runtime.gooosOnResume`) runs on every task resume:
  - updates TSS.RSP0
  - selects CR3 (process pml4 or bootPML4)
  - updates `Process.LastCpuID`
  - updates per-CPU `CurrentPoolIdx`

### Interrupt-state helpers (`src/goroutine_irq.go`)

- `readInterruptDepth()` from `%gs:4`
- `readSyscallDepth()` from `%gs:44`
- `readPreemptDisable()` from `%gs:48`

These gates are consumed by preempt ISR logic.

## Kernel Preemption Path (Feature 2.1)

Trigger chain:

1. BSP LAPIC tick (`handleLAPICTimer`) calls `broadcastPreemptIPI()`.
2. Target CPUs receive vector `0xFB` and execute `handlePreemptIPI()`.
3. `handlePreemptIPI()` validates safe-point constraints.
4. If safe and schedulable task exists, yields through `gooosSchedulerYield()` (`runtime.Gosched`).

Safe-point checks in `handlePreemptIPI()`:

- `InterruptDepth > 1` => skip
- `PreemptDisable > 0` => skip and set `WantReschedule`
- `SyscallDepth > 1` => skip
- `taskCurrent() == 0` => skip

## Ring 3 Preemption and Signal Delivery Path (Feature 2.2)

Within `handlePreemptIPI()`:

- If interrupted frame has `CS & 3 == 3`, handler attempts `maybeDeliverSignal(frame)`.
- If signal frame rewrite succeeds, handler returns and ISR epilogue `iretq` enters user signal handler.
- If no signal delivered, fallback is kernel-level yield of hosting goroutine.

Additionally, BSP timer path in `handleLAPICTimer()` attempts immediate signal delivery on BSP when frame indicates Ring 3.

## Preemption Configuration Gates (`src/preempt_config.go`)

- `preemptEnabled` (global compile-time master)
- `runPreemptProbe` (kernel preempt test harness auto mode)
- `runUserPreemptProbe` (user preempt test harness auto mode)
- `runSMPShellPreemptProbe` (shell preempt harness auto mode)

## Scheduler-Visible Invariants

1. Preempt ISR path must remain `//go:nosplit` compatible and avoid unsafe runtime calls.
2. `%gs` offsets are ABI and must stay synchronized between `PerCPU` struct and assembly users.
3. `gooosOnResume()` must tolerate first-run goroutine without registration (`gi == nil` early return).
4. Ring 3 stack ownership is represented by `CurrentPoolIdx`; ISR-side user preempt accounting depends on it.

## Known Risk Surfaces

- `preemptEnabled=true` with diagnostic probes can introduce behavior-sensitive flapping across test runs.
- Mixed Ring 0 / Ring 3 preempt contexts increase dependency on exact `SyscallDepth` and `InterruptDepth` bookkeeping.
- Stability of 2.3 shell preempt behavior currently depends on timing-sensitive APIC/launch interactions.
