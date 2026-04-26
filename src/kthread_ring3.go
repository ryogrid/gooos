// src/kthread_ring3.go -- Route C M4.1: ring3Wrapper as kernel thread.
//
// Side-table approach: kthreadHostedProc[slot] maps a kthread pool
// slot -> the *Process it hosts. Updated by kschedSpawnRing3Wrapper
// at spawn time; cleared by processExit's kthread branch.
//
// CR3 + TSS.RSP0 install runs at the top of ring3WrapperKT and
// at every park-then-resume site (kschedYield, kschedPark,
// KEvent.Wait, fsReqQueue.Push/Pop) via kthreadResumeRing3Ctx.
// We intentionally do NOT add the call inside kschedLoopOnce or
// kschedLoop — attempt 2 hit a non-deterministic boot regression
// when any function call was added there. See
// no_goroutine_kernel_design/12_implementation_notes.md §M4.1.
//
// M4.1.b lands cross-CPU re-install: when a kthread parks and is
// re-dispatched on a different CPU, the new CPU's TSS.RSP0 is
// stale and CR3 may have been changed by an intervening goroutine
// (via gooosOnResume). The post-resume hook re-installs both
// before any Ring-3 trap can land on the wrong stack/PML4.

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
	t := kschedSpawnInternal("ring3", ring3WrapperKT)
	// Record the proc BEFORE the push so a wake on the target CPU
	// can resolve the proc as soon as the kthread is dispatched.
	kthreadHostedProc[t.Slot] = proc
	// §15 §3.1 / §16 Step 4: spawn placement.
	// - userspaceSMP=true: round-robin onto AP queues
	//   (1..numCoresOnline-1) via the Ring-3 tier. BSP queue 0
	//   is reserved for the boot shell (R4); exec'd children
	//   skip it.
	// - userspaceSMP=false (M6 fallback): land on BSP via the
	//   Ring-3 tier, processed by the BSP combined pump (Step 3).
	// - uniprocessorKernel=false (legacy SMP M5; never combined
	//   with M7 here): pre-§14 round-robin onto kschedQueues
	//   service tier — kept as dead code under flag.
	target := uint32(0)
	if userspaceSMP && numCoresOnline > 1 {
		target = 1 + (kschedSpawnRRCounter % (numCoresOnline - 1))
		kschedSpawnRRCounter++
	} else if !uniprocessorKernel {
		target = kschedSpawnRRCounter
		kschedSpawnRRCounter++
		if numCoresOnline == 0 {
			target = 0
		} else {
			target = target % numCoresOnline
		}
		kschedPush(t, target) // legacy service-tier path
		return t
	}
	kschedPushRing3(t, target)
	return t
}

// kschedSpawnRing3WrapperOnBSP pins the kthread to CPU 0. Used for
// the boot shell so the BSP elf.go combined pump dispatches it via
// kschedLoopRing3OnlyOnce(0).
//
// §15 §3.1 R4 / §16 Step 4: switch from kschedPush(t, 0) (service
// tier) to kschedPushRing3(t, 0) (Ring-3 tier). The BSP combined
// pump (src/elf.go) drives both tiers, so the boot shell is still
// dispatched on BSP either way — but routing it through the
// Ring-3 tier keeps R3 ("only ring3WrapperKT instances on Ring-3
// queues") a meaningful invariant and aligns the boot shell with
// the rest of M7.
func kschedSpawnRing3WrapperOnBSP(proc *Process) *KernelThread {
	t := kschedSpawnInternal("ring3", ring3WrapperKT)
	kthreadHostedProc[t.Slot] = proc
	kschedPushRing3(t, 0)
	return t
}

// kthreadResumeRing3Ctx installs CR3 + TSS.RSP0 + per-CPU pool-slot
// for the kthread currently running on this CPU. Called at first
// dispatch (from ring3WrapperKT) AND after every park-then-resume
// kschedSwitch in caller code (kschedYield, kschedPark, KEvent.Wait,
// fsReqQueue.Push/Pop). No-op if the running kthread is not a
// Ring-3 host (proc nil), which covers fsTask and other service
// kthreads.
//
// nosplit: walks pointer side tables and writes CR3 — must not
// allocate, park, or take a goroutine stack-grow path.
//
//go:nosplit
func kthreadResumeRing3Ctx() {
	cpu := cpuID()
	if cpu >= maxCPUs {
		return
	}
	t := kschedRunning[cpu]
	if t == nil || t.Slot < 0 || int(t.Slot) >= kthreadPoolCap {
		return
	}
	proc := kthreadHostedProc[t.Slot]
	if proc == nil {
		return
	}
	perCPUBlocks[cpu].CurrentPoolIdx = int32(t.Slot)
	if proc.pml4 != 0 {
		writeCR3(proc.pml4)
	}
	tssSetRSP0(uintptr(unsafe.Pointer(&t.Stack.Top)))
}

// ring3WrapperKT is the kthread entry point for a Ring-3 process.
// One-shot setup (proc.poolIdx + procByPoolSlot + setCurrentProc),
// then kthreadResumeRing3Ctx for the per-resume install (also fired
// on first dispatch). Never returns in the success path -- Ring 3
// -> processExit -> kschedExit (via the kthread branch in
// processExit).
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

	// One-shot bookkeeping (not in kthreadResumeRing3Ctx because
	// it's idempotent but unnecessary on every resume).
	proc.poolIdx = int(t.Slot)
	setProcByPoolSlot(int(t.Slot), proc)

	// Bridge currentProc() lookups: syscall handlers call
	// currentProc() which reads procByTask[taskCurrent()]. From a
	// kthread context taskCurrent() returns the per-CPU stale
	// TinyGo task (whatever was running when waitForEvents called
	// kschedLoopOnce). Storing under that key lets syscall ISRs
	// running on this kthread's stack resolve the proc through the
	// usual path.
	setCurrentProc(proc)

	// First-dispatch install (also re-fires on every wake via
	// kthreadResumeRing3Ctx at the post-kschedSwitch sites).
	kthreadResumeRing3Ctx()

	serialPrintln("ring3WrapperKT: jumping to Ring 3 entry=0x" + hextoa(uint64(proc.EntryPoint)))
	setGateDPL3(0x80)
	jumpToRing3(proc.EntryPoint, proc.StackTop)
}
