// src/kthread_ring3.go -- Ring-3 context installation for kthread-
// hosted ring3Wrappers.
//
// Route C M4.1: ring3Wrapper runs as a gooos kernel thread whose
// embedded 16 KiB stack (KernelStack) is the kernel stack the CPU
// uses for Ring 0 ← Ring 3 traps (TSS.RSP0) and whose dispatched
// CR3 is the per-process PML4. kschedLoopOnce installs both
// right before the kschedSwitch into such a thread by calling
// kschedInstallRing3Ctx below.
//
// Design: no_goroutine_kernel_design/07_userspace_boundary.md.

package main

import "unsafe"

// kschedInstallRing3Ctx is invoked from kschedLoopOnce /
// kschedLoop when a kthread with non-nil Proc is about to be
// dispatched. It writes the host process's PML4 into CR3 (only
// if different from the current CR3 — CR3 writes flush the TLB
// and should be avoided when the previous host was the same
// process) and points TSS.RSP0 at the kthread's own stack top
// so subsequent int 0x80 / LAPIC IRQs from Ring 3 land on a
// well-defined kernel stack.
//
//go:nosplit
func kschedInstallRing3Ctx(t *KernelThread) {
	proc := t.Proc
	if proc == nil {
		return
	}
	// TSS.RSP0: point at the kthread's stack top. &Stack.Top is
	// the address just past the usable stack region (Pad), which
	// is the right one-past-end value for RSP0.
	tssSetRSP0(uintptr(unsafe.Pointer(&t.Stack.Top)))
	// CR3 swap: only if different. readCR3 costs one rdcr3; the
	// flush-on-equal-write is the costly case we elide.
	if proc.pml4 != 0 && proc.pml4 != readCR3() {
		writeCR3(proc.pml4)
	}
}

// kschedSpawnProc spawns a kernel thread that hosts a Ring-3
// process. Same shape as kschedSpawn but populates the Proc
// field atomically before the thread is first dispatched. The
// entry function should be a ring3Wrapper closure that reads
// proc from the thread's Proc field.
//
// Returns the thread handle so callers (elfLoad / elfSpawn) can
// park on proc.ExitEv rather than juggling a goroutine handle.
func kschedSpawnProc(name string, entry func(), proc *Process) *KernelThread {
	t := kthreadPoolAlloc()
	if t == nil {
		kthreadPoolExhaustedPanic()
	}
	n := len(name)
	if n > len(t.Name)-1 {
		n = len(t.Name) - 1
	}
	for i := 0; i < n; i++ {
		t.Name[i] = name[i]
	}
	t.Name[n] = 0
	t.Entry = entry
	t.Proc = proc

	// Build the initial switch frame — same layout as kschedSpawn
	// (kthread_lifecycle.go). kschedEnter reads %r13 as
	// *KernelThread and calls kschedRunEntry(t) → t.Entry().
	top := uintptr(unsafe.Pointer(&t.Stack.Top))
	rsp := top - 8*8
	enterAddr := kschedEnterAddr()
	selfPtr := uintptr(unsafe.Pointer(t))
	words := [8]uintptr{
		0,         // RBX
		0,         // RBP
		0,         // R12
		selfPtr,   // R13 -> &KernelThread
		0,         // R14
		0,         // R15
		0x202,     // RFLAGS (IF=1)
		enterAddr, // RIP -> kschedEnter
	}
	for i := 0; i < 8; i++ {
		*(*uintptr)(unsafe.Pointer(rsp + uintptr(i)*8)) = words[i]
	}
	t.SavedRSP = rsp
	t.State = uint32(KStateRunnable)

	target := kschedSpawnRRCounter
	kschedSpawnRRCounter++
	if numCoresOnline == 0 {
		target = 0
	} else {
		target = target % numCoresOnline
	}
	kschedPush(t, target)
	return t
}
