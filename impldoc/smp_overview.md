# SMP v2 — Overview and Work Plan

This document is the entry point to the design set for bringing
gooos from BSP-only execution to true multi-processor support.
Seven documents under `impldoc/smp_*.md` together provide a
complete, Claude-Code-executable blueprint.

Supersedes `impldoc/deferred_smp_v2.md` (items 1-5 from
`impldoc/deferred_overview.md`) for implementation guidance.
The deferred doc is preserved as historical record.

## 1. Problem Statement

SMP v1 (`src/smp.go`) discovers APs via ACPI MADT, copies a
real-mode trampoline to 0x8000, and boots them with
INIT-SIPI-SIPI. Each AP prints "AP N online" to serial and
enters `sti; hlt` forever (`src/smp.go:211-215`). All
goroutines — kernel services, Ring-3 wrappers, the shell — run
on the BSP.

The combined result: on a 4-core QEMU (`make run-smp`), 3 of
4 cores are permanently idle. No user program benefits from
additional processors; a CPU-bound goroutine monopolizes the
BSP while APs halt.

### 1.1 Blockers

| ID | Blocker | Current State | Home Doc |
|---|---|---|---|
| B1 | No per-CPU storage | GS base unused; no way to identify current CPU in O(1) | `smp_percpu_and_sync.md §1` |
| B2 | Single global GDT + TSS | `tss[104]` shared; `tssSetRSP0` writes one TSS for all CPUs | `smp_percpu_and_sync.md §5` |
| B3 | Single global runqueue | `~/.local/tinygo/src/runtime/scheduler.go:28` — one `task.Queue` | `smp_kernel_scheduler.md §2` |
| B4 | Single global `systemStack` | `internal/task/task_stack_amd64.go:7` — AP scheduler would collide | `smp_kernel_scheduler.md §3` |
| B5 | No spinlocks | All critical sections use `cli`/`sti` (single-CPU only) | `smp_percpu_and_sync.md §4` |
| B6 | No LAPIC timer | PIT fires on BSP only; APs have no timer tick source | `smp_kernel_lapic_and_ipi.md §2` |
| B7 | No IPI support | APs cannot be woken from `hlt`; no cross-CPU wakeup | `smp_kernel_lapic_and_ipi.md §6` |
| B8 | No IOAPIC | PIC passthrough via LINT0; IRQs cannot be routed to APs | `smp_kernel_lapic_and_ipi.md §8` |
| B9 | Shared data unsynchronized | `procByTask`, `gInfoByTask`, `pitTicks`, etc. | `smp_kernel_data_audit.md` |
| B10 | ISR depth is global | `gooos_in_interrupt_depth` in `.bss` — single counter | `smp_percpu_and_sync.md §6` |
| B11 | No TLB shootdown | `invlpg` is CPU-local; AP caches stale after BSP unmap | `smp_kernel_lapic_and_ipi.md §7` |
| B12 | No preemption | Cooperative scheduler; CPU-bound goroutine blocks its core | `smp_kernel_lapic_and_ipi.md §5` (partially: timer wakes idle CPUs; true in-goroutine preemption deferred to v3) |

## 2. Document Coverage Table

| Doc | Sections | Blockers Addressed |
|---|---|---|
| `smp_percpu_and_sync.md` | Per-CPU storage, spinlock, per-CPU GDT/TSS, ISR depth | B1, B2, B5, B10 |
| `smp_kernel_scheduler.md` | TinyGo fork, per-CPU runqueue, systemStack, work stealing, AP scheduler | B3, B4 |
| `smp_kernel_lapic_and_ipi.md` | LAPIC timer, IOAPIC, IPI, TLB shootdown, preemption | B6, B7, B8, B11, B12 |
| `smp_kernel_data_audit.md` | Global variable audit, fix strategy per variable | B9 |
| `smp_user_multicore.md` | Ring-3 on APs, per-CPU CR3/TSS, user TLB shootdown | (user-space extension of B2, B11) |
| `smp_verification.md` | Test matrix, stress probes, regression | (validation of all) |

## 3. Design Decisions

| # | Decision | Rationale | Rejected Alternative |
|---|---|---|---|
| D1 | Kernel-first phasing | User SMP depends on per-CPU TSS + distributed scheduler | User-first (impossible without kernel infra) |
| D2 | Per-CPU storage via `IA32_GS_BASE` wrmsr | O(1) CPU identification via `%gs:offset`; no LAPIC ID read per access | LAPIC ID lookup (slower), fixed VA per CPU (wasteful) |
| D3 | xchg-based spinlock | Simplest correct primitive on x86; sufficient for v2 | Ticket lock (fairer but more complex), CAS loop (no advantage for short holds) |
| D4 | Global page allocator + spinlock | Minimizes v2 surface; per-CPU page caches deferred | Per-CPU caches (optimization, not correctness) |
| D5 | LAPIC timer on all CPUs (100 Hz) | Each CPU needs independent tick source for preemption | Shared PIT (fires on BSP only; APs starve) |
| D6 | Timer-based preemption | Cooperative across N CPUs is broken: one busy goroutine blocks its core | Keep cooperative (only works on 1 CPU) |
| D7 | TLB shootdown via targeted IPI | Correctness: AP may cache stale PTE after BSP unmap | Broadcast (wasteful for per-process unmaps) |
| D8 | Full IOAPIC programming | Enables per-IRQ CPU routing; prerequisite for distributing HW interrupts | PIC passthrough only (limits IRQs to BSP) |
| D9 | Extend `scripts/tinygo_runtime.patch` | Same mechanism as today (292 lines); no new tooling | Separate patch file (two files to manage), full fork (high maintenance) |
| D10 | maxCPUs = 17 (16 APs + 1 BSP) | Matches `smpMaxAPs` (`src/smp.go:46`) + BSP; ~1 KiB total per-CPU overhead | 4 (limits scalability), 16 (off-by-one with BSP) |

## 4. Phased Work Plan

Design sources: the six sibling `impldoc/smp_*.md` documents.
One git commit per top-level item.

### Phase 0 — Foundation

- [ ] **1. Per-CPU storage (GS base)**
  - `src/stubs.S`: add `wrmsr`/`rdmsr` wrappers.
  - `src/percpu.go` (new): per-CPU data block type, `cpuID()`
    helper, BSP + AP init via `IA32_GS_BASE`.
  - `.bss` reservation: `[maxCPUs]PerCPUBlock`.
  - Verify: `make build` clean; serial prints
    `"BSP cpuID=0"` + `"AP N cpuID=N"` for each AP.

- [ ] **2. Spinlock primitive**
  - `src/stubs.S`: add `spinlockAcquire` (xchg-based),
    `spinlockRelease` (mov + mfence).
  - `src/spinlock.go` (new): Go wrapper type `Spinlock`,
    `Acquire()`/`Release()` with interrupt save/restore.
  - Verify: `make build` clean; unit probe (BSP acquires,
    releases, re-acquires).

- [ ] **3. Per-CPU GDT + TSS**
  - `src/gdt.go`: `perCPUGDT [maxCPUs][gdtEntries]uint64`,
    `perCPUTSS [maxCPUs][tssSize]byte`. BSP copies its GDT
    template; each AP runs `lgdt` + `ltr` on its own.
  - `tssSetRSP0()` rewritten: writes `perCPUTSS[cpuID()]`.
  - Verify: `make build` clean; APs report TSS loaded on
    serial; `test_sendkey.sh 1` PASS.

- [ ] **4. Per-CPU interrupt depth**
  - `src/isr.S`: ISR prologue/epilogue change from
    `incl gooos_in_interrupt_depth(%rip)` to
    `incl %gs:CPU_INTR_DEPTH`.
  - `src/goroutine_irq.go`: read via per-CPU accessor.
  - Verify: `make build` clean; ISR fires on BSP without
    panic; `test_sendkey.sh 1` PASS.

### Phase 1 — Kernel SMP

- [ ] **5. LAPIC register definitions + EOI**
  - `src/smp.go`: add `lapicRegLVTTimer`, `lapicRegTimerDiv`,
    `lapicRegTimerInit`, `lapicRegTimerCurrent`,
    `lapicRegEOI`. Add `lapicSendEOI()`.
  - Verify: `make build` clean.

- [ ] **6. LAPIC timer calibration + per-AP init**
  - `src/lapic_timer.go` (new): calibrate LAPIC timer against
    PIT (10 ms measurement); program periodic mode at 100 Hz
    on BSP + each AP.
  - Register per-CPU timer handler.
  - Verify: serial shows `"LAPIC timer: N ticks/10ms"`;
    each AP prints periodic heartbeat.

- [ ] **7. IOAPIC discovery + redirection table**
  - `src/ioapic.go` (new): parse ACPI MADT type-1 entries for
    IOAPIC base address; map MMIO page; read/write
    IOREGSEL/IOWIN registers; program redirection table
    entries for keyboard (IRQ1) and PIT (IRQ0).
  - Verify: keyboard and timer interrupts still arrive on BSP
    (default routing); serial shows IOAPIC version + max
    redirection entries.

- [ ] **8. TinyGo patch: per-CPU runqueues + systemStack**
  - `runtime/scheduler.go`: replace `var runqueue task.Queue`
    with `var runqueues [maxCPUs]task.Queue`.
  - `internal/task/task_stack_amd64.go`: replace
    `var systemStack uintptr` with `var systemStacks
    [maxCPUs]uintptr`.
  - All `runqueue.Push`/`Pop` sites updated to use
    `runqueues[cpuID()]`.
  - Verify: `make build` clean; BSP scheduler still works
    (single-CPU regression).

- [ ] **9. TinyGo patch: spinlock-protected Queue**
  - `internal/task/queue.go`: replace interrupt-only
    `Disable`/`Restore` with spinlock + interrupt disable.
  - Verify: `make build` clean; channel operations work.

- [ ] **10. TinyGo patch: cross-CPU wakeup in chan.go**
  - `runtime/chan.go`: `resumeRX`/`resumeTX` push to target
    task's home CPU queue; if different CPU, call
    gooos-side IPI hook.
  - Verify: `make build` clean; goroutine on CPU 0 sends to
    chan, receiver on CPU 1 wakes.

- [ ] **11. AP scheduler spawn**
  - `src/smp.go:apEntry`: instead of `sti; hlt` loop, call
    into TinyGo's scheduler loop on this AP.
  - AP loads its per-CPU GDT/TSS, sets GS base, calls
    `scheduler()`.
  - Verify: serial shows goroutines running on multiple CPUs;
    `test_sendkey.sh 1` PASS.

- [ ] **12. Shared data audit fixes**
  - Apply per-variable fixes from `smp_kernel_data_audit.md`:
    atomics for `pitTicks`/`nextPID`/kbd head+tail; spinlocks
    for `procByTask`/`procByPID`/`gInfoByTask`/VGA;
    per-CPU for `lastErrorCode`/`lastFramePtr`.
  - Verify: `make build` clean; all existing harnesses PASS
    under `-smp 4`.

- [ ] **13. IPI send primitive + wakeup vector**
  - `src/ipi.go` (new): `lapicSendIPI(apicID, vector)`;
    wakeup vector handler (LAPIC EOI + return — forces hlt
    exit on target AP).
  - Verify: BSP sends wakeup IPI to each AP; AP serial output
    confirms receipt.

- [ ] **14. Timer-based preemption**
  - LAPIC timer handler: set `wantReschedule` flag; ISR
    epilogue checks flag and yields to scheduler if set.
  - Verify: CPU-bound goroutine yields after ~10 ms; other
    goroutines on same CPU make progress.

### Phase 2 — User SMP

- [ ] **15. Ring-3 goroutines on APs**
  - `gooosOnResume` already swaps CR3 + sets TSS.RSP0; with
    per-CPU TSS (item 3), this works on any CPU.
  - Verify: `test_sendkey.sh 1` under `-smp 4` PASS; shell
    commands execute correctly.

- [ ] **16. TLB shootdown for user page unmaps**
  - `processExit` / `freeProcPML4`: after unmapping user
    pages, send TLB-shootdown IPI to all CPUs that may have
    cached the process's PML4.
  - Verify: rapid spawn/exit cycle under `-smp 4` does not
    page-fault on stale TLB entries.

- [ ] **17. processExit cross-CPU cleanup**
  - `procByTask`/`procByPID` removal under spinlock;
    `unregisterRing3G` under lock; ensure no AP references
    freed Process after exit.
  - Verify: concurrent `ls | wc` pipelines under `-smp 4`;
    `test_pipe_matrix.sh` PASS.

### Phase 3 — Polish

- [ ] **18. README.md + current_impl_doc updates**
  - README.md progress table: update "SMP" row from
    "Done (v1)" to "Done (v2)" with description.
  - `current_impl_doc/scheduler.md`: document per-CPU
    runqueues, work stealing, preemption.
  - `current_impl_doc/memory.md`: document spinlock-protected
    page allocator.

- [ ] **19. Reviewer pass + completeness**
  - Reviewer subagent: CRITICAL=0, MAJOR=0.
  - `grep -rn 'TODO|FIXME|XXX'` over diff: zero new markers.
  - Commit sequence matches 1:1 with TODO items 1-18.

## 5. Dependency DAG

```
Phase 0 (foundation):
  [1] Per-CPU storage (GS base)
       │
       ├──► [2] Spinlock primitive
       │         │
       ├──► [3] Per-CPU GDT + TSS
       │         │
       └──► [4] Per-CPU interrupt depth
                 │
Phase 1 (kernel SMP):
       ┌─────────┘
       │
       ├──► [5] LAPIC register defs ──► [6] LAPIC timer
       │                                      │
       ├──► [7] IOAPIC discovery              │
       │                                      │
       ├──► [8] Per-CPU runqueues ──► [9] Queue spinlock
       │         │                        │
       │         └──► [10] Cross-CPU wakeup in chan.go
       │                    │
       ├──► [11] AP scheduler spawn (depends on 3,4,6,8)
       │
       ├──► [12] Data audit fixes (depends on 2)
       │
       ├──► [13] IPI primitive (depends on 5)
       │         │
       └──► [14] Preemption (depends on 6,11)

Phase 2 (user SMP):
       [15] Ring-3 on APs (depends on 3,11)
       [16] TLB shootdown (depends on 13)
       [17] processExit cleanup (depends on 12,15)

Phase 3 (polish):
       [18] README + docs (depends on all above)
       [19] Reviewer (depends on 18)
```

## 6. Risk Register

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R-fork-divergence | TinyGo upstream changes break our patch | Medium | High | Pin TinyGo version; patch tests in CI |
| R-percpu-overhead | 16 × 4 KiB per-CPU blocks waste memory | Low | Low | 64 KiB total; acceptable on 128+ MiB target |
| R-deadlock | Spinlock ordering violations cause deadlock | Medium | High | Document lock ordering; limit nesting to 2 |
| R-cache-coherency | False sharing on per-CPU arrays | Low | Medium | Align per-CPU blocks to cache-line (64 B) |
| R-nosplit-lock | `gInfoByTask` access from nosplit `gooosOnResume` | High | High | Replace map with fixed-size array indexed by pool slot; see `smp_kernel_data_audit.md §6` |
| R-ioapic-compat | QEMU IOAPIC behavior differs from real hardware | Low | Medium | Test on multiple QEMU versions; document assumptions |
| R-preemption-reentry | Timer ISR fires during scheduler critical section | Medium | High | Disable timer interrupt during scheduler; re-enable after task switch |
| R-tlb-shootdown-perf | IPI storm during rapid process exit | Low | Medium | Batch TLB invalidations; use process-generation counter |

## 7. Open Questions

1. **CPU affinity for user processes**: should a Ring-3 process
   be pinnable to a specific CPU, or always migratable? Current
   recommendation: migratable by default; add affinity field to
   `Process` if a use case arises.

2. **Sleep queue distribution**: keep single global
   `sleepQueue` (BSP-managed, wakeups push to target CPU's
   runqueue via IPI) or distribute per-CPU? Recommendation:
   global for v2 simplicity.

3. **GC mark phase under SMP**: TinyGo's conservative GC scans
   the global range + live stacks. Under SMP, multiple CPUs may
   be in GC simultaneously. Needs stop-the-world or per-CPU GC
   phase coordination. Flag for detailed design during
   implementation.

4. **GC stop-the-world**: TinyGo's conservative GC assumes
   single-threaded execution during mark phase. Under SMP,
   concurrent mutation during GC mark could miss live pointers.
   Needs an IPI-based stop-the-world protocol (halt all APs
   during GC, resume after mark completes) or a "GC runs on
   BSP only with AP pause" approach. The TinyGo patch must add
   this coordination. Detail during implementation of items
   8-11.

5. **sleepQueue / timerQueue synchronization**: TinyGo's
   `sleepQueue` and `timerQueue` (`~/.local/tinygo/src/runtime/
   scheduler.go:29,31`) are global linked lists accessed from
   any CPU under SMP. Protect with a dedicated `sleepLock`
   spinlock in the TinyGo runtime patch. Add to the patch
   surface enumeration in `smp_kernel_scheduler.md §2`.

6. **Lock ordering**: with spinlocks on `procByTask`,
   `gInfoByTask`, VGA, page allocator — what is the canonical
   nesting order? Outermost acquired first:
   1=pageAllocLock, 2=procLock, 3=gInfoLock, 4=vgaLock.
   A function holding lock N must not acquire lock M where
   M < N. Document in `smp_percpu_and_sync.md §4.3`.

## 8. Relationship to Existing Docs

- **`impldoc/deferred_smp_v2.md`**: predecessor sketches for
  items 1-5. Superseded by this design set for implementation
  guidance. Preserved as historical record (per convention in
  `deferred_overview.md §7`).
- **`impldoc/deferred_overview.md`**: items 1-5 from the
  inventory are addressed by this design set. Items 6-16 remain
  independent deferred work.
- **`impldoc/goroutine_design_gc_and_smp.md`**: SMP interaction
  notes from Phase A. Referenced where applicable; not modified.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
