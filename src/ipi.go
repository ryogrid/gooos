// src/ipi.go -- Inter-Processor Interrupt (IPI) support.
//
// Provides lapicSendIPI for targeted IPI delivery and a wakeup
// IPI vector handler that wakes APs from hlt for cross-CPU
// goroutine scheduling.
//
// See impldoc/smp_kernel_lapic_and_ipi.md §6 for the design.

package main

// IPI vector assignments.
const (
	ipiWakeupVector  = 0xFC // wake AP from hlt for scheduling
	ipiPreemptVector = 0xFB // force reschedule on target core (feature 2.1)
)

// lapicSendIPI sends an IPI to the specified APIC ID with the
// given vector. Uses the LAPIC ICR (Interrupt Command Register).
//
// Nosplit because every caller is itself nosplit (pitWakeAPs from
// the PIT ISR, broadcastPreemptIPI from the LAPIC-timer ISR,
// gooosWakeupCPU from the TinyGo scheduler wake path). Without
// this annotation TinyGo may emit a stack-growth check at entry
// and recursively call the Go runtime from ISR context.
//
//go:nosplit
func lapicSendIPI(targetAPICID uint8, vector uint8) {
	// Write destination APIC ID to ICR high (bits 24-31).
	lapicWrite(lapicRegICRH, uint32(targetAPICID)<<24)
	// Write vector + fixed delivery + assert level to ICR low.
	lapicWrite(lapicRegICRL, uint32(vector)|0x00004000)
	// Wait for delivery.
	lapicWaitICR()
}

// handleWakeupIPI is the IPI handler for cross-CPU goroutine
// wakeup. The handler just sends LAPIC EOI — returning from the
// ISR wakes the CPU from hlt, and the scheduler loop checks the
// runqueue naturally.
//
//go:nosplit
func handleWakeupIPI(vector uint64) {
	lapicSendEOI()
}

// gooosWakeupCPU sends an IPI to wake the specified CPU so it
// checks its runqueue. Called from the TinyGo runtime when a
// goroutine is pushed to a remote CPU's queue (cross-CPU
// channel wakeup). Currently a no-op placeholder — will be
// wired to lapicSendIPI once per-CPU APIC ID tracking is in
// place.
//
//go:nosplit
//go:linkname gooosWakeupCPU gooosWakeupCPU
func gooosWakeupCPU(cpuIdx uint32) {
	if cpuIdx >= maxCPUs {
		return
	}
	apicID := perCPUBlocks[cpuIdx].APICID
	if apicID == perCPUBlocks[cpuID()].APICID {
		return // don't IPI self
	}
	lapicSendIPI(uint8(apicID), ipiWakeupVector)
}

// broadcastPreemptIPI sends the preempt IPI (vector 0xFB) to every
// online core other than the caller. Called from handleLAPICTimer on
// the BSP's 100 Hz tick when preemptEnabled == true (feature 2.1).
// Each AP's handlePreemptIPI then decides per-CPU whether to
// Gosched() based on PreemptDisable / InterruptDepth / SyscallDepth.
//
//go:nosplit
func broadcastPreemptIPI() {
	n := uint32(numCoresOnline)
	if n == 0 {
		n = 1
	}
	me := cpuID()
	meAPIC := perCPUBlocks[me].APICID
	for i := uint32(0); i < n; i++ {
		if i == me {
			continue
		}
		apicID := perCPUBlocks[i].APICID
		if apicID == meAPIC {
			continue
		}
		lapicSendIPI(uint8(apicID), ipiPreemptVector)
	}
}
