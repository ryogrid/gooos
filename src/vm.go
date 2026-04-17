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

// allocStartAddr returns the linker-defined _alloc_start address.
// This is the first address after the .pagetables section, available
// for dynamic page allocation by allocPage.
// Implemented in stubs.S.
//
//go:linkname allocStartAddr allocStartAddr
func allocStartAddr() uintptr

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

// readFlags returns the current RFLAGS register. Implemented in stubs.S.
//
//go:linkname readFlags readFlags
func readFlags() uintptr

// restoreFlags loads the given value into RFLAGS. Implemented in stubs.S.
//
//go:linkname restoreFlags restoreFlags
func restoreFlags(flags uintptr)

// invlpg invalidates the TLB entry for a virtual address.
// Implemented in stubs.S.
//
//go:linkname invlpg invlpg
func invlpg(addr uintptr)

// nextFreePage is the bump allocator's next available physical address.
var nextFreePage uintptr

// freeStackCap bounds the external free stack. 4096 entries × 8 bytes =
// 32 KiB of .bss metadata, enough to hold ~16 MiB of reclaimable pages.
const freeStackCap = 4096

// freeStack is a LIFO of freed physical page addresses. Storing the
// metadata OUTSIDE the freed pages avoids the old corruption bug where
// a next-pointer stored in a freed page was misread as PTE bits when
// the page was later reused as an intermediate page table.
var (
	freeStack    [freeStackCap]uintptr
	freeStackLen int
)

// vmInit initializes the page frame allocator.
// Must be called before mapPage or allocPage.
func vmInit() {
	end := allocStartAddr()
	// Align up to the next 4 KiB boundary.
	nextFreePage = (end + pageSize - 1) &^ (pageSize - 1)
}

// pageAllocLock protects the page allocator (free stack + bump
// pointer) for SMP safety. Lock ordering rank 1 (outermost).
var pageAllocLock Spinlock

// allocPage returns the physical address of a zeroed 4 KiB page.
// Prefers the LIFO free stack; falls back to the bump allocator.
func allocPage() uintptr {
	flags := pageAllocLock.Acquire()

	var page uintptr
	if freeStackLen > 0 {
		freeStackLen--
		page = freeStack[freeStackLen]
	} else {
		page = nextFreePage
		nextFreePage += pageSize
	}

	pageAllocLock.Release(flags)

	for i := uintptr(0); i < pageSize; i += 8 {
		*(*uint64)(unsafe.Pointer(page + i)) = 0
	}
	return page
}

// allocPagesContig returns the physical address of n physically contiguous
// zeroed 4 KiB pages. Bypasses the LIFO free stack (which does not guarantee
// contiguity) and always bump-allocates. Used for kernel stacks and other
// multi-page structures accessed as a single flat region via the identity map.
func allocPagesContig(n int) uintptr {
	flags := pageAllocLock.Acquire()
	base := nextFreePage
	nextFreePage += uintptr(n) * pageSize
	pageAllocLock.Release(flags)

	total := uintptr(n) * pageSize
	for i := uintptr(0); i < total; i += 8 {
		*(*uint64)(unsafe.Pointer(base + i)) = 0
	}
	return base
}

// freePage returns a physical page to the allocator. The page is zeroed
// before being pushed so that if walkOrCreate reuses it as an intermediate
// page table, every PTE slot reads as non-present. If the free stack is
// full the page is leaked (bump allocator has ~950 MiB of headroom).
func freePage(paddr uintptr) {
	if paddr == 0 {
		return
	}
	for i := uintptr(0); i < pageSize; i += 8 {
		*(*uint64)(unsafe.Pointer(paddr + i)) = 0
	}

	flags := pageAllocLock.Acquire()
	if freeStackLen < freeStackCap {
		freeStack[freeStackLen] = paddr
		freeStackLen++
	}
	pageAllocLock.Release(flags)
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

// walkAndGetPaddr returns the physical page frame address for a virtual
// address by walking the 4-level page table. Returns 0 if the address
// is not mapped at 4 KiB granularity.
func walkAndGetPaddr(vaddr uintptr) uintptr {
	pml4 := readCR3() &^ 0xFFF

	pml4Idx := (vaddr >> 39) & 0x1FF
	pdpIdx := (vaddr >> 30) & 0x1FF
	pdIdx := (vaddr >> 21) & 0x1FF
	ptIdx := (vaddr >> 12) & 0x1FF

	pdp := walkExisting(pml4, pml4Idx)
	if pdp == 0 {
		return 0
	}
	pd := walkExisting(pdp, pdpIdx)
	if pd == 0 {
		return 0
	}
	pt := walkExisting(pd, pdIdx)
	if pt == 0 {
		return 0
	}

	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	if *entry&uint64(pagePresent) == 0 {
		return 0
	}
	return uintptr(*entry) &^ 0xFFF
}

// --- Per-process PML4 variants ---------------------------------------------
//
// The *In / *From helpers take an explicit PML4 physical
// address instead of reading CR3. Used by impldoc/
// shell_io_multiprocess.md §3 to populate a child process's
// page tables before the kernel ever switches to that PML4.

// mapPageInto is mapPage with an explicit PML4. The kernel
// runs on its own boot PML4 throughout the call; the per-
// process PML4 only becomes active when gooosOnResume swaps
// CR3 (phase 4d).
func mapPageInto(pml4, vaddr, paddr, flags uintptr) {
	pml4Idx := (vaddr >> 39) & 0x1FF
	pdpIdx := (vaddr >> 30) & 0x1FF
	pdIdx := (vaddr >> 21) & 0x1FF
	ptIdx := (vaddr >> 12) & 0x1FF

	pdp := walkOrCreate(pml4, pml4Idx, flags)
	pd := walkOrCreate(pdp, pdpIdx, flags)
	pt := walkOrCreate(pd, pdIdx, flags)

	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	*entry = uint64(paddr&^0xFFF) | uint64(flags)
}

// unmapPageFrom is unmapPage with an explicit PML4. Does NOT
// invlpg — the caller is not running on `pml4`, so its TLB
// entry can't be present. When the caller eventually loads
// `pml4` into CR3, the swap flushes the TLB anyway.
func unmapPageFrom(pml4, vaddr uintptr) {
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
	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	*entry = 0
}

// walkAndGetPaddrIn is walkAndGetPaddr with an explicit PML4.
// Used by elfSpawn (phase 4e) to find the physical page backing
// a child's vaddr, so the kernel can populate the page through
// the identity-mapped paddr without dereferencing the child's
// vaddr.
func walkAndGetPaddrIn(pml4, vaddr uintptr) uintptr {
	pml4Idx := (vaddr >> 39) & 0x1FF
	pdpIdx := (vaddr >> 30) & 0x1FF
	pdIdx := (vaddr >> 21) & 0x1FF
	ptIdx := (vaddr >> 12) & 0x1FF

	pdp := walkExisting(pml4, pml4Idx)
	if pdp == 0 {
		return 0
	}
	pd := walkExisting(pdp, pdpIdx)
	if pd == 0 {
		return 0
	}
	pt := walkExisting(pd, pdIdx)
	if pt == 0 {
		return 0
	}
	entry := (*uint64)(unsafe.Pointer(pt + ptIdx*8))
	if *entry&uint64(pagePresent) == 0 {
		return 0
	}
	return uintptr(*entry) &^ 0xFFF
}

// handlePageFault displays the faulting address and error code on VGA
// and serial, then halts. Page faults are fatal in this kernel.
//
// Allocation-free: formats into panicHexBuf so it is safe to call
// from ISR context. See impldoc/deferred_fatal_handlers.md.
//
//go:nosplit
func handlePageFault(vector uint64) {
	faultAddr := readCR2()
	idx := cpuID()
	errCode := lastErrorCodes[idx]
	frame := (*SyscallFrame)(unsafe.Pointer(lastFramePtrs[idx]))
	faultRIP := frame.RIP

	off := 0
	off = appendStr(panicHexBuf[:], off, "PF: addr=")
	off = appendHex(panicHexBuf[:], off, uint64(faultAddr))
	off = appendStr(panicHexBuf[:], off, " err=")
	off = appendHex(panicHexBuf[:], off, errCode)
	off = appendStr(panicHexBuf[:], off, " rip=")
	off = appendHex(panicHexBuf[:], off, uint64(faultRIP))

	vgaWriteLine(12, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')

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
