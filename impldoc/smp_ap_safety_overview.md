# AP Scheduler Safety — Overview and Work Plan

Covers the fixes required to safely enable Application
Processor (AP) participation in the TinyGo scheduler. APs
currently idle in `sti; hlt` (`src/smp.go:263-266`) because
entering the scheduler causes crashes — APs steal kernel
goroutines from the BSP during boot, hitting unsynchronized
shared state.

## 1. Problem Summary

Five issues were discovered during the SMP v2 implementation:

| # | Issue | Root Cause | Current Mitigation |
|---|---|---|---|
| P1 | AP scheduler entry crashes on boot | APs steal goroutines that use unsynchronized shared state (Go maps, channels, globals) | `apSchedulerEntry()` disabled; APs idle |
| P2 | Round-robin goroutine distribution corruption | New goroutines pushed to AP queues during boot execute before BSP finishes init | Reverted; `runqueuePushBack` uses local CPU only |
| P3 | `ioDelay()` spin loops starve BSP | 3 APs doing continuous port 0x80 I/O saturate QEMU's I/O bus | Bare spin loops (`for x == 0 {}`) |
| P4 | `waitForEvents()` panic on APs | TinyGo panics when runqueue + sleep/timer queues are all empty | `wait_gooos.go` overrides with `sti; hlt; cli` |
| P5 | IOAPIC IRQ0 stops PIT timer | QEMU IOAPIC redirection table doesn't deliver PIT interrupts after PIC disable | `ioapicInit()` disabled; PIC pass-through via LINT0 |

## 2. Design Strategy

### 2.1 Boot-Phase Gating

**Goal**: APs must not enter the scheduler until the BSP
has completed its entire boot sequence — all service
goroutines spawned, filesystem populated, shell running.

**Mechanism**: a `bspBootDone` flag in `.bss`:
- BSP sets `bspBootDone = 1` as the **last action before
  `setupUserspace()`** (`src/main.go:~475`).
- APs spin on `bspBootDone == 0` with `pause` instruction
  (not `ioDelay`) before calling `apSchedulerEntry()`.
- After `bspBootDone`, APs enter the scheduler and begin
  work-stealing.

**Why this placement**: `setupUserspace()` calls `elfLoad`
which spawns the shell goroutine and blocks forever on
`exitCh`. By this point, fsTask, keyboardPump, afterTicks,
and all ELF storage are complete. The scheduler has been
running stably on the BSP for the entire boot sequence.

### 2.2 Pre-Existing Goroutines at Gate Release

When `bspBootDone` is set, the BSP's runqueue may contain
goroutines spawned earlier during boot. APs will immediately
steal these. Each must be analyzed for migration safety:

| Goroutine | Spawned at | Migration Safety |
|---|---|---|
| `fsTask` | `main.go:396` | **Safe**. Single goroutine; all access serialized by `fsReqCh` channel. Channel wakeup pushes resumed task to the **sender's** CPU queue (via `runqueuePushBack` in `resumeRX`/`resumeTX`), so the woken `fsTask` returns to the CPU that sent the request. |
| `keyboardPump` | `main.go:397` | **Safe**. Single goroutine draining the SPSC ring buffer. Only one consumer exists, so the single-consumer invariant holds regardless of which CPU runs it. |
| `afterTicks` test | `main.go:351` | **Safe**. Already completed by the time `bspBootDone` is set (it fires after 2 PIT ticks = 20ms; boot takes ~200ms). Not on the runqueue at gate release. |
| `ring3Wrapper` (shell) | spawned by `elfLoad` inside `setupUserspace` | **Safe**. Spawned AFTER `bspBootDone`; uses `procLock` + `gInfoLock` protected maps. |

**Conclusion**: no explicit affinity pinning needed. All
pre-existing goroutines are either complete, single-instance
with channel serialization, or protected by spinlocks.

### 2.3 Goroutine Distribution

**Strategy**: keep `runqueuePushBack` pushing to the local
CPU's queue (`runqueues[gooosCpuID()]`). Rely on work
stealing for distribution. This avoids the round-robin
corruption issue: new goroutines always start on the
spawning CPU, and APs steal idle.

Work stealing is triggered by:
- LAPIC timer (100 Hz) waking APs from `hlt`
- IPI wakeup (`gooosWakeupCPU`) — currently wired but
  not called from channel wakeup path

For more aggressive distribution, `elfSpawn` can send
a wakeup IPI to a target AP after `go ring3Wrapper(child)`.

### 2.4 AP Spin Loop Safety

**Replace `ioDelay(1)` with `pause` instruction**: the x86
`pause` hint reduces power consumption and avoids bus
contention in spin-wait loops. Add a `pause()` assembly
stub to `src/stubs.S`:

```asm
.global pause
pause:
    pause
    ret
```

Use in AP spin loops:
```go
for gdtReady == 0 {
    pause()
}
```

The `pause` call also acts as a compiler barrier, preventing
loop elision.

### 2.5 Remaining Synchronization Gaps

The data audit from `smp_kernel_data_audit.md` + the
exploration above identifies these remaining gaps:

| Variable | File:Line | Status | Fix Needed |
|---|---|---|---|
| **`Queue.Push/Pop`** | `task/queue.go:24-61` | **`interrupt.Disable` only** | **Add spinlock — `interrupt.Disable` is per-CPU, doesn't protect cross-CPU `stealWork` access** |
| `vgaCursorRow/Col` | `vga.go:17-18` | `vgaLock` declared | Wrap top-level VGA functions only (not inner helpers — `Spinlock` is non-reentrant; `vgaConsolePutChar` calls `vgaConsoleScroll` internally) |
| `pitTicks` | `pit.go:22` | Single BSP writer | Acceptable under x86-TSO; atomic increment deferred |
| `gooosKbdHead/Tail` | `keyboard_irq.go:25-26` | SPSC lock-free | Safe (single producer ISR on BSP, single consumer pump) |
| serial port I/O | `serial.go` | No lock | Cosmetic interleaving; optional serial lock |
| `sleepQueue/timerQueue` | TinyGo `scheduler.go:34-36` | `interrupt.Disable` only | Global queues accessed from BSP scheduler only; safe as long as APs don't call `addSleepTask`/`addTimer` |
| `handlers[256]` | `interrupt.go:16` | Boot-write-only | Document invariant: no registration after APs start |
| `fs` (FileSystem) | `fs.go:29` | Serialized via `fsReqCh` channel | Safe; Go channel runtime handles synchronization |

**Critical insight**: with boot-phase gating (§2.1), most
of these gaps are eliminated. APs only enter the scheduler
after all boot-time writes are complete. The remaining
runtime accesses are either per-CPU, spinlock-protected, or
channel-serialized.

### 2.6 IOAPIC IRQ0 Investigation

**Hypothesis**: QEMU's IOAPIC requires parsing MADT type-2
entries (Interrupt Source Overrides) to determine the correct
trigger mode and polarity for IRQ0. The current
`parseMADT()` (`src/smp.go:346-381`) handles types 0 and 1
but **skips type 2**.

On QEMU, the MADT typically contains an override entry
mapping ISA IRQ 0 to GSI 2 with level-triggered,
active-low polarity. Without this override, the IOAPIC
programs IRQ pin 0 as edge-triggered active-high (default),
which may not match how QEMU's virtual PIT signals the
IOAPIC.

**Fix**: parse type-2 entries, apply the override when
programming the redirection table for IRQ0.

## 3. Work Plan

One git commit per top-level item.

- [ ] **0. Queue spinlock for cross-CPU safety**
  - `internal/task/queue.go`: `Push`/`Pop`/`Append`/`Empty`
    currently use `interrupt.Disable`/`Restore` which only
    protects against same-CPU preemption. Under SMP,
    `stealWork()` calls `runqueues[peer].Pop()` from a
    different CPU, racing with the owner's `Push`. Add
    per-queue spinlock (`gooos_spinlockAcquire/Release`)
    around head/tail manipulation, keeping `interrupt.Disable`
    for local ISR safety.
  - Update `scripts/tinygo_runtime.patch` + patch script.
  - User-space builds already have no-op stubs for
    `spinlockAcquire/Release` (`user/runtime_asm_amd64.S`).
  - Verify: `make build` clean; `test_sendkey.sh 1` PASS;
    `test_gochan.sh` PASS.

- [ ] **1. `pause()` assembly stub**
  - `src/stubs.S`: add `pause` instruction wrapper.
  - `src/smp.go`: replace bare spin loops in `apEntry` with
    `pause()` calls.
  - Verify: `make build` clean; `test_sendkey.sh 1` PASS;
    `-smp 4` boot completes without BSP starvation.

- [ ] **2. Boot-phase gating**
  - `src/smp.go`: add `var bspBootDone uint32`.
  - `src/smp.go:apEntry`: add `for bspBootDone == 0 { pause() }`
    before `apSchedulerEntry()`.
  - `src/smp.go:apEntry`: uncomment `apSchedulerEntry()`.
  - `src/main.go`: set `bspBootDone = 1` immediately before
    `setupUserspace()`.
  - Verify: `-smp 4` boots; shell responds; `ls` works;
    `test_sendkey.sh 1` PASS under `-smp 4`.

- [ ] **3. VGA console spinlock wrapping**
  - `src/vga.go`: wrap **top-level entry points only**
    (`vgaConsolePrint`, `vgaConsoleClear`) with `vgaLock`.
    Do NOT wrap inner helpers (`vgaConsolePutChar`,
    `vgaConsoleScroll`) — `Spinlock` is non-reentrant and
    `vgaConsolePutChar` calls `vgaConsoleScroll` internally.
    Create `_locked` wrappers or lock at the print/clear level.
  - Verify: `make build` clean; no deadlock; serial + VGA
    output not garbled under `-smp 4`.

- [ ] **4. Serial port lock (optional)**
  - `src/serial.go` (or wherever): add serialLock spinlock.
  - Wrap `serialPutChar` and `serialPrint` with serialLock.
  - Verify: serial output not interleaved under `-smp 4`.

- [ ] **5. elfSpawn AP wakeup IPI**
  - `src/process.go:elfSpawn`: after `go ring3Wrapper(child)`,
    call `gooosWakeupCPU(targetCPU)` for one idle AP.
  - Verify: `smpprobe` shows workers on different cpuIDs
    under `-smp 4`.

- [ ] **6. IOAPIC type-2 override parsing**
  - `src/smp.go:parseMADT`: handle MADT type-2 entries
    (Interrupt Source Override). Store GSI mapping + flags.
  - `src/ioapic.go:ioapicInit`: apply override when
    programming IRQ0 redirection (GSI, trigger, polarity).
  - Re-enable `ioapicInit()` in `src/main.go`.
  - Verify: `afterTicks: OK` under `-smp 4` with IOAPIC.

- [ ] **7. Regression matrix**
  - Run all existing test harnesses under `-smp 4`:
    `test_sendkey.sh` x3, `test_gochan.sh`, `test_goprobe.sh`,
    `test_tinyc.sh`, `test_pipe_matrix.sh`.
  - `smpprobe` shows multiple cpuIDs.
  - Verify: all PASS, `pf=0`.

- [ ] **8. README.md update**
  - Update SMP progress row to reflect AP scheduler
    participation + multi-core goroutine execution.
  - Mention `smpprobe` demo.

## 4. Dependency DAG

```
[0] Queue spinlock (prerequisite for safe work stealing)
 │
 └──► [1] pause stub
       │
       └──► [2] boot-phase gating (depends on 0+1)
             │
             ├──► [3] VGA spinlock wrapping (top-level only)
             ├──► [4] serial lock (optional)
             ├──► [5] elfSpawn AP wakeup IPI
             │
             └──► [6] IOAPIC type-2 parsing (independent)

[7] regression matrix (depends on 2-6)
[8] README update (depends on 7)
```

## 5. Risk Register

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R-ap-gc | GC mark phase on BSP while AP runs goroutine → concurrent mutation | Medium | High | Deferred (GC stop-the-world); AP-side GC unlikely in practice due to cooperative scheduling |
| R-sleep-queue | AP scheduler calls `addSleepTask` on global `sleepQueue` | Low | Medium | APs don't directly add to sleepQueue (user `sys_sleep` goes through BSP-side `afterTicks`) |
| R-ioapic-qemu | IOAPIC fix may not resolve QEMU quirk | Medium | Low | Keep PIC fallback; IOAPIC is an optimization |
| R-steal-latency | Work stealing latency (100 Hz LAPIC timer) delays AP pickup | Low | Low | elfSpawn IPI wakeup (item 5) addresses this |

## 6. Open Questions

1. **GC stop-the-world**: when an AP triggers GC (via heap
   allocation), does TinyGo's conservative mark phase scan
   all CPU stacks? If not, it may miss live roots on other
   CPUs. This is a fundamental correctness issue deferred to
   a separate design.

2. **sleepQueue/timerQueue per-CPU**: should these move to
   per-CPU or stay global with a spinlock? Current design:
   global with `interrupt.Disable` (BSP only). If APs call
   `time.Sleep`, they hit `addSleepTask` which touches the
   global queue without a lock. Mitigation: user `sys_sleep`
   routes through kernel-side `afterTicks`, not user-side
   `addSleepTask`.

## Reviewer MINOR notes

- P3 description refers to `ioDelay` spin loops which were
  already reverted to bare spin. Historical context; current
  code uses `for x == 0 {}`.
- `pause()` compiler-barrier claim is imprecise: the barrier
  comes from the Go function call, not the x86 `pause`
  instruction itself. `pause` is a CPU pipeline hint.
- IOAPIC type-2 fix (item 6) will need to modify
  `ioapicSetRedirection` to set trigger mode (bit 15) and
  polarity (bit 13) flags, and route to the correct GSI pin.
- IOAPIC parsing (item 6) is independent of AP scheduling
  and can be tested under `-smp 1`.
- `gooosWakeupCPU` exists in `src/ipi.go` but is not called
  from the TinyGo runtime channel wakeup path; accurate.
