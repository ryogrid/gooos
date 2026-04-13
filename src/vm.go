// src/vm.go -- Virtual memory management: 4 KiB page mapping.
//
// Provides mapPage/unmapPage for 4-level x86_64 page table manipulation,
// a simple bump allocator for page frames, and a page fault handler.
//
// The existing 2 MiB identity mapping (0x00000000 - 0x3FFFFFFF) set up
// by boot.S remains untouched. This module adds 4 KiB granularity for
// new mappings outside that range.

package main

import "unsafe"

// Page table flags.
const (
	pageSize    = 4096
	pagePresent = 1 << 0
	pageWrite   = 1 << 1
	pageUser    = 1 << 2 // U/S bit: page accessible from Ring 3
)

// heapEndAddr returns the linker-defined _heap_end address.
// Implemented in stubs.S.
//
//go:linkname heapEndAddr heapEndAddr
func heapEndAddr() uintptr

// readCR2 returns the faulting virtual address from CR2.
// Implemented in stubs.S.
//
//go:linkname readCR2 readCR2
func readCR2() uintptr

// readCR3 returns the PML4 physical base address from CR3.
// Implemented in stubs.S.
//
//go:linkname readCR3 readCR3
func readCR3() uintptr

// invlpg invalidates the TLB entry for a virtual address.
// Implemented in stubs.S.
//
//go:linkname invlpg invlpg
func invlpg(addr uintptr)

// nextFreePage is the bump allocator's next available physical address.
var nextFreePage uintptr

// vmInit initializes the page frame allocator.
// Must be called before mapPage or allocPage.
func vmInit() {
	end := heapEndAddr()
	// Align up to the next 4 KiB boundary.
	nextFreePage = (end + pageSize - 1) &^ (pageSize - 1)
}

// allocPage returns the physical address of a zeroed 4 KiB page.
func allocPage() uintptr {
	page := nextFreePage
	nextFreePage += pageSize
	// Zero the page (required for new page table entries).
	for i := uintptr(0); i < pageSize; i += 8 {
		*(*uint64)(unsafe.Pointer(page + i)) = 0
	}
	return page
}

// mapPage maps a 4 KiB virtual page to a physical page.
// It walks the 4-level page table (PML4 -> PDP -> PD -> PT), creating
// intermediate tables as needed, and sets the leaf PT entry.
//
// Does NOT split existing 2 MiB huge page entries. Use only for virtual
// addresses not covered by the boot-time identity map.
func mapPage(vaddr, paddr, flags uintptr) {
	pml4 := readCR3() &^ 0xFFF

	pml4Idx := (vaddr >> 39) & 0x1FF
	pdpIdx := (vaddr >> 30) & 0x1FF
	pdIdx := (vaddr >> 21) & 0x1FF
	ptIdx := (vaddr >> 12) & 0x1FF

	pdp := walkOrCreate(pml4, pml4Idx, flags)
	pd := walkOrCreate(pdp, pdpIdx, flags)
	pt := walkOrCreate(pd, pdIdx, flags)

	// Set the leaf page table entry.
	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	*entry = uint64(paddr&^0xFFF) | uint64(flags)
}

// unmapPage removes the 4 KiB mapping for a virtual address and flushes
// the TLB entry. Does nothing if the address is not mapped at 4 KiB
// granularity.
func unmapPage(vaddr uintptr) {
	pml4 := readCR3() &^ 0xFFF

	pml4Idx := (vaddr >> 39) & 0x1FF
	pdpIdx := (vaddr >> 30) & 0x1FF
	pdIdx := (vaddr >> 21) & 0x1FF
	ptIdx := (vaddr >> 12) & 0x1FF

	pdp := walkExisting(pml4, pml4Idx)
	if pdp == 0 {
		return
	}
	pd := walkExisting(pdp, pdpIdx)
	if pd == 0 {
		return
	}
	pt := walkExisting(pd, pdIdx)
	if pt == 0 {
		return
	}

	// Clear the page table entry.
	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	*entry = 0

	// Flush the TLB entry for this virtual address.
	invlpg(vaddr)
}

// walkOrCreate returns the physical address of the next-level page table.
// If the entry at table[index] is not present, a new zeroed page table
// is allocated and linked. The flags parameter controls permission bits
// on intermediate entries: if pageUser is set, the User/Supervisor bit
// is propagated to all intermediate page table levels (required for
// Ring 3 accessible pages, since the CPU ANDs permissions across levels).
func walkOrCreate(table uintptr, index uintptr, flags uintptr) uintptr {
	entry := (*uint64)(unsafe.Pointer(table + index*8))
	if *entry&uint64(pagePresent) != 0 {
		// Propagate User bit to existing entries when needed.
		if flags&pageUser != 0 {
			*entry |= uint64(pageUser)
		}
		return uintptr(*entry) &^ 0xFFF
	}
	// Allocate a new page table and link it.
	newTable := allocPage()
	intermediateFlags := uintptr(pagePresent | pageWrite)
	if flags&pageUser != 0 {
		intermediateFlags |= pageUser
	}
	*entry = uint64(newTable) | uint64(intermediateFlags)
	return newTable
}

// walkExisting returns the physical address of the next-level page table,
// or 0 if the entry is not present.
func walkExisting(table uintptr, index uintptr) uintptr {
	entry := (*uint64)(unsafe.Pointer(table + index*8))
	if *entry&uint64(pagePresent) == 0 {
		return 0
	}
	return uintptr(*entry) &^ 0xFFF
}

// handlePageFault displays the faulting address and error code on VGA
// and serial, then halts. Page faults are fatal in this kernel.
func handlePageFault(vector uint64) {
	faultAddr := readCR2()
	errCode := lastErrorCode

	msg := "PF: addr=0x" + hextoa(uint64(faultAddr)) + " err=0x" + hextoa(errCode)
	vgaWriteLine(12, msg)
	serialPrintln(msg)

	// Fatal: halt the CPU.
	for {
		hlt()
	}
}

// hextoa converts a uint64 to its hexadecimal string representation.
func hextoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	const hexDigits = "0123456789ABCDEF"
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = hexDigits[n&0xF]
		n >>= 4
	}
	return string(buf[i:])
}
