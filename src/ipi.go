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

// lapicBroadcastIPI sends a fixed-delivery IPI using destination
// shorthand, avoiding per-CPU APIC-ID targeting.
//
// includeSelf=true  -> all including self   (dest shorthand 0b10)
// includeSelf=false -> all excluding self   (dest shorthand 0b11)
//
//go:nosplit
func lapicBroadcastIPI(vector uint8, includeSelf bool) {
	// Destination shorthand occupies ICR low bits 18-19.
	var shorthand uint32 = 0x000C0000 // all excluding self
	if includeSelf {
		shorthand = 0x00080000 // all including self
	}
	// With destination shorthand, ICR high destination field is ignored.
	lapicWrite(lapicRegICRH, 0)
	lapicWrite(lapicRegICRL, uint32(vector)|0x00004000|shorthand)
	lapicWaitICR()
}

// lapicSendSelfIPI queues a fixed-delivery self IPI using destination
// shorthand "self" (bits 19:18 = 01). Used from timer ISR context to
// request a local preempt interrupt without waiting in ICR polling.
//
//go:nosplit
func lapicSendSelfIPI(vector uint8) {
	const selfShorthand uint32 = 0x00040000
	lapicWrite(lapicRegICRH, 0)
	lapicWrite(lapicRegICRL, uint32(vector)|0x00004000|selfShorthand)
}

// wakeFirstSeen[cpu] is flipped to 1 the first time handleWakeupIPI
// enters on that CPU. Exposed via netDiag as a 4-bit bitmap. Plain
// [maxCPUs]uint32 — NOT a counter, NOT a u64: the 082051f attempt
// to increment a uint64 counter here hung the user's environment,
// so this diagnostic uses only a single "seen" flag per CPU with
// word-sized stores.
var wakeFirstSeen [maxCPUs]uint32

// preemptTargetSnapshot stores the latest BSP-selected APIC-ID fanout
// set for vector 0xFB delivery. BSP timer path updates this every tick
// in broadcastPreemptIPI; used for deterministic target selection.
var preemptTargetSnapshot [maxCPUs]uint8
var preemptTargetSnapshotN uint32

// handleWakeupIPI is the IPI handler for cross-CPU goroutine
// wakeup. The handler just sends LAPIC EOI — returning from the
// ISR wakes the CPU from hlt, and the scheduler loop checks the
// runqueue naturally.
//
//go:nosplit
func handleWakeupIPI(vector uint64) {
	c := cpuID()
	if c < maxCPUs && wakeFirstSeen[c] == 0 {
		wakeFirstSeen[c] = 1
	}
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
	if cpuIdx == cpuID() {
		return // don't IPI self
	}
	apicID := perCPUBlocks[cpuIdx].APICID
	// AP slots start as APICID=0 until AP bring-up latches the real ID.
	// cpuIdx=0 is BSP and may legitimately have APICID 0.
	if cpuIdx != 0 && apicID == 0 {
		return
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
	snapN := uint32(0)
	for i := uint32(0); i < n; i++ {
		if i == me {
			continue
		}
		apicID := perCPUBlocks[i].APICID
		if i != 0 && apicID == 0 {
			continue
		}
		if snapN < maxCPUs {
			preemptTargetSnapshot[snapN] = uint8(apicID)
			snapN++
		}
	}
	preemptTargetSnapshotN = snapN
	for i := uint32(0); i < snapN; i++ {
		lapicSendIPI(preemptTargetSnapshot[i], ipiPreemptVector)
	}
	lapicSendSelfIPI(ipiPreemptVector)
}
