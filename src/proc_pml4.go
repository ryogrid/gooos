// src/proc_pml4.go — per-process PML4 lifecycle helpers.
//
// Each Process owns its own PML4 page. PML4[0] points at a
// per-process PDP. That per-process PDP shares its first
// entry (PDP[0]) with the boot kernel's PDP — so vaddrs
// 0..1 GiB stay identity-mapped to the kernel's text/data/
// heap on every CPU. Per-process PDP[1..511] hold per-process
// user mappings (user binaries link at 0x40100000, stack at
// 0x7FFF0000, arg page at 0x40300000, all > 1 GiB).
//
// See impldoc/shell_io_multiprocess.md §2.3.

package main

import "unsafe"

// pml4SharedKernelPDP0 holds the verbatim boot PDP[0] entry
// (paddr + flags) that every per-process PDP installs as its
// own PDP[0]. Captured the first time newProcPML4 runs. The
// shared value is the boot PD pointer; per-process PDP[1..511]
// are private.
var pml4SharedKernelPDP0 uint64

// bootPML4 is the kernel's boot-time PML4 phys addr. Captured
// at boot; used by processExit to switch CR3 back to a known-
// alive PML4 before freeProcPML4 deallocates the per-process
// one. Without this swap the kernel would be running on freed
// pages once freeProcPML4 returned its PT/PD/PDP/PML4 pages
// to the allocator.
var bootPML4 uintptr

// captureBootPML4 stores the current CR3 as the boot PML4. Must
// be called once during main() before any Ring-3 goroutine ever
// runs.
//
//go:nosplit
func captureBootPML4() {
	bootPML4 = readCR3() &^ 0xFFF
}

// captureKernelPDP0 reads the boot PML4 (current CR3) and walks
// its PML4[0] → PDP[0] entry. The PDP[0] entry is the pointer
// to the boot PD covering 0..1 GiB identity. Per-process PDPs
// install this same entry at their own PDP[0] so the kernel
// half stays mapped. Idempotent.
func captureKernelPDP0() {
	if pml4SharedKernelPDP0 != 0 {
		return
	}
	bootP := readCR3() &^ 0xFFF
	pml4Entry := *(*uint64)(unsafe.Pointer(bootP))
	if pml4Entry&uint64(pagePresent) == 0 {
		return
	}
	bootPDP := uintptr(pml4Entry) &^ 0xFFF
	pml4SharedKernelPDP0 = *(*uint64)(unsafe.Pointer(bootPDP))
}

// newProcPML4 allocates a fresh PML4 page AND a fresh
// per-process PDP page. PML4[0] points at the per-process PDP.
// The per-process PDP[0] is set to the boot PDP[0] entry by
// value (so vaddrs 0..1 GiB stay identity-mapped to the same
// kernel PD). Per-process PDP[1..511] are empty for user
// mappings; PML4[1..511] are empty too.
//
// Returns the physical address of the new PML4. allocPage
// zeros each page on alloc.
func newProcPML4() uintptr {
	captureKernelPDP0()
	pml4 := allocPage()
	pdp := allocPage()
	// PML4[0] entry: per-process PDP, PRESENT|WRITE|USER so
	// user pages farther down are reachable from Ring 3 (the
	// CPU ANDs U/S across levels).
	*(*uint64)(unsafe.Pointer(pml4)) = uint64(pdp) | uint64(pagePresent|pageWrite|pageUser)
	// PDP[0] entry: shared boot PDP[0] (= boot PD covering
	// 0..1 GiB identity, with whatever flags boot.S set).
	*(*uint64)(unsafe.Pointer(pdp)) = pml4SharedKernelPDP0
	return pml4
}

// freeProcPML4 walks PML4[1..511] of the per-process PML4 and
// frees every per-process PDP / PD / PT page underneath.
// Does NOT touch PML4[0] (shared with the kernel).
//
// User physical pages themselves are freed by the caller via
// processExit's existing UserPaddrs walk before this runs;
// freeProcPML4 only releases the page-table machinery.
func freeProcPML4(pml4 uintptr) {
	if pml4 == 0 {
		return
	}
	for i := uintptr(1); i < 512; i++ {
		entry := *(*uint64)(unsafe.Pointer(pml4 + i*8))
		if entry&uint64(pagePresent) == 0 {
			continue
		}
		freePDP(uintptr(entry) &^ 0xFFF)
	}
	freePage(pml4)
}

// freePDP releases a per-process PDP page after walking its
// 512 entries to free any present PDs. Ignores huge-page
// entries (PS bit) since gooos's user mappings are 4 KiB.
func freePDP(pdp uintptr) {
	for i := uintptr(0); i < 512; i++ {
		entry := *(*uint64)(unsafe.Pointer(pdp + i*8))
		if entry&uint64(pagePresent) == 0 {
			continue
		}
		freePD(uintptr(entry) &^ 0xFFF)
	}
	freePage(pdp)
}

// freePD releases a PD page after walking its 512 entries
// for present PTs.
func freePD(pd uintptr) {
	for i := uintptr(0); i < 512; i++ {
		entry := *(*uint64)(unsafe.Pointer(pd + i*8))
		if entry&uint64(pagePresent) == 0 {
			continue
		}
		// PT pages contain only leaf entries; freeing the PT
		// page itself releases all its entry slots. The
		// physical pages those entries pointed at are already
		// freed by processExit's UserPaddrs walk.
		freePage(uintptr(entry) &^ 0xFFF)
	}
	freePage(pd)
}
