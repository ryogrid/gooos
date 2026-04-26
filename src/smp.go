// src/smp.go -- Symmetric Multi-Processing: AP discovery and boot.
//
// Discovers Application Processors via ACPI MADT, copies a real-mode
// trampoline below 1 MiB, and boots APs with the INIT-SIPI-SIPI
// sequence via Local APIC inter-processor interrupts.

package main

import "unsafe"

// Local APIC constants.
const (
	lapicBase    = uintptr(0xFEE00000) // Default LAPIC MMIO base address
	lapicRegID   = uintptr(0x020)      // APIC ID register (bits 24-31)
	lapicRegEOI  = uintptr(0x0B0)      // End-of-Interrupt register
	lapicRegSVR  = uintptr(0x0F0)      // Spurious Interrupt Vector Register
	lapicRegICRL = uintptr(0x300)      // Interrupt Command Register (low)
	lapicRegICRH = uintptr(0x310)      // Interrupt Command Register (high)
	lapicRegLVT0 = uintptr(0x350)      // LVT LINT0 register
	lapicRegLVT1 = uintptr(0x360)      // LVT LINT1 register

	// LAPIC timer registers (SMP v2).
	lapicRegLVTTimer     = uintptr(0x320) // LVT Timer register
	lapicRegTimerInitCnt = uintptr(0x380) // Timer initial count
	lapicRegTimerCurrCnt = uintptr(0x390) // Timer current count (read-only)
	lapicRegTimerDivCfg  = uintptr(0x3E0) // Timer divide configuration

	// Page table flags for MMIO (uncacheable).
	pagePCD = uintptr(1 << 4) // Page Cache Disable
	pagePWT = uintptr(1 << 3) // Page Write-Through
)

// Trampoline layout constants (must match trampoline.S).
const (
	trampPhys    = uintptr(0x8000) // Physical address where trampoline is copied
	trampSIPIVec = 0x08            // SIPI vector = trampPhys / 0x1000

	// Data area offsets from trampPhys.
	trampOffPML4    = uintptr(0xF28)
	trampOffEntry64 = uintptr(0xF30)
	trampOffStacks  = uintptr(0xF38)
	trampOffCounter = uintptr(0xF40)
)

// IPI command values for ICR Low register.
const (
	// Destination shorthand: all excluding self (bits 19:18 = 11).
	// Level: assert (bit 14).
	icrINIT = uint32(0x000C4500) // INIT IPI (delivery mode 101)
	icrSIPI = uint32(0x000C4600) // Startup IPI (delivery mode 110)
)

const smpMaxAPs = 16

// gdtReady is set to 1 by the BSP after gdtInit completes.
// APs spin on this before calling gdtInitPerCPU so they see
// a fully populated gdtTable template.
var gdtReady uint32

// bspBootDone is set to 1 by the BSP after the full boot
// sequence completes (services running, filesystem populated).
// APs spin on this before entering the scheduler.
var bspBootDone uint32

// numCoresOnline is the count of CPUs that successfully booted.
// Set by smpInit after the AP wait loop completes. Referenced by
// the patched TinyGo runtime (runtime_gooos.go) via
// `//go:extern main.numCoresOnline` so schedulerWake's IPI
// broadcast knows how many APs exist.
var numCoresOnline uint32 = 1

// apStacks holds per-AP stack top pointers. The trampoline indexes
// into this array using the atomically claimed AP index.
var apStacks [smpMaxAPs]uintptr

// trampolineStartAddr returns address of the trampoline code blob in .rodata.
//
//go:linkname trampolineStartAddr trampolineStartAddr
func trampolineStartAddr() uintptr

// trampolineEndAddr returns address past the trampoline code blob.
//
//go:linkname trampolineEndAddr trampolineEndAddr
func trampolineEndAddr() uintptr

// apEntryAddr returns address of the apEntry Go function.
//
//go:linkname apEntryAddr apEntryAddr
func apEntryAddr() uintptr

// lapicRead reads a 32-bit Local APIC register via MMIO.
func lapicRead(reg uintptr) uint32 {
	return *(*uint32)(unsafe.Pointer(lapicBase + reg))
}

// lapicWrite writes a 32-bit Local APIC register via MMIO.
func lapicWrite(reg uintptr, val uint32) {
	*(*uint32)(unsafe.Pointer(lapicBase + reg)) = val
}

// lapicSendEOI signals end-of-interrupt to the Local APIC.
// Must be called at the end of every LAPIC-routed interrupt handler.
//
//go:nosplit
func lapicSendEOI() {
	lapicWrite(lapicRegEOI, 0)
}

// restoreBSPVirtualWire reasserts BSP-side PIC pass-through after late
// boot transitions. On affected SMP boots, keyboard IRQ1 can stop
// arriving once the shell reaches ShellReady and AP release begins even
// though the original boot-time LAPIC/PIC setup succeeded. Rewriting the
// BSP's virtual-wire state here is deterministic and low-risk: BSP keeps
// ExtINT on LINT0, NMI on LINT1, and both PICs fully unmasked in the
// non-IOAPIC path.
func restoreBSPVirtualWire() {
	lapicWrite(lapicRegLVT0, 0x00000700) // ExtINT, unmasked
	lapicWrite(lapicRegLVT1, 0x00000400) // NMI, unmasked
	outb(pic1Data, 0x00)
	outb(pic2Data, 0x00)
}

// lapicWaitICR spins until the ICR delivery status bit (12) is idle,
// with a bounded iteration cap. Nosplit because callers run from ISR
// context (pitWakeAPs → handleTimer, broadcastPreemptIPI → handleLAPICTimer).
//
// The previous unbounded spin could freeze the ISR if the LAPIC
// delivery stalled for any reason (emulation corner, hardware quirk);
// a hung PIT ISR would in turn freeze afterTicks, the shell, and every
// other kernel goroutine. A dropped IPI is recoverable because the
// next PIT tick will retry; a hung ISR is not. 65_536 MMIO reads take
// a few hundred microseconds at QEMU rates — far below the 10 ms PIT
// period — so the bound is safe.
//
//go:nosplit
func lapicWaitICR() {
	for i := 0; i < 65536; i++ {
		if lapicRead(lapicRegICRL)&(1<<12) == 0 {
			return
		}
	}
	// Timeout: give up, let the next caller retry.
	// P03 audit (gated counter in src/percpu.go): bump so
	// sleepAuditDump can report IPI delivery stalls.
	if runSleepAudit {
		lapicICRTimeouts++
	}
}

// ioDelay performs a short delay (~1 µs per iteration) using port 0x80.
func ioDelay(us int) {
	for i := 0; i < us; i++ {
		outb(0x80, 0)
	}
}

// smpInit discovers APs, boots them via INIT-SIPI-SIPI, and reports
// the total core count on VGA and serial.
func smpInit() {
	// Map LAPIC MMIO page (0xFEE00000) as identity-mapped,
	// uncacheable. This address is ABOVE the 1 GiB boot identity
	// map, so a 4 KiB mapPage is required. The PML4[0] → PDP[3]
	// entry is shared with child processes via newProcPML4's full
	// PDP copy.
	mapPage(lapicBase, lapicBase, pagePresent|pageWrite|pagePCD|pagePWT)

	// Configure LVT LINT0 for ExtINT (PIC pass-through) and LINT1 for NMI
	// before enabling the LAPIC, to preserve PIC interrupt delivery.
	lapicWrite(lapicRegLVT0, 0x00000700) // ExtINT, unmasked
	lapicWrite(lapicRegLVT1, 0x00000400) // NMI, unmasked

	// Enable LAPIC: set software-enable bit (8) and spurious vector (0xFF).
	svr := lapicRead(lapicRegSVR)
	lapicWrite(lapicRegSVR, svr|(1<<8)|0xFF)

	bspID := lapicRead(lapicRegID) >> 24
	serialPrintln("SMP: BSP APIC ID=" + utoa(uint64(bspID)))

	// Try ACPI MADT to learn expected AP count.
	expectedAPs := detectAPsFromACPI(bspID)
	if expectedAPs > 0 {
		serialPrintln("SMP: MADT reports " + utoa(uint64(expectedAPs)) + " APs")
	} else {
		serialPrintln("SMP: MADT not found, using broadcast")
	}

	// Copy trampoline blob to physical address 0x8000.
	src := trampolineStartAddr()
	size := trampolineEndAddr() - src
	for i := uintptr(0); i < size; i++ {
		*(*byte)(unsafe.Pointer(trampPhys + i)) = *(*byte)(unsafe.Pointer(src + i))
	}

	// Allocate per-AP stacks (4 KiB each, stack grows down).
	for i := 0; i < smpMaxAPs; i++ {
		page := allocPage()
		apStacks[i] = page + pageSize
	}

	// Patch trampoline data area.
	*(*uint32)(unsafe.Pointer(trampPhys + trampOffPML4)) = uint32(readCR3() &^ 0xFFF)
	*(*uint64)(unsafe.Pointer(trampPhys + trampOffEntry64)) = uint64(apEntryAddr())
	*(*uint64)(unsafe.Pointer(trampPhys + trampOffStacks)) = uint64(uintptr(unsafe.Pointer(&apStacks[0])))
	*(*uint32)(unsafe.Pointer(trampPhys + trampOffCounter)) = 0

	// ---- INIT-SIPI-SIPI sequence ----

	// Send INIT IPI to all APs (broadcast, all excluding self).
	lapicWaitICR()
	lapicWrite(lapicRegICRL, icrINIT)

	// Wait 10 ms (2 PIT ticks at 100 Hz, gives margin).
	initTarget := pitTicks + 2
	for pitTicks < initTarget {
		hlt()
	}

	// Send first SIPI with trampoline vector.
	lapicWaitICR()
	lapicWrite(lapicRegICRL, icrSIPI|trampSIPIVec)

	// Wait 200 µs.
	ioDelay(200)

	// Send second SIPI (retry per Intel spec).
	lapicWaitICR()
	lapicWrite(lapicRegICRL, icrSIPI|trampSIPIVec)

	// Wait for APs with adaptive timeout: reset deadline each time a
	// new AP comes online, so we do not wait the full timeout if all
	// APs are already up.
	deadline := pitTicks + 10 // 100 ms initial timeout
	lastCount := uint32(0)
	for pitTicks < deadline {
		count := *(*uint32)(unsafe.Pointer(trampPhys + trampOffCounter))
		if count != lastCount {
			lastCount = count
			deadline = pitTicks + 5 // 50 ms after each new AP
		}
		if expectedAPs > 0 && int(lastCount) >= expectedAPs {
			break
		}
		hlt()
	}

	apCount := *(*uint32)(unsafe.Pointer(trampPhys + trampOffCounter))
	totalCores := uint64(apCount) + 1 // +1 for BSP
	numCoresOnline = uint32(totalCores)

	msg := "SMP: " + utoa(totalCores) + " cores online"
	vgaWriteLine(19, msg)
	serialPrintln(msg)
}

// apEntry is the 64-bit entry point for each AP, called from trampoline.
// Outputs "AP N online" to serial using only port I/O (no heap allocation)
// and halts forever.
//
//export apEntry
func apEntry(apIndex uint64) {
	// Initialize per-CPU storage for this AP before any per-CPU access.
	percpuInitAP(apIndex)

	// Wait for BSP to finish gdtInit (populates canonical gdtTable
	// entries that gdtInitPerCPU copies from). gooosPause() provides
	// an x86 pipeline hint and acts as a compiler barrier to prevent
	// loop elision.
	for gdtReady == 0 {
		gooosPause()
	}

	// Load per-CPU GDT + TSS for this AP.
	gdtInitPerCPU(int(apIndex) + 1)

	// Load the IDT on this AP. Each CPU has its own IDTR, and an
	// AP starts with IDTR = {base=0, limit=0xFFFF} (x86 reset
	// default). Without this, any exception on the AP triple-faults
	// because the CPU reads a zero-filled descriptor from address 0
	// — the root cause of the Ring-3 iretq triple-fault investigated
	// in M4 (impldoc/smp_m4_ring3_fault.md, evidence in
	// tmp/m4_qemu.log: "IDT=     0000000000000000 0000ffff").
	idtLoadAP()

	// Enable this AP's LAPIC (software-enable bit + spurious vector).
	// The BSP does this in smpInit; APs must do it themselves.
	svr := lapicRead(lapicRegSVR)
	lapicWrite(lapicRegSVR, svr|(1<<8)|0xFF)
	// Latch APICID only after LAPIC software-enable. Capturing earlier
	// (inside percpuInitAP) can read as 0 on some boots, which then
	// makes wakeup/preempt IPI send paths skip this AP forever.
	percpuLatchAPICIDCurrent()

	// Wait for BSP to finish LAPIC timer calibration.
	for lapicCalibratedInitCnt == 0 {
		gooosPause()
	}
	// Enable the AP's LAPIC timer. Prior concern (M2-4 Deferred in
	// TODO_SMP4.md) was that handleLAPICTimer's AP path could hit
	// a non-nosplit call or lock contention during BSP's late
	// boot. As of the preempt-phase-gating work in
	// current_impl_2026_04_24/03_smp_preempt_phase_gating.md, the
	// AP branch of handleLAPICTimer only sets
	// `perCPUBlocks[idx].WantReschedule = 1` and sends EOI — both
	// are //go:nosplit and lock-free. AP preempt fanout (vector
	// 0xFB) remains BSP-broadcast-only under the phase gate, so
	// enabling the AP timer here gives each AP an independent
	// 100 Hz tick that drives local reschedule flags without
	// adding new cross-CPU lock pressure. B2 per TODO_FIX.md.
	lapicTimerInit()

	// Per-AP "online" chatter races heavily under SMP and tends to
	// obscure the later shell/autorun diagnostics. The BSP summary
	// line ("SMP: N cores online") remains the authoritative signal.

	// Wait for BSP to complete its full boot sequence.
	for bspBootDone == 0 {
		gooosPause()
	}
	// Re-latch APICID once more after BSP boot completion. Early bring-up
	// reads can transiently return 0 on some boots.
	percpuLatchAPICIDCurrent()

	// Mask AP-side LINT0/LINT1. BSP keeps PIC pass-through on LINT0;
	// APs must not accept ExtINT/NMI through local LINT lines or
	// legacy PIC IRQ routing becomes flaky.
	*(*uint32)(unsafe.Pointer(uintptr(0xFEE00350))) = 0x10000 // LVT LINT0
	*(*uint32)(unsafe.Pointer(uintptr(0xFEE00360))) = 0x10000 // LVT LINT1

	// Enter the TinyGo scheduler loop on this AP.
	sti()
	markAPSchedulerEntered()
	apSchedulerEntry()

	// Safety net.
	for {
		hlt()
	}
}

// apSchedulerEntry was the AP entry into TinyGo's apScheduler.
// M5.2 (scheduler=none): runtime.apScheduler doesn't exist
// without a goroutine scheduler. Replace with the gooos kthread
// scheduler loop directly.
//
// §14 invariant U2: under uniprocessorKernel the gooos kernel
// runs as a uniprocessor on BSP. APs idle in `sti; hlt;` until
// they receive a Ring-3 dispatch IPI (mechanism is M7 future
// work). The previous `kschedLoop()` call is preserved as a
// comment so a future `git revert` of the §14 commit restores
// the SMP kernel scheduler in one diff.
func apSchedulerEntry() {
	// §15 §3.2 / §16 Step 3: M7 dispatch path. When userspaceSMP
	// is true, APs run the Ring-3-only dispatcher and consume
	// from kschedQueuesRing3[cpuID()]. Service kthreads stay
	// BSP-only per R1+R2; this loop never pops from the
	// service tier.
	if userspaceSMP {
		kschedLoopRing3Only(cpuID())
		return
	}
	if uniprocessorKernel {
		// M6: AP kernel-mode idle. Per-CPU LAPIC timer continues
		// to fire (initialised in apEntry); handlePreemptIPI
		// short-circuits because kschedRunning[c]==nil. Any IPI
		// (wakeup, freeze) wakes the CPU; without work to do,
		// it loops back into hlt.
		for {
			sti()
			hlt()
		}
	}
	kschedLoop()
}

// ---------- ACPI MADT Parsing ----------

// detectAPsFromACPI searches ACPI tables for the MADT and returns the
// number of enabled APs (excluding the BSP). Returns 0 on failure.
func detectAPsFromACPI(bspAPICID uint32) int {
	rsdp := findRSDP()
	if rsdp == 0 {
		return 0
	}

	// RSDP offset 16: RSDT physical address (4 bytes).
	rsdtAddr := uintptr(*(*uint32)(unsafe.Pointer(rsdp + 16)))
	if rsdtAddr == 0 || rsdtAddr >= 0x40000000 {
		return 0 // Outside identity-mapped region
	}

	// Verify RSDT signature "RSDT".
	sig := (*[4]byte)(unsafe.Pointer(rsdtAddr))
	if sig[0] != 'R' || sig[1] != 'S' || sig[2] != 'D' || sig[3] != 'T' {
		return 0
	}

	// RSDT header: length at offset 4. Entries (4-byte pointers) at offset 36.
	length := uintptr(*(*uint32)(unsafe.Pointer(rsdtAddr + 4)))
	numEntries := (length - 36) / 4

	for i := uintptr(0); i < numEntries; i++ {
		tableAddr := uintptr(*(*uint32)(unsafe.Pointer(rsdtAddr + 36 + i*4)))
		if tableAddr == 0 || tableAddr >= 0x40000000 {
			continue
		}
		// Check for MADT signature "APIC".
		tsig := (*[4]byte)(unsafe.Pointer(tableAddr))
		if tsig[0] == 'A' && tsig[1] == 'P' && tsig[2] == 'I' && tsig[3] == 'C' {
			return parseMADT(tableAddr, bspAPICID)
		}
	}
	return 0
}

// findRSDP searches standard BIOS memory areas for the ACPI RSDP
// signature "RSD PTR " (8 bytes, 16-byte aligned).
func findRSDP() uintptr {
	// Search main BIOS ROM area: 0xE0000 - 0xFFFFF.
	for addr := uintptr(0xE0000); addr < 0x100000; addr += 16 {
		if matchRSDP(addr) {
			return addr
		}
	}
	// Search EBDA (segment address at BDA 0x040E).
	ebdaSeg := *(*uint16)(unsafe.Pointer(uintptr(0x040E)))
	ebdaBase := uintptr(ebdaSeg) << 4
	if ebdaBase > 0 && ebdaBase < 0xA0000 {
		limit := ebdaBase + 1024
		for addr := ebdaBase; addr < limit; addr += 16 {
			if matchRSDP(addr) {
				return addr
			}
		}
	}
	return 0
}

// matchRSDP checks whether the 8-byte signature at addr is "RSD PTR ".
func matchRSDP(addr uintptr) bool {
	p := (*[8]byte)(unsafe.Pointer(addr))
	return p[0] == 'R' && p[1] == 'S' && p[2] == 'D' && p[3] == ' ' &&
		p[4] == 'P' && p[5] == 'T' && p[6] == 'R' && p[7] == ' '
}

// parseMADT walks MADT entries and counts enabled processors other
// than the BSP.
func parseMADT(madtAddr uintptr, bspAPICID uint32) int {
	length := uintptr(*(*uint32)(unsafe.Pointer(madtAddr + 4)))
	apCount := 0

	// MADT entries start at offset 44.
	offset := uintptr(44)
	for offset+2 <= length {
		entryType := *(*byte)(unsafe.Pointer(madtAddr + offset))
		entryLen := uintptr(*(*byte)(unsafe.Pointer(madtAddr + offset + 1)))
		if entryLen < 2 {
			break // Prevent infinite loop on malformed table
		}

		if entryType == 0 && entryLen >= 8 {
			// Type 0: Processor Local APIC entry.
			// Offset 3: APIC ID, Offset 4: Flags (bit 0 = enabled).
			apicID := *(*byte)(unsafe.Pointer(madtAddr + offset + 3))
			flags := *(*uint32)(unsafe.Pointer(madtAddr + offset + 4))
			if flags&1 != 0 && uint32(apicID) != bspAPICID {
				apCount++
			}
		}
		if entryType == 1 && entryLen >= 12 {
			// Type 1: IOAPIC entry.
			// Offset 4: IOAPIC MMIO base address (4 bytes).
			addr := uintptr(*(*uint32)(unsafe.Pointer(madtAddr + offset + 4)))
			if ioapicBase == 0 && addr != 0 {
				ioapicBase = addr
			}
		}
		offset += entryLen
	}
	return apCount
}
