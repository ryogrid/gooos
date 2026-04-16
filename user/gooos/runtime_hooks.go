// Runtime hooks called by the patched TinyGo task scheduler in Ring-3
// user programs. The kernel provides equivalent bodies in
// src/goroutine_tss.go (gooosOnResume) and src/panic.go
// (gooosStackOverflow); userspace cannot touch TSS or issue the
// kernel's printing/halt primitives, so these supply Ring-3-safe
// replacements routed through sys_write and sys_exit.
//
// Both symbols are reached via //go:linkname from the patched
// internal/task/task_stack*.go hooks installed by
// scripts/tinygo_runtime.patch.

package gooos

import "unsafe"

//go:linkname gooosOnResume runtime.gooosOnResume
//go:nosplit
func gooosOnResume() {
	// No TSS to update — the CPU is already in Ring 3 and the
	// process's PML4 is current. User-goroutine switches stay
	// entirely inside this process.
}

//go:linkname gooosStackOverflow runtime.gooosStackOverflow
//go:nosplit
func gooosStackOverflow(_ uintptr) {
	// The `uintptr` parameter carries the failing *Task pointer from
	// internal/task/task_stack.go:Pause — unused in v1 because Go's
	// strconv.FormatUint allocates (unsafe on a corrupted stack).
	// Canary corruption means this goroutine's stack is already
	// dangerous, so avoid any allocation or multi-byte format.
	// sys_write routes fd=1 to serial only (fd=0 routes to VGA +
	// serial but requires Ring-0 MMIO the overflow path must not
	// touch). Match user/gooos/io.go's (buf, len, fd) argument
	// order on syscall3.
	msg := "gooos: user goroutine stack overflow\n"
	p := unsafe.Pointer(unsafe.StringData(msg))
	syscall3(sysWrite, uintptr(p), uintptr(len(msg)), 1)
	syscall1(sysExit, 1)
	for {
	}
}
