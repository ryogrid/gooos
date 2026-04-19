# SMP Deferred Items and Known Issues

Status snapshot post `smp-take3` TinyGo 0.33.0 → 0.40.1 migration
(see `impldoc/smp_migration_overview.md` for the migration plan and
`TODO_SMP3.md` for per-item completion status).
Cross-references `impldoc/smp_overview.md` (items 1-19),
`impldoc/smp_ap_safety_overview.md` (items 0-8),
`TODO_SMP1.md`, `TODO_SMP2.md`, and `TODO_SMP3.md`.

## 1. What Is Implemented

| Area | Status | Details |
|---|---|---|
| Per-CPU storage (GS base) | Done | `src/percpu.go`: PerCPU struct, `cpuID()`, BSP + AP init via `IA32_GS_BASE` |
| Spinlock primitive | Done | `src/spinlock.go` + `src/stubs.S`: xchg-based TTAS with `pause` hint |
| Per-CPU GDT + TSS | Done | `src/gdt.go`: `perCPUGDT[17]`, `perCPUTSS[17]`, `tssSetRSP0` per-CPU |
| Per-CPU ISR depth | Done | `src/isr.S`: `%gs:4` (per-CPU) + global `gooos_in_interrupt_depth` (dual counter) |
| LAPIC register defs + EOI | Done | `src/smp.go`: LVTTimer, TimerDivCfg, etc.; `lapicSendEOI()` |
| LAPIC timer calibration (BSP) | Done | `src/lapic_timer.go`: calibrated against PIT, 100 Hz periodic on BSP |
| IOAPIC driver code | Done | `src/ioapic.go`: MADT type-1 parsing, redirection table, PIC disable — but **disabled at runtime** |
| Per-CPU runqueues | Done | TinyGo `scheduler_cooperative.go` (was `scheduler.go` in 0.33.0): `runqueues[17]`, `scheduleTask` / `Gosched` per-CPU |
| Per-CPU systemStacks | Done | TinyGo `task_stack_amd64.go`: `systemStacks[17]` |
| Queue spinlock | Done | TinyGo `queue.go`: `gooos_spinlockAcquire/Release` on top of upstream's `lockAtomics` (per-CPU-only under `scheduler=tasks`) |
| Heap spinlock | **Retired in 0.40.1 migration** | Upstream 0.40.x ships `gcLock task.PMutex` (no-op under `tinygo.unicore`, real Mutex under `scheduler=cores`). gooos relies on BSP-only-allocates contract under Wave 1; M5 `gcPauseCore` closes the cross-CPU gap under Wave 2 |
| schedLock (sleep/timer) | Done | TinyGo `scheduler_cooperative.go`: spinlock around `sleepQueue`/`timerQueue` access |
| Per-CPU currentTask | Done | TinyGo `task_stack_unicore.go` (new file in 0.40.1; was `task_stack.go` in 0.33.0): `currentTasks[17]` indexed by `cpuID()` |
| Work stealing | **Dormant** | TinyGo `scheduler_cooperative.go`: `stealWork()` function exists but is **not called** from the scheduler's pop site. Wiring it triggers the Ring-3 AP triple-fault below. Enabling deferred to `TODO_SMP3.md` M3 |
| AP scheduler entry function | Done | TinyGo `scheduler_cooperative.go`: `apScheduler()` → `scheduler()` |
| Boot-phase gating | Done | `src/smp.go`: `bspBootDone` flag; APs spin with `gooosPause()` |
| `waitForEvents` override | Done | TinyGo `wait_gooos.go` (kernel) + `wait_gooos_user.go` (userspace, new): `sti; hlt; cli` idle in kernel, no-op in Ring 3 |
| `interrupt.In()` override | Done | TinyGo `interrupt_gooos.go`: always returns false (gooos syscall design; §2.2) |
| IPI send primitive | Done | `src/ipi.go`: `lapicSendIPI()`, wakeup vector 0xFC |
| Page allocator spinlock | Done | `src/vm.go`: `pageAllocLock` |
| Process map spinlocks | Done | `src/process.go`: `procLock` on `procByTask`/`procByPID`/`foregroundProc` |
| gInfoByTask spinlock | Done | `src/goroutine_tss.go`: `gInfoLock` |
| `pause()` assembly stub | Done | `src/stubs.S`: `gooosPause()` for AP spin loops |
| `sys_getcpuid` syscall | Done | `src/userspace.go`: syscall #21 returns `cpuID()` |
| `smpprobe` demo command | Done | `user/cmd/smpprobe`: spawns workers, reports cpuID |
| User-space SMP stubs | Done | `user/runtime_asm_amd64.S`: no-op `spinlockAcquire/Release/cpuID` |
| TinyGo patch (~800 lines) | Done | `scripts/tinygo_runtime.patch` covers all above TinyGo changes; targets `~/.local/tinygo0.40.1/src/`. Size dropped from 853 (0.33.0) after the migration retired the heapLock and split hunks across `scheduler_cooperative.go` / `scheduler.go` / `task_stack_unicore.go` / `wait_gooos_user.go` |

## 2. Known Issues (Blocking AP Scheduling)

### 2.1 Ring-3 Triple Fault on APs

**Symptom**: when an AP steals a `ring3Wrapper` goroutine via
`stealWork()` and calls `jumpToRing3` → `iretq`, the AP
silently triple-faults. No serial output, no panic — the CPU
simply resets.

**Observed behavior**:
- `ring3Wrapper` debug prints confirm: "cpuID=2,
  stackAcquired, jumping to Ring 3" — then silence.
- Single-CPU and `-smp 4` with stealWork disabled: shell
  works perfectly on BSP.
- `-smp 4` with stealWork enabled: shell goroutine stolen by
  AP → triple fault.

**Hypothesis**: the per-CPU TSS or GDT on the AP is not
correctly configured for Ring-3 → Ring-0 transitions. When
user code on the AP fires `int 0x80`, the CPU reads RSP0
from the AP's TSS — if this is wrong or the TSS type bits
are stale after `ltr`, the CPU cannot switch stacks and
triple-faults.

**Investigation needed**: QEMU + GDB step-through of `iretq`
on an AP. Check: (a) AP's TR (Task Register) points at
correct per-CPU TSS; (b) TSS type is 0x9 (available, not
0xB busy); (c) RSP0 in AP TSS matches the ring3StackPool
kernel stack; (d) CR3 in AP matches `proc.pml4`; (e) the
user CS/SS selectors (0x1B/0x23) resolve correctly in the
AP's per-CPU GDT.

**Workaround**: stealWork disabled; all goroutines run on BSP.

### 2.2 AP LAPIC Timer Causes Boot Hang

**Symptom**: enabling `lapicTimerInit()` on APs causes the
system to hang during boot under `-smp 4`. Boot stops at
"Scheduler: TinyGo goroutines active".

**Root cause (suspected)**: the ISR prologue's dual-counter
approach (`incl gooos_in_interrupt_depth(%rip)` + `incl
%gs:4`) races on the global counter when multiple CPUs fire
timer ISRs simultaneously. `incl` is a non-atomic
read-modify-write on x86; two concurrent `incl` on the same
address can lose an update, leaving the global counter
permanently elevated.

**Fix approach**: remove the global counter entirely and
switch `interrupt.In()` to read the per-CPU `%gs:4` counter.
This was attempted but caused "blocked inside interrupt"
panics because syscall handlers call `task.Pause()` while
the per-CPU ISR depth is 1 (which is correct — the goroutine
IS in ISR context). The current fix (§1: `interrupt.In()`
always returns false) eliminates this check, but without the
AP timer the issue is moot.

**Workaround**: AP LAPIC timer disabled; APs wake only via IPI.

### 2.3 IOAPIC IRQ0 Redirection Failure

**Symptom**: after `ioapicInit()` masks the 8259A PIC and
programs the IOAPIC redirection table (IRQ0 → vector 32 to
BSP), PIT timer interrupts stop arriving. `pitTicks` never
increments, causing `afterTicks` and `sys_sleep` to hang.

**Hypothesis**: QEMU's MADT contains type-2 entries
(Interrupt Source Override) that map ISA IRQ 0 to GSI 2 with
level-triggered, active-low polarity. The current
`ioapicSetRedirection()` hardcodes edge-triggered active-high
(the default). Without parsing type-2 entries, the redirection
is incorrect.

**Fix approach**: parse MADT type-2 entries in `parseMADT()`;
apply the override (GSI number, trigger mode bit 15, polarity
bit 13) when programming the redirection table for IRQ0.

**Workaround**: `ioapicInit()` disabled; PIC pass-through via
LAPIC LINT0 (ExtINT) is active.

## 3. Deferred Items from `impldoc/smp_overview.md`

Items from the original 19-item work plan
(`smp_overview.md §4`) that are **not fully functional**:

| Item | Description | Status | Blocker |
|---|---|---|---|
| 7 | IOAPIC discovery + redirection | Code present, disabled at runtime | §2.3 |
| 11 | AP scheduler spawn | Infrastructure done, APs idle | §2.1 |
| 14 | Timer-based preemption | BSP LAPIC timer works; AP timer disabled | §2.2 |
| 15 | Ring-3 goroutines on APs | Triple fault on AP iretq | §2.1 |
| 16 | TLB shootdown for user pages | IPI primitive ready; not wired into processExit | APs not running Ring-3 |
| 17 | processExit cross-CPU cleanup | Spinlocks in place; untested under AP execution | APs not running Ring-3 |

## 4. Deferred Items from `smp_ap_safety_overview.md`

| Item | Description | Status |
|---|---|---|
| 3 | VGA console spinlock wrapping | `vgaLock` declared, functions not wrapped (cosmetic) |
| 4 | Serial port lock | Not implemented (cosmetic) |
| 5 | elfSpawn AP wakeup IPI | Implemented but reverted — APs not in scheduler |
| 6 | IOAPIC type-2 override parsing | Not implemented |

## 5. TinyGo Runtime SMP Gaps (Remaining)

Despite the comprehensive patch (853 lines), the following
TinyGo runtime areas remain problematic under multi-CPU
execution:

| Gap | File | Description |
|---|---|---|
| Map concurrent access | `runtime/hashmap.go` | Go maps are not thread-safe; concurrent `hashmapSet` from multiple CPUs corrupts bucket metadata. gooos kernel maps are protected by spinlocks (`procLock`, `gInfoLock`), but any TinyGo-internal map use (string interning, etc.) is unprotected. |
| GC stop-the-world | `runtime/gc_blocks.go` | GC mark phase scans all runqueues but cannot safely pause APs. `heapLock` protects alloc, but GC mark/sweep while APs allocate simultaneously may miss live roots. |
| `schedulerDone` race | `runtime/scheduler.go:24` | Read by all CPUs without atomic ops. Harmless in practice (only written once at shutdown by BSP). |
| Channel internals | `runtime/chan.go` | Channel struct mutations (blocked list, state transitions) use `interrupt.Disable` only — per-CPU, not cross-CPU safe. Currently safe because channels are goroutine-local and only one goroutine touches a channel end at a time. |

## 6. Priority Order for Remaining Work

1. **Ring-3 triple fault on APs** (§2.1) — highest priority;
   blocks all multi-core user execution. Requires hardware-
   level debugging with QEMU+GDB.

2. **AP LAPIC timer** (§2.2) — needed for AP preemption.
   Blocked by ISR global counter race. Fix: migrate
   `interrupt.In()` fully to per-CPU counter, which requires
   understanding why syscall-blocking goroutines see depth=1
   (expected behavior for gooos's ISR-hosted syscall design).

3. **IOAPIC type-2 parsing** (§2.3) — independent of AP
   scheduling. Can be developed and tested under `-smp 1`.

4. **VGA/serial locks** (§4) — cosmetic only; low priority.

5. **GC stop-the-world** (§5) — correctness issue for
   long-running SMP workloads. Deferred until APs actually
   execute goroutines.
