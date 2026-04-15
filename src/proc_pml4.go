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

// pml4SharedKernelPDP holds the boot PDP physical address that
// every per-process PML4 shares as its PDP[0]. Captured the
// first time newProcPML4 runs.
var pml4SharedKernelPDP uintptr

// captureKernelPDP reads the boot PML4 (current CR3) and
// remembers its PML4[0] entry's PDP physical address. Idempotent.
//
//go:nosplit
func captureKernelPDP() {
	if pml4SharedKernelPDP != 0 {
		return
	}
	bootPML4 := readCR3() &^ 0xFFF
	entry := *(*uint64)(unsafe.Pointer(bootPML4))
	if entry&uint64(pagePresent) == 0 {
		// Boot has no PML4[0] — should be impossible because
		// boot.S sets it up before paging is enabled.
		return
	}
	// Capture the per-PML4 entry as-is (flags + addr) so we can
	// install it verbatim into per-process PML4[0]. The PDP
	// physical address itself is the masked value; we keep the
	// full entry for simplicity.
	pml4SharedKernelPDP = uintptr(entry)
}

// newProcPML4 allocates a fresh PML4 page and wires up its
// PML4[0] to the boot kernel's PDP (so the per-process
// address space sees the same kernel identity map). Returns
// the physical address of the new PML4. allocPage zeros the
// page so PML4[1..511] are all empty.
func newProcPML4() uintptr {
	captureKernelPDP()
	pml4 := allocPage()
	// PML4[0] = boot PDP entry (pointer + flags inherited).
	*(*uint64)(unsafe.Pointer(pml4)) = uint64(pml4SharedKernelPDP)
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
