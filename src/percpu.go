// src/percpu.go -- Per-CPU storage for SMP v2.
//
// Each CPU writes its per-CPU data block address into IA32_GS_BASE
// at boot time. All per-CPU reads use %gs:offset addressing, giving
// O(1) access without LAPIC ID lookups.
//
// See impldoc/smp_percpu_and_sync.md §1 for the full design.

package main

import "unsafe"

// maxCPUs is smpMaxAPs (16 APs) + 1 BSP = 17.
const maxCPUs = 17

// MSR number for GS base.
const ia32GSBASE = 0xC0000101

// PerCPU is the per-CPU data block. Each CPU's GS base points to
// its own instance. Fields accessed from assembly must have stable
// byte offsets documented below.
type PerCPU struct {
	CPUIndex       uint32   // offset 0:  CPU index (0 = BSP)
	InterruptDepth uint32   // offset 4:  ISR nesting counter (%gs:4)
	SystemStack    uintptr  // offset 8:  scheduler stack for TinyGo
	TSSPtr         uintptr  // offset 16: pointer to this CPU's TSS
	APICID         uint32   // offset 24: LAPIC APIC ID
	WantReschedule uint32   // offset 28: timer preemption flag
	CurrentPML4    uintptr  // offset 32: CR3 of current goroutine
	CurrentPoolIdx int32    // offset 40: ring3 pool slot (-1 if kernel)
	SyscallDepth   uint32   // offset 44: syscall-dispatch depth (%gs:44)
	PreemptDisable uint32   // offset 48: spinlock-held / no-preempt nesting depth (%gs:48)
	_pad           [12]byte // pad to 64-byte cache line boundary
}

// Assembly-visible byte offsets (must match struct layout above).
const (
	pcpuOffCPUIndex       = 0
	pcpuOffInterruptDepth = 4
	pcpuOffSystemStack    = 8
	pcpuOffTSSPtr         = 16
	pcpuOffAPICID         = 24
	pcpuOffWantReschedule = 28
	pcpuOffCurrentPML4    = 32
	pcpuOffCurrentPoolIdx = 40
	pcpuOffSyscallDepth   = 44
	pcpuOffPreemptDisable = 48
)

// perCPUBlocks is the .bss-resident array. Each entry is padded to
// 64 bytes (cache line) to avoid false sharing.
var perCPUBlocks [maxCPUs]PerCPU

// ---- P03 Sleep-audit counters (gated by runSleepAudit) ----
//
// Kept as separate arrays (not embedded in PerCPU) so the
// ABI-critical struct offsets above are not disturbed. Plain
// uint64 increments are acceptable for diagnostics — a racey
// increment only biases the counter slightly. See
// current_impl_2026_04_24/fix_plan_deferred_1_5/03_sleep_cross_cpu_channel_wakeup_audit.md.

var SchedTasksPushed [maxCPUs]uint64 // bumped by gooosNotePush
var SchedPopOk [maxCPUs]uint64       // bumped by gooosNotePop when ok==true
var SchedPopNil [maxCPUs]uint64      // bumped by gooosNotePop when ok==false
var lapicICRTimeouts uint64          // bumped by src/smp.go:lapicWaitICR on timeout

//go:linkname gooosNotePush gooosNotePush
func gooosNotePush(cpuIdx uint32) {
	if !runSleepAudit {
		return
	}
	if cpuIdx < maxCPUs {
		SchedTasksPushed[cpuIdx]++
	}
}

//go:linkname gooosNotePop gooosNotePop
func gooosNotePop(cpuIdx uint32, ok bool) {
	if !runSleepAudit {
		return
	}
	if cpuIdx >= maxCPUs {
		return
	}
	if ok {
		SchedPopOk[cpuIdx]++
	} else {
		SchedPopNil[cpuIdx]++
	}
}

// wrmsr writes a 64-bit value to the specified MSR.
// Implemented in stubs.S.
//
//go:linkname wrmsr wrmsr
func wrmsr(msr uint32, val uint64)

// rdmsr reads a 64-bit value from the specified MSR.
// Implemented in stubs.S.
//
//go:linkname rdmsr rdmsr
func rdmsr(msr uint32) uint64

// cpuID returns the current CPU index (0 = BSP).
// Reads from %gs:0 — must be called after percpuInit.
// Implemented in stubs.S.
//
//go:nosplit
//go:linkname cpuID cpuID
func cpuID() uint32

// gooosPause executes the x86 PAUSE instruction as a hint for
// spin-wait loops. Reduces power consumption and bus contention.
// Implemented in stubs.S.
//
//go:nosplit
//go:linkname gooosPause gooosPause
func gooosPause()

// percpuInitBSPEarly sets the BSP's GS base to point at its per-CPU
// data block. Must be called BEFORE interrupts are enabled — the ISR
// prologue uses %gs:4 to increment the per-CPU interrupt depth counter.
// The APIC ID is filled in later by percpuInitBSPLate (after the LAPIC
// MMIO page is mapped).
func percpuInitBSPEarly() {
	perCPUBlocks[0].CPUIndex = 0
	perCPUBlocks[0].CurrentPoolIdx = -1
	addr := uint64(uintptr(unsafe.Pointer(&perCPUBlocks[0])))
	wrmsr(ia32GSBASE, addr)
}

// percpuInitBSPLate fills in the BSP's APIC ID (requires LAPIC
// MMIO to be mapped by smpInit) and logs the GS base.
func percpuInitBSPLate() {
	perCPUBlocks[0].APICID = lapicRead(lapicRegID) >> 24
	addr := uint64(uintptr(unsafe.Pointer(&perCPUBlocks[0])))
	serialPrintln("SMP: BSP cpuID=0 gsbase=0x" + hextoa(addr))
}

// percpuInitAP initializes per-CPU storage for an AP.
// Called from apEntry before any other per-CPU work.
//
// APICID is intentionally not latched here: APs call this before
// their LAPIC software-enable write in apEntry, and reading
// lapicRegID too early can yield 0 on some boots. The AP latches
// APICID after LAPIC enable via percpuLatchAPICIDCurrent.
//
//go:nosplit
func percpuInitAP(apIndex uint64) {
	idx := apIndex + 1 // BSP is 0; first AP is 1
	perCPUBlocks[idx].CPUIndex = uint32(idx)
	perCPUBlocks[idx].APICID = 0
	perCPUBlocks[idx].CurrentPoolIdx = -1
	addr := uint64(uintptr(unsafe.Pointer(&perCPUBlocks[idx])))
	wrmsr(ia32GSBASE, addr)
}

// percpuLatchAPICIDCurrent captures the current CPU's LAPIC ID into
// its per-CPU block. Called on APs only after LAPIC software-enable
// has been written in apEntry.
//
//go:nosplit
func percpuLatchAPICIDCurrent() {
	idx := cpuID()
	if idx >= maxCPUs {
		return
	}
	id := lapicRead(lapicRegID) >> 24
	if idx != 0 && id == 0 {
		// Some boots briefly report AP LAPIC ID as 0 immediately after
		// software-enable. Retry a bounded number of times.
		for i := 0; i < 1024; i++ {
			gooosPause()
			id = lapicRead(lapicRegID) >> 24
			if id != 0 {
				break
			}
		}
	}
	perCPUBlocks[idx].APICID = id
}
