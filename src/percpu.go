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

// sleepAuditISRBuf is the scratch area sleepAuditISRDump formats
// into. Separate from panicHexBuf because the ISR-side audit runs
// periodically and could otherwise race with panic formatting.
// Only written from handleTimer on BSP, so the single-buffer
// discipline is safe.
var sleepAuditISRBuf [192]byte

// sleepAuditISRDump emits a single-line audit snapshot directly
// from the PIT ISR, bypassing afterTicks / the scheduler / any
// goroutine runtime. The netDiag-based sleepAuditDump at
// src/net.go:237 is fine when the scheduler is healthy, but the
// F1 flake IS a scheduler-side hang — the netDiag goroutine
// blocks on afterTicks exactly like sleeptest does, so 0 audit
// lines escape a failing run. Running here, from interrupt
// context, guarantees at least one dump per 2 s regardless of
// scheduler state.
//
// Format is compact (single line) so each PIT tick that emits
// one adds ~150 B of serial traffic at most.
//
//go:nosplit
func sleepAuditISRDump() {
	buf := sleepAuditISRBuf[:]
	off := 0
	off = appendStr(buf, off, "AUDIT t=")
	off = appendDec(buf, off, pitTicks)
	for i := uint32(0); i < 4 && i < maxCPUs; i++ {
		off = appendStr(buf, off, " c")
		off = appendDec(buf, off, uint64(i))
		off = appendStr(buf, off, "=")
		off = appendDec(buf, off, SchedTasksPushed[i])
		off = appendStr(buf, off, "/")
		off = appendDec(buf, off, SchedPopOk[i])
		off = appendStr(buf, off, "/")
		off = appendDec(buf, off, SchedPopNil[i])
	}
	off = appendStr(buf, off, " icr=")
	off = appendDec(buf, off, lapicICRTimeouts)
	off = appendStr(buf, off, " at=")
	off = appendDec(buf, off, afterTicksCalls)
	buf[off] = '\r'
	off++
	buf[off] = '\n'
	off++
	serialPrintBytes(buf[:off])
}

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

// migrateTraceEntry captures one `migrateAndPause` call for the
// P03a Option D audit — records source CPU + target CPU + the
// task pointer at push, and the actual resume CPU after the
// PauseLocked returns. Paired with pushTick/resumeTick so cases
// where the task sits in the target queue for many ticks are
// visible. See
// current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md
// §Option D.
type migrateTraceEntry struct {
	srcCPU     uint32
	targetCPU  uint32
	resumeCPU  uint32
	pushTick   uint64
	resumeTick uint64
	used       uint32 // 1 after push recorded; 2 after resume recorded
}

const migrateTraceSize = 64

var migrateTrace [migrateTraceSize]migrateTraceEntry
var migrateTraceHead uint32 // monotonic write index (racey; diagnostic)

// migrateTracePush records the push half of a migrateAndPause
// call. Returns the trace slot index so migrateTraceResume can
// fill in the resume half.
//
//go:linkname migrateTracePush migrateTracePush
func migrateTracePush(srcCPU, targetCPU uint32) uint32 {
	if !runSleepAudit {
		return 0xFFFFFFFF
	}
	idx := migrateTraceHead % migrateTraceSize
	migrateTraceHead++
	migrateTrace[idx].srcCPU = srcCPU
	migrateTrace[idx].targetCPU = targetCPU
	migrateTrace[idx].pushTick = pitTicks
	migrateTrace[idx].resumeCPU = 0xFFFFFFFF
	migrateTrace[idx].resumeTick = 0
	migrateTrace[idx].used = 1
	return idx
}

// migrateTraceResume fills in the resume-side data on the trace
// entry the matching push returned. If the slot has been wrapped
// by another call since, the update is silently dropped.
//
//go:linkname migrateTraceResume migrateTraceResume
func migrateTraceResume(idx uint32, resumeCPU uint32) {
	if !runSleepAudit {
		return
	}
	if idx >= migrateTraceSize {
		return
	}
	// Best-effort write; if the slot was recycled, we just
	// overwrite with the latest resume, which is still
	// informative.
	migrateTrace[idx].resumeCPU = resumeCPU
	migrateTrace[idx].resumeTick = pitTicks
	migrateTrace[idx].used = 2
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
