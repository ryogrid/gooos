// src/lapic_timer.go -- LAPIC timer calibration and per-CPU init.
//
// Calibrates the LAPIC timer against the PIT (already running at
// 100 Hz) on the BSP, then programs each CPU's LAPIC timer in
// periodic mode at the calibrated interval (~100 Hz).
//
// See impldoc/smp_kernel_lapic_and_ipi.md §3-4 for the design.

package main

// lapicTimerVector is the interrupt vector for the per-CPU LAPIC timer.
const lapicTimerVector = 0xFE

// lapicCalibratedInitCnt holds the calibrated initial count for
// 100 Hz (10 ms period). Set by lapicTimerCalibrate on the BSP,
// read by all APs.
var lapicCalibratedInitCnt uint32

// lapicTimerCalibrate measures the LAPIC timer decrement rate
// using the PIT (already running at 100 Hz) as a reference.
// Must be called on the BSP after pitInit() and smpInit()
// (LAPIC MMIO must be mapped, interrupts must be enabled).
func lapicTimerCalibrate() {
	// Divide configuration: value 0x03 = divide by 16.
	lapicWrite(lapicRegTimerDivCfg, 0x03)

	// Mask the LAPIC timer during calibration so it doesn't fire.
	// One-shot mode, masked, vector 0xFE.
	lapicWrite(lapicRegLVTTimer, 0x00010000|uint32(lapicTimerVector))

	// Start the LAPIC timer at max count.
	lapicWrite(lapicRegTimerInitCnt, 0xFFFFFFFF)

	// Wait for exactly one PIT tick (~10 ms).
	// Use hlt() between iterations so PIT IRQ can fire.
	t0 := pitTicks
	for pitTicks == t0 {
		hlt()
	}

	// Read how far the LAPIC timer counted down.
	current := lapicRead(lapicRegTimerCurrCnt)
	elapsed := uint32(0xFFFFFFFF) - current

	// Stop the calibration timer.
	lapicWrite(lapicRegTimerInitCnt, 0)

	lapicCalibratedInitCnt = elapsed

	serialPrint("LAPIC timer: ")
	serialPrint(utoa(uint64(elapsed)))
	serialPrintln(" ticks/10ms")
}

// lapicTimerInit programs this CPU's LAPIC timer in periodic
// mode using the BSP-calibrated initial count. Must be called
// after lapicTimerCalibrate has run on the BSP.
func lapicTimerInit() {
	// Divide configuration: same as calibration (divide by 16).
	lapicWrite(lapicRegTimerDivCfg, 0x03)

	// LVT Timer: periodic mode (bit 17), vector 0xFE, unmasked.
	lapicWrite(lapicRegLVTTimer, 0x00020000|uint32(lapicTimerVector))

	// Initial count: calibrated ticks per 10 ms = 100 Hz.
	lapicWrite(lapicRegTimerInitCnt, lapicCalibratedInitCnt)
}

// handleLAPICTimer is the per-CPU LAPIC timer handler (vector 0xFE).
// Sets the wantReschedule flag so the scheduler yields on the next
// opportunity, and sends LAPIC EOI. The actual preemption happens
// when the CPU returns from hlt — the scheduler loop checks the
// local runqueue and steals from peers.
//
//go:nosplit
func handleLAPICTimer(vector uint64) {
	idx := cpuID()
	perCPUBlocks[idx].WantReschedule = 1
	if preemptEnabled {
		// Feature 2.1: broadcast preempt IPI (vector 0xFB) to every
		// online AP. Each AP's handlePreemptIPI decides per-CPU
		// whether to Gosched based on its own PreemptDisable /
		// InterruptDepth / SyscallDepth state. BSP itself is not
		// preempted by this tick (known limitation; BSP-pinned long-
		// running goroutines must cooperatively yield).
		broadcastPreemptIPI()
	}
	lapicSendEOI()
}
