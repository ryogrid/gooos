# Boot and Kernel Initialization

## Entry Chain

### Assembly bootstrap (`src/boot.S`)

- `_start` contains Multiboot1 header and is placed early in image.
- Bootstrap creates initial paging hierarchy:
  - `pml4`
  - `pdp`
  - `pd`
- `pd[0..511]` is populated with 2 MiB huge-page identity mappings for `[0, 1 GiB)`.
- `CR3` is loaded with `pml4`; `CR4.PAE` and `EFER.LME` are enabled; paging is turned on via `CR0.PG`.
- A temporary GDT (`gdt64`) is loaded and far jump enters `long_mode_start`.
- `IA32_GS_BASE` is initialized to `early_percpu_bsp` before entering Go runtime, so ISR `%gs:4` depth access is valid immediately.
- Control then calls TinyGo runtime entry symbol `main` with `(argc=0, argv=nil)`.

## Kernel `main()` Sequencing (`src/main.go`)

The initialization sequence is linear and intentionally staged around interrupt safety and runtime dependencies.

1. Console and interrupt substrate:
   - `serialInit()`
   - `idtInit()`
   - `registerHandler(0, handleDivisionError)`
   - `registerHandler(14, handlePageFault)`
   - `picRemap()`
   - default IRQ handlers for vectors `32..47`
2. Per-CPU bootstrap setup:
   - `percpuInitBSPEarly()` before interrupts are enabled.
3. Timer/input setup:
   - `pitInit()` + `registerHandler(32, handleTimer)`
   - `keyboardInit()` + `registerHandler(33, handleKeyboard)`
4. `sti()` enables maskable interrupts.
5. Timer-wheel service starts with `afterTicksInit()`.
6. Memory and VM setup:
   - `vmInit()`
   - `ring3StackPoolInit()`
   - `captureBootPML4()`
7. SMP and privilege setup:
   - `smpInit()`
   - `percpuInitBSPLate()`
   - `gdtInit()` and then `gdtReady = 1`
8. IPI/timer handlers are registered before LAPIC timer is started:
   - `registerHandler(ipiWakeupVector, handleWakeupIPI)`
   - `registerHandler(ipiPreemptVector, handlePreemptIPI)`
   - `registerHandler(lapicTimerVector, handleLAPICTimer)`
   - `lapicTimerCalibrate()` and `lapicTimerInit()`
9. Networking stack setup (if NIC present):
   - `pciInit()`
   - `e1000Init()`
   - IRQ registration for e1000
   - `e1000EnableInterrupts()`
   - `netInit()`
10. Services and userspace artifacts:
   - `go fsTask()`
   - `go keyboardPump()`
   - user ELF blobs written into in-memory FS (`sh.elf`, `ls.elf`, `tcpcli.elf`, etc.)
11. SMP scheduler release:
   - `bspBootDone = 1`
12. Optional preempt probes gated by constants in `src/preempt_config.go`.
13. Final transfer to userspace shell path through `setupUserspace()`.

## Boot-Time Gates

- `gdtReady` (`src/smp.go`): APs spin until BSP publishes GDT template readiness.
- `bspBootDone` (`src/smp.go`): APs spin until BSP finishes service setup and filesystem population.

## Boot-Phase Invariants

1. `percpuInitBSPEarly()` must execute before `sti()`, otherwise ISR depth accounting via `%gs:4` is invalid.
2. LAPIC-related handlers must be registered before LAPIC timer can emit vectors.
3. AP entry to scheduler is blocked until `bspBootDone` to avoid early execution against partially initialized shared state.
4. `captureBootPML4()` must run before per-process PML4 cleanup paths can safely restore CR3 on process teardown.

## Notable Boot-Surface Risk Points

- AP LAPIC timer init in `apEntry` is intentionally disabled in current state (commented call to `lapicTimerInit()`).
- Bring-up includes multiple asynchronous goroutines before userspace starts; ordering relies on `afterTicks` availability and pre-registration of handlers.
