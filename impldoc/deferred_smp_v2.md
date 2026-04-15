# Deferred — SMP v2 (items 1–5)

Covers inventory items 1–5 from `deferred_overview.md §1`: moves
goroutine scheduling from BSP-only (v1) to true multi-CPU. This is
the largest deferred block; every item in this file must land
together for SMP v2 to be coherent. A gooos-owned TinyGo fork is
the prerequisite.

## 1. Problem statement

Phase A and Phase B produced a BSP-only system:

- `src/smp.go:apEntry` brings APs up via INIT-SIPI-SIPI but leaves
  them in a `sti + hlt` idle loop. APs never touch the TinyGo
  runqueue.
- `src/isr.S` runs every ISR on whatever stack the current CPU has
  (fine for BSP-only; TSS.RSP0 is per-CPU via `gooosOnResume`,
  but the TSS itself is a single global).
- `gooos_in_interrupt_depth` is a single `.bss` `uint32` — works
  because only the BSP ever increments it.
- `src/keyboard_irq.go`'s ring buffer relies on x86-TSO; the
  SPSC invariant holds because BSP is both producer (ISR) and
  consumer (pump). With the pump migrated to an AP the invariant
  breaks; the design doc flagged this as `R-b5-smp-atomics`.
- TinyGo's runqueue is a single global (`scheduler.go:runqueue`).
  Concurrent `runqueue.Push/Pop` from multiple CPUs corrupts it.

The combined result: v1 leaves 3/4 of an SMP-capable x86_64
machine idle. SMP v2 makes them useful.

## 2. Item 1 — Per-CPU runqueues + work stealing

### 2.1 TinyGo runtime changes (gooos fork)

TinyGo's `scheduler.go` declares:

```go
// ~/.local/tinygo/src/runtime/scheduler.go:29
var runqueue task.Queue
```

The fork replaces the global with a per-CPU slice:

```go
var runqueues [maxCPUs]task.Queue
```

`runqueue.Push` / `runqueue.Pop` call sites (chan.go, scheduler
idle path, resumeRX/resumeTX) get rewritten to:

```go
runqueues[cpuID()].Push(t) // for tasks that should stay local
// or
runqueues[targetCPU].Push(t) // for cross-CPU wakeups
```

`cpuID()` is a new gooos-side helper that reads the LAPIC ID
or (faster) a per-CPU segment base field maintained by ISR
entry.

### 2.2 Work stealing

When a CPU's local runqueue is empty, the scheduler peeks at
peer queues:

```go
func stealWork() *task.Task {
    self := cpuID()
    for i := 0; i < len(runqueues); i++ {
        peer := (self + i + 1) % len(runqueues)
        if t := runqueues[peer].PopTail(); t != nil {
            return t // stolen
        }
    }
    return nil
}
```

`PopTail` is a new `task.Queue` method that pops from the
opposite end vs. `Pop` (to avoid cache-line ping-pong with the
owner's `Push`/`Pop` that operate on the head).

### 2.3 Synchronization

Each per-CPU queue is protected by a short spinlock or
lock-free atomics. The Linux-kernel approach (per-CPU deque
with atomic head / tail) is the model; a simple spinlock on
each queue is acceptable for the first iteration and can be
tightened later.

Spinlock primitive: a new `src/spinlock.go` with
`xchg`-based `Acquire` / `Release`. Go wrappers over
`src/stubs.S` additions for `xchg` if TinyGo's own
`sync/atomic` does not emit the right instructions under
baremetal.

## 3. Item 2 — APIC timer preemption on APs

### 3.1 LAPIC timer programming

Each AP's LAPIC has a local timer. Program it during
`apEntry` (`src/smp.go:191`) to fire at 100 Hz (matching the
BSP's PIT for simplicity):

```go
func lapicTimerInit() {
    lapicWrite(lapicRegLVT_Timer, (0x20+vectorAPICTimer)|lvtTimerPeriodic)
    lapicWrite(lapicRegTimerDivide, 0x3) // divide by 16
    lapicWrite(lapicRegTimerInitCount, lapicTimerCount)
}
```

`lapicTimerCount` calibrated against the PIT at boot: run a
PIT-based 10 ms measurement, read the LAPIC timer's decrement
rate, set `initCount` to the measured per-10ms count.

### 3.2 Per-CPU `handleTimer`

Replace the single `handleTimer` with per-CPU:

```go
//go:nosplit
func handleAPTimer(vector uint64) {
    // No shared pitTicks race because only the BSP writes it.
    // Per-CPU bookkeeping lives in per-CPU storage.
    picSendEOI(0) // or LAPIC EOI
}
```

Register `handleAPTimer` on each AP via `registerHandler` on
its per-CPU IDT (or share the IDT and dispatch by vector).

### 3.3 Shared `pitTicks` race

Keep `pitTicks` as a BSP-only counter (the PIT fires only on
the BSP). APs use their LAPIC timer for preemption but consult
`pitTicks` only via the shared read path (single writer →
naturally atomic on x86). Document this invariant.

## 4. Item 3 — Per-CPU TSS + GDT

### 4.1 Per-CPU GDT

Today `src/gdt.go:gdtInit` loads one GDT with one TSS
descriptor. Under SMP v2 each AP needs its own GDT +
TSS descriptor pointing at its own TSS structure:

```go
var perCPUGDT [maxCPUs][gdtEntries]uint64
var perCPUTSS [maxCPUs]TSS
```

During `apEntry` each AP:

1. Builds its GDT from the BSP template + its own TSS descriptor.
2. `lgdt` of its per-CPU GDT.
3. `ltr` to load its per-CPU TSS.

### 4.2 TSS.RSP0 semantics

`gooosOnResume` (src/goroutine_tss.go) already writes TSS.RSP0
when a Ring-3 goroutine resumes. In SMP v2 the hook must write
the **current CPU's** TSS, not the single BSP TSS. Extend
`tssSetRSP0` to use per-CPU storage:

```go
func tssSetRSP0(top uintptr) {
    perCPUTSS[cpuID()].rsp0 = top
}
```

### 4.3 Ring-3 goroutine migration

If a Ring-3 goroutine is work-stolen from CPU A to CPU B
mid-exec, `gooosOnResume` fires on CPU B and sets CPU B's
TSS.RSP0. No explicit migration protocol needed — the hook
already happens on every resume.

**Risk**: stack coherency. A Ring-3 goroutine's kernel stack
(pool slot from `ring3_pool.go`) contains data written by
CPU A's ISR. If CPU B suddenly hits an ISR on the same stack,
cache-line bouncing occurs; correctness still holds on
x86-TSO.

## 5. Item 4 — `atomic.StoreUint32` / `LoadUint32` retrofit

### 5.1 Keyboard ring buffer

`src/keyboard_irq.go`'s `gooosKbdHead` / `gooosKbdTail` are
currently written with plain `mov`. Under BSP-only v1 this is
safe (x86-TSO, single writer). Under SMP v2 the pump may
migrate to an AP while the ISR still runs on BSP. The writes
need `atomic.StoreUint32` on the producer side and
`atomic.LoadUint32` on the consumer side:

```go
//go:nosplit
func keyboardIRQSend(event uint32) {
    h := atomic.LoadUint32(&gooosKbdHead)
    t := atomic.LoadUint32(&gooosKbdTail)
    if h-t >= kbdRingSize {
        return
    }
    gooosKbdRing[h&(kbdRingSize-1)] = event
    atomic.StoreUint32(&gooosKbdHead, h+1)
}
```

### 5.2 Other shared structures

Audit every Phase-B data structure that multiple goroutines
could touch:

- `gInfoByTask` (`src/goroutine_tss.go`) — map access from
  `gooosOnResume` (called from scheduler context on each CPU).
  Map access needs a lock or a conversion to
  `sync.Map`-equivalent. Conservative approach: protect
  with a spinlock.
- `procByTask` (`src/process.go`) — same reasoning.
- `pitTicks` — read by many, written by one (BSP); x86 aligned
  64-bit reads are atomic; no change needed.
- `gooos_in_interrupt_depth` — moves to per-CPU (item 4.3 in
  the inventory; see §7 of this file).
- Phase-B file scope variables (`savedParent`,
  `fsReqCh`, etc.) — channels are runtime-synchronized already;
  `savedParent` is single-exec only (`phase_b_ring3_and_exec.md
  §6.1`) and so escapes the audit.

### 5.3 TinyGo atomic primitives under baremetal

Confirm that `sync/atomic` in TinyGo emits `lock`-prefixed
instructions on x86 under the gooos target. If it falls back
to "just use mov on small ops", the retrofit is cosmetic. The
author of each site should verify via `objdump -d`.

## 6. Item 5 — LAPIC IPI support

### 6.1 IPI send primitive

New file `src/lapic_ipi.go`:

```go
// lapicSendIPI sends an IPI to the given APIC ID with the given
// vector number.
func lapicSendIPI(apicID uint8, vector uint8) {
    lapicWrite(lapicRegICRH, uint32(apicID)<<24)
    lapicWrite(lapicRegICRL, uint32(vector)|icrFixed|icrAssert)
    for lapicRead(lapicRegICRL)&icrDeliveryPending != 0 {}
}
```

### 6.2 Use cases

1. **Cross-CPU goroutine wakeup**: when CPU A's chan-send
   transfers a value to a task owned by CPU B's runqueue, send
   an IPI to B so B wakes from `sti+hlt` immediately (rather
   than waiting for the next LAPIC timer tick).
2. **TLB shootdown**: when BSP modifies a page table entry
   that APs may have cached, broadcast a TLB-shootdown IPI.
   Mandatory for correctness once APs run Ring-3 code.
3. **Work-steal nudges**: signal an idle CPU that new work is
   available.

### 6.3 IPI handlers

Each use case needs its own vector (one high-priority LAPIC
vector per purpose). `handleIPI_Wakeup` just does EOI (the
wakeup is simply "leave hlt and run scheduler"). TLB shootdown
handler does `invlpg` on the requested vaddrs (passed via a
shared descriptor struct keyed by sending CPU).

## 7. Items stitching: per-CPU `gooos_in_interrupt_depth`

Distinct from item 4 atomics but threaded through SMP v2.
`src/isr.S` increments a single `.bss` counter today. Under
SMP v2 this must be per-CPU:

1. Each AP gets a per-CPU data segment (via `wrmsr` on
   `IA32_GS_BASE`).
2. ISR prologue writes the counter through `gs:offset`:
   `incl %gs:CPU_INTR_DEPTH`.
3. `interruptIn()` (Go side) reads through a
   `//go:linkname`-bridged per-CPU accessor.

This is the single most mechanical change in SMP v2 but touches
every ISR, so it warrants its own subsection:

```asm
# src/isr.S after SMP v2
isr_common:
    pushq %rax
    ...
    pushq %r15
    incl %gs:CPU_INTR_DEPTH
    movq 120(%rsp), %rdi
    ...
```

`CPU_INTR_DEPTH` is the byte offset of the counter within each
per-CPU block.

## 8. Dependencies

**All items 1–5 depend on a gooos-owned TinyGo fork.** The fork's
minimum surface:

- `runtime/scheduler.go` — per-CPU runqueue.
- `internal/task/task_stack.go` + `task_stack_amd64.go` — already
  patched (Phase A/B); SMP v2 adds task migration hooks.
- `runtime/chan.go` — `resumeRX`/`resumeTX` targeting correct
  per-CPU queue.

Between SMP v2 items internally:

```
per-CPU storage (§7)  ←── foundation
     │
     ├─ item 1 (runqueue)
     ├─ item 3 (TSS/GDT)
     ├─ item 4 (atomics retrofit)
     │
     ├─ item 2 (APIC timer)  ←── depends on item 1 + §7
     │
     └─ item 5 (IPI)         ←── depends on §7 for vector
                                 dispatch per CPU
```

Recommended landing order: §7 → item 3 → item 1 → item 4 →
item 5 → item 2.

## 9. Verification

Each SMP v2 item must pass locally plus a combined
multi-core smoke test.

1. `make build` clean.
2. 10/10 sendkey trials under `make run-smp` (currently 4 cores).
   Pass criterion: same as v1.
3. **Concurrency stress**: a new kernel-boot probe that spawns
   4 goroutines, each running a tight counter loop for 1
   second. After 1 second, expect counters roughly equal across
   CPUs (within 20% — work stealing is probabilistic). If one
   CPU dominates, the per-CPU runqueue is not being populated.
4. **IPI smoke**: kernel sends a no-op IPI from BSP to each AP;
   AP's handler increments a per-CPU flag; BSP reads the flags.
5. **TLB shootdown smoke** (paired with item 5): BSP unmaps a
   page, broadcasts shootdown, verifies APs' cached translation
   is invalidated.
6. `grep -rE "TODO|FIXME|HACK|XXX" src/` returns 0 new hits.

## 10. Open questions

1. **TinyGo fork hosting.** The fork needs a durable home
   (gooos repo submodule? dedicated `~/.local/tinygo` fork
   branch? vendored copy of just the scheduler.go + task
   package?). User sign-off required.
2. **Non-x86_64 portability.** Every design in this file
   assumes x86_64 (LAPIC, `wrmsr GS_BASE`, `invlpg`). If gooos
   ever targets ARM, the per-CPU storage mechanism changes
   fundamentally.
3. **Fair scheduling vs work-stealing latency.** Work stealing
   optimizes throughput but can starve short-lived tasks on
   heavily-loaded peers. Likely out-of-scope for v2 (good
   enough), but flag.
4. **Ring-3 CPU affinity.** Is a user process allowed to
   migrate across CPUs mid-execution? If not, add an "affinity
   pinned" flag to `Process` and skip that task when stealing.
5. **Number of CPUs.** Today we boot up to 4 in `make run-smp`;
   APIs assume `maxCPUs = 16`. Confirm real-hardware ceiling
   before picking a constant.

## 11. Risk register delta

- **Retires**: `R-b5-smp-atomics`, `R-smp-runqueue-race`
  (implicit), `R-smp-ipi-missing` (implicit).
- **Adds**:
  - `R-tinygo-fork-divergence` — maintenance cost of keeping the
    fork current with upstream TinyGo.
  - `R-percpu-storage-overhead` — each AP wastes ~4 KiB of
    `.bss` for its per-CPU block; negligible but surface it.
  - `R-cross-cpu-cache-coherency` — while x86-TSO is strong, any
    future use of `WC` memory type or non-temporal stores could
    break assumptions silently.
