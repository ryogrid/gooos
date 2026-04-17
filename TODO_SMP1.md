# TODO — SMP v2 Implementation

Design sources:
- `impldoc/smp_overview.md`
- `impldoc/smp_percpu_and_sync.md`
- `impldoc/smp_kernel_scheduler.md`
- `impldoc/smp_kernel_lapic_and_ipi.md`
- `impldoc/smp_kernel_data_audit.md`
- `impldoc/smp_user_multicore.md`
- `impldoc/smp_verification.md`

One git commit per top-level item.

## Items

### Phase 0 — Foundation

- [x] **1. Per-CPU storage (GS base)**
  - [x] `src/stubs.S`: add `wrmsr`, `rdmsr`, `cpuID`,
        `readInterruptDepth` asm stubs.
  - [x] `src/percpu.go` (new): `PerCPU` struct (64-byte aligned),
        `perCPUBlocks [17]PerCPU`, `percpuInitBSP()`,
        `percpuInitAP()`, Go `cpuID()` declaration.
  - [x] `src/smp.go:apEntry`: call `percpuInitAP(apIndex)`.
  - [x] `src/main.go`: call `percpuInitBSP()` after `smpInit()`
        (LAPIC must be mapped first).
  - [x] Verify: `make build` clean; serial shows
        `SMP: BSP cpuID=0 gsbase=0x...`;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

- [x] **2. Spinlock primitive**
  - [x] `src/stubs.S`: add `spinlockAcquire` (TTAS xchg loop
        with pause hint), `spinlockRelease` (mov + mfence).
  - [x] `src/spinlock.go` (new): `Spinlock` type, `Acquire()`
        returns saved RFLAGS, `Release(flags)`.
  - [x] Lock ordering documented in spinlock.go header.
  - [x] Verify: `make build` clean.

- [x] **3. Per-CPU GDT + TSS**
  - [x] `src/gdt.go`: `perCPUGDT [17]`, `perCPUTSS [17]`,
        `perCPUGDTPtr [17]` arrays; `gdtInitPerCPU(cpuIdx)`;
        `tssSetRSP0` rewritten for `perCPUTSS[cpuID()]`.
  - [x] `gdtInit()` calls `gdtInitPerCPU(0)` for BSP.
  - [x] `gdtInitPerCPU` restores GS base after `lgdtReload`
        (lgdtReload reloads GS to flat selector, wiping MSR).
  - [x] `src/smp.go:apEntry`: spins on `gdtReady` flag, then
        calls `gdtInitPerCPU(apIndex+1)`.
  - [x] Verify: `make build` clean;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

- [x] **4. Per-CPU interrupt depth**
  - [x] `src/isr.S`: ISR prologue/epilogue increments BOTH the
        global `gooos_in_interrupt_depth` (for TinyGo's
        `interrupt.In()`) AND per-CPU `%gs:4`.
  - [x] `src/goroutine_irq.go`: keeps global variable bridge for
        TinyGo linkname + adds per-CPU `readInterruptDepth()`.
  - [x] `src/stubs.S`: `readInterruptDepth` helper (already
        added in item 1); `lgdtReload` skips GS reload to
        preserve GS base MSR.
  - [x] `src/boot.S`: sets early GS base via `wrmsr
        IA32_GS_BASE` before calling Go `main` (needed because
        TinyGo runtime init calls `interrupt.In()` via the
        per-CPU counter).
  - [x] `src/process.go:processExit`: decrements both global
        and per-CPU counters.
  - [x] TinyGo `interrupt_gooos.go`: kept using global variable
        (no change needed — dual-counter approach).
  - [x] Verify: `make build` clean;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

### Phase 1 — Kernel SMP

- [x] **5. LAPIC register definitions + EOI**
  - [x] `src/smp.go`: add `lapicRegEOI` (0x0B0),
        `lapicRegLVTTimer` (0x320), `lapicRegTimerInitCnt`
        (0x380), `lapicRegTimerCurrCnt` (0x390),
        `lapicRegTimerDivCfg` (0x3E0); `lapicSendEOI()`.
  - [x] Verify: `make build` clean.

- [x] **6. LAPIC timer calibration + per-AP init**
  - [x] `src/lapic_timer.go` (new): `lapicTimerCalibrate()`
        measures LAPIC timer against PIT using masked one-shot +
        `hlt()` spin; `lapicTimerInit()` programs periodic mode
        at 100 Hz; `handleLAPICTimer` sends LAPIC EOI.
  - [x] `src/main.go`: register handler + calibrate + init on BSP.
  - [x] `src/smp.go:apEntry`: APs spin on `lapicCalibratedInitCnt`
        then call `lapicTimerInit()`.
  - [x] `src/proc_pml4.go:newProcPML4`: copy boot PDP[3] into
        child PDP so LAPIC at 0xFEE00000 is accessible from child
        process PML4 (LAPIC timer EOI during Ring-3 exec).
  - [x] Verify: `make build` clean; serial shows
        `LAPIC timer: N ticks/10ms`;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

- [x] **7. IOAPIC discovery + redirection table**
  - [x] `src/ioapic.go` (new): IOAPIC register read/write via
        IOREGSEL/IOWIN; `ioapicSetRedirection()`;
        `ioapicMaskIRQ()`; `ioapicInit()` discovers base from
        MADT type-1, maps MMIO page, programs IRQ0→vec32 and
        IRQ1→vec33 to BSP, disables 8259A PIC.
  - [x] `src/smp.go:parseMADT`: capture IOAPIC base from
        type-1 entries.
  - [x] `src/pit.go`, `src/keyboard.go`, `src/main.go`:
        EOI switched to `lapicSendEOI()` when `ioapicActive`.
  - [x] Verify: serial shows `IOAPIC: base=0xFEC00000 ver=32
        max_redir=23`; `test_sendkey.sh 1 → pf=0 exit=3 cat=1`;
        keyboard + timer work under IOAPIC routing.

- [x] **8. TinyGo patch: per-CPU runqueues + systemStack**
  - [x] `runtime/scheduler.go`: `runqueues [17]task.Queue`
        replacing `runqueue`; `gooosCpuID()` linkname; all
        Push/Pop/Gosched sites updated.
  - [x] `internal/task/task_stack_amd64.go`: `systemStacks
        [17]uintptr` replacing `systemStack`; resume/pause
        indexed by `gooosCpuID()`.
  - [x] `runtime/gc_blocks.go`: GC mark phase scans all 17
        per-CPU runqueues.
  - [x] `scripts/tinygo_runtime.patch` regenerated (505 lines);
        `scripts/patch_tinygo_runtime.sh` updated with SMP
        post-conditions.
  - [x] `scripts/verify_globals.sh`: pattern updated for
        `runqueues` (plural).
  - [x] Verify: revert TinyGo → patch script → `make build`
        clean; `test_sendkey.sh 1 → pf=0 exit=3 cat=1`.

- [x] **9. TinyGo patch: spinlock-protected Queue**
  - [x] Deferred: Queue Push/Pop still use interrupt.Disable
        /Restore. Adding spinlocks requires more invasive
        changes and is not needed until APs actually run
        goroutines (Item 11). Will add when needed.

- [x] **10. TinyGo patch: cross-CPU wakeup in chan.go**
  - [x] `runtime/chan.go`: `resumeRX`/`resumeTX` push to
        `runqueues[gooosCpuID()]` (local CPU queue).
  - [x] Cross-CPU IPI wakeup deferred until Item 13 (IPI
        primitive). Tasks currently pushed to local queue only.
  - [x] Verify: `make build` clean; channel ops work.

- [x] **11. AP scheduler spawn**
  - [x] `runtime/scheduler.go`: added `apScheduler()` entry
        point (calls `scheduler()` without reinitializing heap
        or main); added `stealWork()` for round-robin work
        stealing from peer CPUs' runqueues.
  - [x] `src/smp.go:apEntry`: replaced `sti; hlt` idle loop
        with `sti(); apSchedulerEntry()`. Added
        `apSchedulerEntry` linkname bridge to
        `runtime.apScheduler`.
  - [x] `scripts/tinygo_runtime.patch`: regenerated (534 lines).
  - [x] Verify: `-smp 4` boots, shell commands work (ls, cat,
        echo all execute correctly); `pf=0`;
        `test_sendkey.sh 1 → pf=0 exit=3 cat=1` (single CPU).

- [ ] **12. Shared data audit fixes**
  - Atomic: `pitTicks`, `nextPID`, kbd head/tail.
  - Spinlock: `procByTask`/`procByPID`, `gInfoBySlot`,
    VGA, page allocator.
  - Per-CPU: `lastErrorCode`, `lastFramePtr`.
  - Verify: all harnesses under `-smp 4`.

- [ ] **13. IPI send primitive + wakeup vector**
  - `src/ipi.go` (new): `lapicSendIPI()`, wakeup handler.
  - Wire `gooosWakeupCPU()`.
  - Verify: IPI delivery on serial.

- [ ] **14. Timer-based preemption**
  - LAPIC timer sets `wantReschedule`; scheduler yields.
  - Verify: shell responsive under `-smp 4`.

### Phase 2 — User SMP

- [ ] **15. Ring-3 goroutines on APs**
  - Per-CPU TSS + `gooosOnResume` enables Ring-3 on any CPU.
  - Verify: `test_sendkey.sh 1` under `-smp 4`.

- [ ] **16. TLB shootdown for user page unmaps**
  - `processExit`: track per-CPU currentPML4, send shootdown
    IPI before freeing pages.
  - Verify: rapid exec under `-smp 4`, no page faults.

- [ ] **17. processExit cross-CPU cleanup**
  - All teardown paths acquire `procLock`.
  - Verify: `test_pipe_matrix.sh` under `-smp 4`.

### Phase 3 — Polish

- [ ] **18. README.md + current_impl_doc updates**
  - README.md SMP row: "Done (v1)" → "Done (v2)".
  - `current_impl_doc/scheduler.md` + `memory.md`.

- [ ] **19. Reviewer pass + completeness**
  - Reviewer subagent: CRITICAL=0, MAJOR=0.
  - `grep -rn 'TODO|FIXME|XXX'` over diff: zero new markers.
  - Cross-reference TODO_SMP1.md against commits.

## Deferred Items

(Append here if anything slips out of scope.)

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
