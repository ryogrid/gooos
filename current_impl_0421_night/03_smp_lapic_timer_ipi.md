# SMP Bring-Up, LAPIC Timer, and IPI Paths

## SMP Boot (`src/smp.go`)

### Core data and gates

- `numCoresOnline` starts at `1` (BSP only), updated after AP bring-up.
- `gdtReady` and `bspBootDone` are global synchronization gates for AP progress.
- AP stacks for trampoline handoff live in `apStacks[smpMaxAPs]`.

### AP startup sequence

`smpInit()` performs:

1. LAPIC MMIO mapping (`mapPage(lapicBase, lapicBase, pagePresent|pageWrite|pagePCD|pagePWT)`).
2. LAPIC software enable and LINT programming.
3. Trampoline copy to `0x8000` physical (`trampPhys`).
4. INIT-SIPI-SIPI broadcast using LAPIC ICR.
5. AP-online wait loop via trampoline counter (`trampOffCounter`).
6. `numCoresOnline` publish.

`apEntry(apIndex)` performs:

1. `percpuInitAP(apIndex)`
2. spin on `gdtReady`
3. `gdtInitPerCPU(int(apIndex)+1)`
4. `idtLoadAP()`
5. LAPIC enable on AP + APICID latch (`percpuLatchAPICIDCurrent()`)
6. spin on `lapicCalibratedInitCnt`
7. AP LAPIC timer init is currently deferred (call is intentionally disabled)
8. spin on `bspBootDone`
9. APICID relatch
10. `sti()` and `apSchedulerEntry()`

## Per-CPU State Model (`src/percpu.go`)

`PerCPU` contains ABI-critical fields:

- `CPUIndex` (`%gs:0`)
- `InterruptDepth` (`%gs:4`)
- `CurrentPoolIdx` (`%gs:40`)
- `SyscallDepth` (`%gs:44`)
- `PreemptDisable` (`%gs:48`)

APICID latching logic includes bounded retry when AP reads transient zero APICID.

## LAPIC Timer Flow (`src/lapic_timer.go`)

- Timer vector: `0xFE`.
- `lapicTimerCalibrate()` computes ticks per PIT interval (10 ms).
- `lapicTimerInit()` configures periodic 100 Hz timer.

`handleLAPICTimer()` current behavior:

1. Sets `perCPUBlocks[idx].WantReschedule = 1` for current CPU.
2. Preempt features run only on BSP (`idx == 0`) after `bspBootDone != 0`.
3. Optional 2.3 probe warmup gate (`runSMPShellPreemptProbe` + `preemptProbeWarmupTicks`).
4. Calls `maybeSignalUserPreempt(i)` for each online CPU.
5. Calls `broadcastPreemptIPI()` for kernel preemption fanout.
6. If interrupted frame is Ring 3 on BSP, attempts `maybeDeliverSignal(frame)` fast path.
7. Sends LAPIC EOI.

## IPI Paths (`src/ipi.go`)

- Wake vector: `0xFC` (`ipiWakeupVector`)
- Preempt vector: `0xFB` (`ipiPreemptVector`)

Key functions:

- `lapicSendIPI(targetAPICID, vector)`
- `lapicBroadcastIPI(vector, includeSelf)`
- `gooosWakeupCPU(cpuIdx)` (runtime wake integration)
- `broadcastPreemptIPI()` (BSP preempt fanout)

`broadcastPreemptIPI()` iterates `numCoresOnline`, skips self, skips APs with APICID=0 (except BSP), sends targeted preempt IPI; falls back to shorthand broadcast if no target was sent.

## SMP Invariants

1. APs must not enter scheduler before BSP completes initialization (`bspBootDone`).
2. `lapicWaitICR()` bounded spin avoids dead ISR loops on delivery-stuck conditions.
3. All IPI/LAPIC ISR handlers must issue LAPIC EOI.
4. `numCoresOnline` is the runtime-visible upper bound for preempt and wake fanout.

## Known Unstable/Deferred SMP Surfaces

- AP LAPIC timer enable remains deferred in `apEntry` comments.
- Preempt behavior in shell preempt probe mode (`runSMPShellPreemptProbe`) is under active diagnostic evolution.
- APICID transient-zero behavior exists at boot and is mitigated with relatch + retry; not guaranteed eliminated in all boot timings.
