// src/cr3.go — Go-side declaration of writeCR3, the asm helper
// added in src/stubs.S for the per-process PML4 work in
// impldoc/shell_io_multiprocess.md §2.5.
//
// Phase 4a lands the helper; phase 4d wires it into
// gooosOnResume so every Ring-3 goroutine resume installs the
// process's own PML4. Until 4d lands the function is unused.

package main

// writeCR3 loads pml4 into the CR3 control register, switching
// the active page-table root. The CR3 write implicitly flushes
// every non-global TLB entry, so callers do not need to issue
// invlpg afterwards.
//
// nosplit because gooosOnResume (the eventual sole caller) is
// nosplit; the asm body is one mov so this is trivially
// satisfied.
//
//go:linkname writeCR3 writeCR3
//go:nosplit
func writeCR3(pml4 uintptr)
