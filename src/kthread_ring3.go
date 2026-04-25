// src/kthread_ring3.go -- Route C M4.1: ring3Wrapper as kernel thread.
//
// Side-table approach: kthreadHostedProc[slot] maps a kthread pool
// slot -> the *Process it hosts. Updated by kschedSpawnRing3Wrapper
// at spawn time; cleared by processExit's kthread branch.
//
// CR3 + TSS.RSP0 install runs at the top of ring3WrapperKT, NOT in
// the scheduler dispatch loop -- attempt 2 hit a non-deterministic
// boot regression when any function call was added inside
// kschedLoopOnce/kschedLoop. See no_goroutine_kernel_design/
// 12_implementation_notes.md §M4.1 for the bisection record.
//
// Scope (M4.1 alpha): first-dispatch install only. Cross-CPU
// preempt re-install is M4.1.b / M4.3 work — see plan §"Out of
// scope".

package main

import "unsafe"

// kthreadHostedProc maps kthread pool slot -> hosted Process. Read
// from the kthread's own context (ring3WrapperKT) and from
// processExit; written by kschedSpawnRing3Wrapper and processExit.
// Indexed by KernelThread.Slot which is stable for the thread's
// lifetime (set in kthreadPoolAlloc).
var kthreadHostedProc [kthreadPoolCap]*Process

// kschedSpawnRing3Wrapper allocates a kthread, records proc in the
// side table, sets the entry to the top-level ring3WrapperKT (no
// closure -- avoids the heap alloc that contributed to attempt 1's
// race), and enqueues. Returns the kthread handle so callers can
// join later.
//
// Mirrors kschedSpawn (src/kthread_lifecycle.go:21) with two
// differences: name = "ring3", side-table store before enqueue.
func kschedSpawnRing3Wrapper(proc *Process) *KernelThread {
	t := kthreadPoolAlloc()
	if t == nil {
		kthreadPoolExhaustedPanic()
	}
	name := "ring3"
	for i := 0; i < len(name); i++ {
		t.Name[i] = name[i]
	}
	t.Name[len(name)] = 0
	t.Entry = ring3WrapperKT

	top := uintptr(unsafe.Pointer(&t.Stack.Top))
	rsp := top - 8*8
	enterAddr := kschedEnterAddr()
	selfPtr := uintptr(unsafe.Pointer(t))
	words := [8]uintptr{
		0,         // RBX
		0,         // RBP
		0,         // R12 (unused)
		selfPtr,   // R13 -> &KernelThread
		0,         // R14
		0,         // R15
		0x202,     // RFLAGS (IF=1, mandatory bit 1 set)
		enterAddr, // RIP -> kschedEnter
	}
	for i := 0; i < 8; i++ {
		*(*uintptr)(unsafe.Pointer(rsp + uintptr(i)*8)) = words[i]
	}
	t.SavedRSP = rsp
	t.State = uint32(KStateRunnable)

	// Record the proc BEFORE the push so a wake on the target CPU
	// can resolve the proc as soon as the kthread is dispatched.
	kthreadHostedProc[t.Slot] = proc

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

// ring3WrapperKT is the kthread entry point for a Ring-3 process.
// Reads proc from the side table, installs CR3 + TSS.RSP0 for the
// kthread's own kernel stack, then jumps into Ring 3. Never
// returns in the success path -- Ring 3 -> processExit ->
// kschedExit (via the kthread branch in processExit).
//
func ring3WrapperKT() {
	serialPrintln("ring3WrapperKT: enter cpuID=" + utoa(uint64(cpuID())))
	cpu := cpuID()
	t := kschedRunning[cpu]
	if t == nil || t.Slot < 0 || int(t.Slot) >= kthreadPoolCap {
		serialPrintln("ring3WrapperKT: bogus thread state, returning")
		return
	}
	proc := kthreadHostedProc[t.Slot]
	if proc == nil {
		serialPrintln("ring3WrapperKT: proc nil, returning")
		return
	}

	// Pool-slot bookkeeping for ISR-context Process lookup. Same
	// shape as the goroutine-hosted ring3Wrapper at
	// src/process.go:259-260.
	proc.poolIdx = int(t.Slot)
	setProcByPoolSlot(int(t.Slot), proc)
	perCPUBlocks[cpu].CurrentPoolIdx = int32(t.Slot)

	// Bridge currentProc() lookups: syscall handlers call
	// currentProc() which reads procByTask[taskCurrent()]. From a
	// kthread context taskCurrent() returns the per-CPU stale
	// TinyGo task (whatever was running when waitForEvents called
	// kschedLoopOnce). Storing under that key lets syscall ISRs
	// running on this kthread's stack resolve the proc through the
	// usual path. Harmless because the kthread stays on its own
	// stack and only this CPU will fire syscalls under this trap.
	setCurrentProc(proc)

	// Per-process CR3 swap (kernel half identity-mapped, so safe
	// from kthread context).
	if proc.pml4 != 0 {
		writeCR3(proc.pml4)
	}

	// Install TSS.RSP0 to point at the top of THIS kthread's
	// stack. This is the kernel stack the CPU will switch to on
	// a Ring-3 -> Ring-0 transition (syscall, fault, IRQ).
	tssSetRSP0(uintptr(unsafe.Pointer(&t.Stack.Top)))

	serialPrintln("ring3WrapperKT: jumping to Ring 3 entry=0x" + hextoa(uint64(proc.EntryPoint)))
	setGateDPL3(0x80)
	jumpToRing3(proc.EntryPoint, proc.StackTop)
}
