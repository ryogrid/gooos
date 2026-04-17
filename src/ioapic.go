// src/ioapic.go -- IOAPIC discovery and interrupt routing.
//
// Parses ACPI MADT type-1 entries to discover the IOAPIC base
// address, then programs the redirection table to route IRQ0
// (PIT timer) and IRQ1 (keyboard) to the BSP via the LAPIC.
// Disables the legacy 8259A PIC once IOAPIC routing is active.
//
// See impldoc/smp_kernel_lapic_and_ipi.md §8 for the design.

package main

import "unsafe"

// ioapicBase is the IOAPIC MMIO base address, discovered from
// the ACPI MADT type-1 entry. Zero if not found.
var ioapicBase uintptr

// ioapicActive is true after ioapicInit successfully programs
// the redirection table and disables the PIC. When true,
// interrupt handlers must call lapicSendEOI() instead of
// picSendEOI().
var ioapicActive bool

// ioapicRead reads a 32-bit IOAPIC register via indirect access.
func ioapicRead(reg uint32) uint32 {
	*(*uint32)(unsafe.Pointer(ioapicBase)) = reg
	return *(*uint32)(unsafe.Pointer(ioapicBase + 0x10))
}

// ioapicWrite writes a 32-bit IOAPIC register via indirect access.
func ioapicWrite(reg uint32, val uint32) {
	*(*uint32)(unsafe.Pointer(ioapicBase)) = reg
	*(*uint32)(unsafe.Pointer(ioapicBase + 0x10)) = val
}

// ioapicSetRedirection programs a single IOAPIC redirection table
// entry. irq is the IOAPIC input pin (0-23), vector is the IDT
// vector, and destAPICID is the target CPU's APIC ID.
func ioapicSetRedirection(irq uint8, vector uint8, destAPICID uint8) {
	regLo := uint32(0x10 + 2*uint32(irq))
	regHi := regLo + 1

	// Low 32 bits: vector, fixed delivery (000), physical dest (0),
	// active high (0), edge-triggered (0), unmasked (0).
	lo := uint32(vector)
	// High 32 bits: destination APIC ID in bits 24-27.
	hi := uint32(destAPICID) << 24

	ioapicWrite(regHi, hi)
	ioapicWrite(regLo, lo)
}

// ioapicMaskIRQ masks (disables) a single IOAPIC redirection entry.
func ioapicMaskIRQ(irq uint8) {
	regLo := uint32(0x10 + 2*uint32(irq))
	lo := ioapicRead(regLo)
	ioapicWrite(regLo, lo|(1<<16))
}

// ioapicInit discovers and programs the IOAPIC. Must be called
// after smpInit (LAPIC enabled) and after parseMADT has set
// ioapicBase. Disables the legacy PIC once IOAPIC is active.
func ioapicInit() {
	if ioapicBase == 0 {
		serialPrintln("IOAPIC: not found in MADT, skipping")
		return
	}

	// Map IOAPIC MMIO page (identity-mapped, uncacheable).
	mapPage(ioapicBase, ioapicBase, pagePresent|pageWrite|pagePCD|pagePWT)

	// Read version and max redirection entries.
	ver := ioapicRead(0x01)
	maxRedir := (ver >> 16) & 0xFF

	serialPrint("IOAPIC: base=0x")
	serialPrint(hextoa(uint64(ioapicBase)))
	serialPrint(" ver=")
	serialPrint(utoa(uint64(ver & 0xFF)))
	serialPrint(" max_redir=")
	serialPrintln(utoa(uint64(maxRedir)))

	// Mask all entries first.
	for i := uint32(0); i <= maxRedir; i++ {
		ioapicMaskIRQ(uint8(i))
	}

	bspAPICID := uint8(lapicRead(lapicRegID) >> 24)

	// Program IRQ0 (PIT timer) → vector 32, route to BSP.
	ioapicSetRedirection(0, 32, bspAPICID)

	// Program IRQ1 (keyboard) → vector 33, route to BSP.
	ioapicSetRedirection(1, 33, bspAPICID)

	// Disable the legacy 8259A PIC by masking all IRQs.
	outb(pic1Data, 0xFF)
	outb(pic2Data, 0xFF)

	ioapicActive = true
	serialPrintln("IOAPIC: PIC disabled, IOAPIC routing active")
}
