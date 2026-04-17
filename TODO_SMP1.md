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

- [ ] **3. Per-CPU GDT + TSS**
  - `src/gdt.go`: `perCPUGDT`, `perCPUTSS` arrays,
    `gdtInitPerCPU(cpuIdx)`, `tssSetRSP0` rewrite.
  - `src/smp.go:apEntry`: call `gdtInitPerCPU(apIndex+1)`.
  - Verify: `make build` clean; `test_sendkey.sh 1` PASS.

- [ ] **4. Per-CPU interrupt depth**
  - `src/isr.S`: `%gs:4` instead of
    `gooos_in_interrupt_depth(%rip)`.
  - `src/goroutine_irq.go`: per-CPU accessor.
  - `src/stubs.S`: `readInterruptDepth` helper.
  - TinyGo `interrupt_gooos.go` patch update if needed.
  - Verify: `make build` clean; `test_sendkey.sh 1` PASS.

### Phase 1 — Kernel SMP

- [ ] **5. LAPIC register definitions + EOI**
  - `src/smp.go`: add missing LAPIC register constants +
    `lapicSendEOI()`.
  - Verify: `make build` clean.

- [ ] **6. LAPIC timer calibration + per-AP init**
  - `src/lapic_timer.go` (new): calibrate against PIT,
    program periodic 100 Hz, per-CPU timer handler.
  - Verify: serial shows calibration + AP heartbeats.

- [ ] **7. IOAPIC discovery + redirection table**
  - `src/ioapic.go` (new): MADT type-1 parsing, MMIO,
    redirection table (IRQ0→vec32, IRQ1→vec33), PIC mask.
  - EOI switch to LAPIC in timer + keyboard handlers.
  - Verify: keyboard + timer still work under IOAPIC.

- [ ] **8. TinyGo patch: per-CPU runqueues + systemStack**
  - `runtime/scheduler.go`: `runqueues [17]task.Queue`.
  - `task_stack_amd64.go`: `systemStacks [17]uintptr`.
  - Update `scripts/tinygo_runtime.patch` + patch script.
  - Verify: revert tree → patch → `make build` clean.

- [ ] **9. TinyGo patch: spinlock-protected Queue**
  - `internal/task/queue.go`: spinlock in Push/Pop,
    new `PopTail()`.
  - Verify: `make build` clean; channel ops work.

- [ ] **10. TinyGo patch: cross-CPU wakeup in chan.go**
  - `runtime/chan.go`: `resumeRX`/`resumeTX` cross-CPU hook.
  - gooos-side `gooosWakeupCPU()` linkname hook.
  - Verify: `make build` clean.

- [ ] **11. AP scheduler spawn**
  - `src/smp.go:apEntry` reworked → scheduler loop.
  - Goroutines start running on APs.
  - Verify: goroutines on multiple CPUs; `-smp 4` boots.

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
