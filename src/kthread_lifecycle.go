// src/kthread_lifecycle.go -- spawn / park / wake / exit.
//
// Spawn writes the initial register frame at the top of a fresh
// kernel stack so the first kschedSwitch-into-the-thread pops the
// frame and rets into the asm kschedEnter trampoline. The
// trampoline marshals %r12 / %r13 into System V arg regs and calls
// the Go entry function.
//
// Design: no_goroutine_kernel_design/02_kernel_thread_runtime.md
//         §"Lifecycle: spawn / park / wake / exit / preempt".

package main

import "unsafe"

// kschedSpawnAt creates a new kernel thread on a specific CPU.
// Used when round-robin placement is wrong (e.g., fsTask must be
// on BSP/CPU 0 so the boot-time BSP elf-pump can dispatch it
// without depending on AP idle hooks).
func kschedSpawnAt(name string, entry func(), targetCPU uint32) *KernelThread {
	t := kschedSpawnInternal(name, entry)
	if numCoresOnline > 0 && targetCPU >= numCoresOnline {
		targetCPU = 0
	}
	kschedPush(t, targetCPU)
	return t
}

// kschedSpawn creates a new kernel thread running entry() and
// enqueues it onto the round-robin target CPU. Returns a pointer
// to the thread so the caller can join / park on it later.
//
// Panics if the kthread pool is exhausted.
func kschedSpawn(name string, entry func()) *KernelThread {
	t := kschedSpawnInternal(name, entry)
	// Round-robin placement across online CPUs. kschedSpawnRRCounter
	// is racey; kschedPush's queue lock linearises the push itself.
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

// kschedSpawnInternal allocates a kthread + builds initial switch
// frame, but does not enqueue. Used by kschedSpawn (round-robin)
// and kschedSpawnAt (target CPU).
func kschedSpawnInternal(name string, entry func()) *KernelThread {
	t := kthreadPoolAlloc()
	if t == nil {
		kthreadPoolExhaustedPanic()
	}
	// Copy name (truncating to 15 + null).
	n := len(name)
	if n > len(t.Name)-1 {
		n = len(t.Name) - 1
	}
	for i := 0; i < n; i++ {
		t.Name[i] = name[i]
	}
	t.Name[n] = 0
	t.Entry = entry

	// Build the initial switch frame at the top of the stack. The
	// asm trampoline (kschedEnter in src/kthread_switch.S) reads
	// %r13 as a *KernelThread and calls back into Go (kschedRunEntry)
	// which does `t.Entry()` — that sidesteps any need for us to
	// reverse-engineer TinyGo's func-value layout.
	top := uintptr(unsafe.Pointer(&t.Stack.Top))
	rsp := top - 8*8
	enterAddr := kschedEnterAddr()
	selfPtr := uintptr(unsafe.Pointer(t))
	words := [8]uintptr{
		0, 0, 0, selfPtr, 0, 0, 0x202, enterAddr,
	}
	for i := 0; i < 8; i++ {
		*(*uintptr)(unsafe.Pointer(rsp + uintptr(i)*8)) = words[i]
	}
	t.SavedRSP = rsp
	t.State = uint32(KStateRunnable)
	return t
}

// kschedExit is the clean shutdown path for a running kernel thread.
// Marks the thread KStateExiting and yields; the scheduler reclaims
// the pool slot on the next pop attempt.
//
//go:nosplit
func kschedExit(code uintptr) {
	cpu := cpuID()
	t := kschedRunning[cpu]
	if t == nil {
		// Called from a context that isn't a managed kernel thread
		// (e.g. the TinyGo scheduler during M0 co-existence). Just
		// return; there's nothing to reclaim.
		return
	}
	t.ExitCode = code
	t.State = uint32(KStateExiting)
	// Switch back to the bootstrap context; kschedLoop sees
	// KStateExiting next iteration and frees the slot.
	kschedSwitch(&kschedBootstrap[cpu], t)
	// Never returns. If somehow it does, halt.
	for {
		hlt()
	}
}

// kschedExitNoreturn is called from the asm trampoline if an entry
// function ever returns. Wraps kschedExit(0) in a no-return shape
// so the asm jmp doesn't expose any stack-frame assumptions. The
// //export marker forces the symbol to be emitted under its bare
// name so kthread_switch.S can resolve it at link time.
//
//export kschedExitNoreturn
//go:nosplit
func kschedExitNoreturn() {
	kschedExit(0)
	for {
		hlt()
	}
}

// kschedPark releases the provided spinlock and parks the current
// thread. Waker must wake via kschedWake after removing the thread
// from whatever sync-primitive wait list it was on. Returns when
// the thread is scheduled again (after wake).
//
// Stubbed in M0 — concrete callers land in M3 with KEvent/KQueue.
//
//go:nosplit
func kschedPark(lock *Spinlock) {
	cpu := cpuID()
	t := kschedRunning[cpu]
	if t == nil {
		return
	}
	t.State = uint32(KStateParked)
	t.ParkLock = lock
	if lock != nil {
		// Caller passed the lock already acquired; drop it before
		// switching out so the waker can take it when it needs to
		// queue a wakeup.
		spinlockRelease(&lock.locked)
	}
	kschedSwitch(&kschedBootstrap[cpu], t)
	// Resumed (possibly on a different CPU). Re-install CR3+TSS
	// for the calling Ring-3-hosting kthread (no-op otherwise).
	// Must run BEFORE any further code that could trap to Ring 0
	// expecting valid TSS.RSP0 / CR3 (e.g., subsequent syscall).
	kthreadResumeRing3Ctx()
	// On resume: ParkLock is stale; clear it. Primitives that need
	// a re-acquired lock do so themselves (§03 condvar pattern).
	t.ParkLock = nil
}

// kschedWake transitions a Parked thread back to Runnable and pushes
// it onto its OwnerCPU queue. Stubbed in M0.
//
//go:nosplit
func kschedWake(t *KernelThread) {
	if t == nil {
		return
	}
	if t.State != uint32(KStateParked) {
		return
	}
	kschedPush(t, t.OwnerCPU)
}

// kthreadPoolExhaustedPanic halts with a visible banner. The workload
// is bounded at boot; exhaustion is a misconfiguration, not a
// recoverable condition.
//
//go:nosplit
func kthreadPoolExhaustedPanic() {
	off := 0
	off = appendStr(panicHexBuf[:], off, "KTHREAD POOL EXHAUSTED (cap=")
	off = appendDec(panicHexBuf[:], off, uint64(kthreadPoolCap))
	off = appendStr(panicHexBuf[:], off, ")")
	vgaWriteLine(15, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')
	for {
		hlt()
	}
}

// kschedRunEntry is the Go callback invoked from the asm
// kschedEnter trampoline. It receives the thread pointer (placed
// in %r13 by the initial frame) and invokes the Go func value
// directly so the compiler handles the fat-pointer calling
// convention for us. //export forces bare-name emission so the
// asm reference resolves at link time.
//
//export kschedRunEntry
//go:nosplit
func kschedRunEntry(t *KernelThread) {
	if t == nil || t.Entry == nil {
		return
	}
	t.Entry()
}
