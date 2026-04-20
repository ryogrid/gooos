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

### 2.1 Ring-3 Triple Fault on APs — RESOLVED (commit `5aea173`, 2026-04-20)

**Original symptom**: when an AP stole a `ring3Wrapper`
goroutine via `stealWork()` and called `jumpToRing3` →
`iretq`, the AP silently triple-faulted.

**Root cause**: APs never loaded their IDT. An AP starts with
`IDTR = {base=0, limit=0xFFFF}` (reset default). The first
exception on the AP — which under x86-64 `iretq` can be a
`#GP` if any segment selector is invalid in the per-CPU GDT
— vectored through a zero IDT, loading a zero-filled
descriptor from address 0 and immediately triple-faulting.
Evidence: `tmp/m4_qemu.log` shows
`IDT=     0000000000000000 0000ffff` on the AP at the fault
point.

**Fix** (M4-fix, commit `5aea173`): added `idtLoadAP()` in
`src/idt.go` (1-line wrapper around `lidt`) and invoked it
from `src/smp.go apEntry` immediately after `gdtInitPerCPU`
(before enabling the AP's LAPIC). Confirmed: `-smp 4` boots
cleanly with stealWork live; shell goroutine routinely
migrates to AP 1 or AP 3 (`ring3Wrapper: cpuID=N`).

**Investigation methodology**: the issue was diagnosed using
QEMU's `-d int,cpu_reset,guest_errors` flag (no interactive
GDB needed) — the zeroed IDT register in the register dump
at triple-fault was unambiguous. See
`impldoc/smp_m4_ring3_fault.md` for the full investigation
playbook.

### 2.2 AP LAPIC Timer Causes Boot Hang — PARTIAL (racy counter fixed 2026-04-20; second-order hang deferred)

**Status 2026-04-20**: the global-counter race originally
suspected as the cause has been eliminated. `incl
gooos_in_interrupt_depth(%rip)` is gone; ISR depth is now
per-CPU at `%gs:4`, and `interrupt.In()` in
`runtime/interrupt/interrupt_gooos.go` reads that per-CPU
counter while distinguishing syscall context (vector 0x80
branch bumps a separate `SyscallDepth` at `%gs:44`, and
`interrupt.In()` returns false when `SyscallDepth > 0` to
unblock `task.Pause()` from syscall handlers). Commits:
`6a3ef14`, `49b7605`, `f25f839`.

**Remaining symptom**: re-enabling `lapicTimerInit()` on APs
*still* hangs boot under `-smp 4` after "Scheduler: TinyGo
goroutines active". The cause is no longer the dual-counter
race; the hang now points to a different interaction in the
AP timer ISR dispatch path — likely a contention between the
AP's timer-driven scheduler entry and BSP's `setupUserspace`
flow, or a `go_interrupt_handler` split-stack interaction on
the AP. Needs investigation under QEMU + GDB.

**Workaround**: AP LAPIC timer remains disabled (M2-4
deferred). APs wake via the IPI path
(`runtime.schedulerWake → gooosWakeupCPU` broadcast landed
in M3-6, commit `aa5bb91`), which is sufficient for
work-stealing to function — every `scheduleTask` push pokes
idle APs. The consequence is that APs cannot preempt
long-running CPU-bound goroutines; cooperative yield points
or channel ops are required for migration.

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
| 5 | elfSpawn AP wakeup IPI | Done via `runtime.schedulerWake → gooosWakeupCPU` broadcast (2026-04-20, commit `aa5bb91`) |
| 6 | IOAPIC type-2 override parsing | Not implemented |

## 5. TinyGo Runtime SMP Gaps (Remaining)

Despite the patch (post-0.40.1-migration size ~800 lines; was
853 lines on 0.33.0 before the heapLock retirement), the
following TinyGo runtime areas remain problematic under
multi-CPU execution:

| Gap | File | Description |
|---|---|---|
| Map concurrent access | `runtime/hashmap.go` | Go maps are not thread-safe; concurrent `hashmapSet` from multiple CPUs corrupts bucket metadata. gooos kernel maps are protected by spinlocks (`procLock`, `gInfoLock`), but any TinyGo-internal map use (string interning, etc.) is unprotected. |
| GC stop-the-world | `runtime/gc_blocks.go` | GC mark phase scans the current CPU's runqueue via `schedulerRunQueue()` but cannot safely pause APs. Upstream 0.40.1's `gcLock task.PMutex` is a no-op under `tinygo.unicore` (`scheduler=tasks`); gooos relies on the BSP-only-allocates contract (APs idle in `waitForEvents`). Under `scheduler=cores`, `gcLock` becomes a real Mutex and the M5 `gcPauseCore` IPI closes the remaining concurrent-mutator window. |
| Channel internals | `runtime/chan.go` | Channel struct mutations (blocked list, state transitions) use `interrupt.Disable` only — per-CPU, not cross-CPU safe. Currently safe because channels are goroutine-local and only one goroutine touches a channel end at a time. |

## 6. Priority Order for Remaining Work

Status as of 2026-04-20 (commits `5aea173`, `68f6835`,
`aa5bb91`): §2.1 resolved, §1 work-stealing row done,
`scheduler=cores` live, APs executing kernel and user
goroutines. Remaining work, in priority order:

1. **AP LAPIC timer second-order hang** (§2.2) — APs still
   have no independent preemption source. Cooperative yield
   points + IPI wake are enough for work-stealing to
   function, but a compute-bound goroutine on an AP won't
   yield and can block that AP forever. Needs QEMU+GDB
   bisection on the remaining dispatch-path interaction.

2. **GC stop-the-world** (§5) — `gcPauseCore` / `gcSignalCore`
   are still stubs; under `scheduler=cores` this leaves a
   concurrent-mutator window during mark. Not triggered by
   the current test matrix; becomes important for
   long-running SMP workloads. M5 work.

3. **IOAPIC type-2 parsing** (§2.3) — independent of AP
   scheduling. Can be developed and tested under `-smp 1`.

4. **VGA/serial locks** (§4) — cosmetic only; low priority.
