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
	lapicRegSVR  = uintptr(0x0F0)      // Spurious Interrupt Vector Register
	lapicRegICRL = uintptr(0x300)      // Interrupt Command Register (low)
	lapicRegICRH = uintptr(0x310)      // Interrupt Command Register (high)
	lapicRegLVT0 = uintptr(0x350)      // LVT LINT0 register
	lapicRegLVT1 = uintptr(0x360)      // LVT LINT1 register

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

// lapicWaitICR spins until the ICR delivery status bit (12) is idle.
func lapicWaitICR() {
	for lapicRead(lapicRegICRL)&(1<<12) != 0 {
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
	// Map LAPIC MMIO page as identity-mapped, uncacheable.
	mapPage(lapicBase, lapicBase, pagePresent|pageWrite|pagePCD|pagePWT)

	// Configure LVT LINT0 for ExtINT (PIC pass-through) and LINT1 for NMI
	// before enabling the LAPIC, to preserve PIC interrupt delivery.
	lapicWrite(lapicRegLVT0, 0x00000700) // ExtINT, unmasked
	lapicWrite(lapicRegLVT1, 0x00000400) // NMI, unmasked

	// Enable LAPIC: set software-enable bit (8) and spurious vector (0xFF).
	svr := lapicRead(lapicRegSVR)
	lapicWrite(lapicRegSVR, svr|(1<<8)|0xFF)

	bspID := lapicRead(lapicRegID) >> 24
	serialPrint("SMP: BSP APIC ID=")
	serialPrintln(utoa(uint64(bspID)))

	// Try ACPI MADT to learn expected AP count.
	expectedAPs := detectAPsFromACPI(bspID)
	if expectedAPs > 0 {
		serialPrint("SMP: MADT reports ")
		serialPrint(utoa(uint64(expectedAPs)))
		serialPrintln(" APs")
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
	serialPutChar('A')
	serialPutChar('P')
	serialPutChar(' ')
	if apIndex >= 10 {
		serialPutChar(byte('0' + apIndex/10))
	}
	serialPutChar(byte('0' + apIndex%10))
	serialPutChar(' ')
	serialPutChar('o')
	serialPutChar('n')
	serialPutChar('l')
	serialPutChar('i')
	serialPutChar('n')
	serialPutChar('e')
	serialPutChar('\r')
	serialPutChar('\n')

	// Idle: enable interrupts and halt until an IPI arrives. v1 does
	// not send IPIs; SMP v2 will use them for cross-CPU goroutine
	// wakeups.
	sti()
	for {
		hlt()
	}
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
		offset += entryLen
	}
	return apCount
}
