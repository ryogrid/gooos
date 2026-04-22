// src/process.go -- Process lifecycle on goroutines (Phase B).
//
// Every Ring-3 user process runs inside a dedicated goroutine spawned
// by ring3Wrapper. TSS.RSP0 is updated per-goroutine via
// src/goroutine_tss.go. The parent goroutine blocks on an `exitCh`
// native channel until the child's processExit wakes it.
//
// Single-CPU v1 invariant: procByTask has one live entry per
// goroutine, no locking. Per-process PML4 (4e) replaced the
// previous savedParent global; each Process now owns its own
// address space.

package main

import "unsafe"

// maxUserPages bounds the per-process page tracking. Large enough to
// hold every PT_LOAD + stack + arg + heap page TinyGo's mmap may
// request (up to 256 pages for a 1 MiB initial heap).
const maxUserPages = 512

// userHeapLimit is the per-process heap ceiling enforced by
// sysSbrkHandler. 2 MiB = 1 MiB for the fixed .heap reservation
// in user/linker_user.ld plus 1 MiB of sbrk slack. Prevents a
// runaway user program from exhausting kernel physical memory
// via sys_sbrk in a loop. See impldoc/userspace_conservative_gc_runtime.md §4.
const userHeapLimit = 2 * 1024 * 1024

// Process is a Ring-3 process descriptor. Under Phase B it is no
// longer indexed by TaskID; instead procByTask maps a goroutine's
// task.Task pointer to its *Process.
type Process struct {
	ExitCode    uintptr
	Exited      uint32
	ArgString   [256]byte
	ArgLen      int
	UserPages   [maxUserPages]uintptr
	UserPaddrs  [maxUserPages]uintptr
	UserPageCnt int
	HeapBreak   uintptr
	HeapLimit   uintptr // sys_sbrk ceiling; HeapBreak + userHeapLimit on init
	EntryPoint  uintptr
	StackTop    uintptr

	parent  *Process     // nil for the boot shell
	exitCh  chan uintptr // parent waits here; child sends exit code
	poolIdx int          // ring3StackPool slot index, -1 if none

	// fds is the per-process file descriptor table. nil entries
	// are closed slots. Inherited shallow-copy on exec (each
	// FileDesc instance is shared with the parent until one side
	// closes). See impldoc/shell_io_fd_table.md.
	fds [procMaxFDs]FileDesc

	// pml4 is the physical address of this process's PML4 page.
	// Zero until 4e populates it via newProcPML4. The
	// gooosOnResume hook only swaps CR3 when pml4 != 0.
	// See impldoc/shell_io_multiprocess.md.
	pml4 uintptr

	// pid is the process identifier returned to userspace by
	// sys_spawn. Zero for the boot shell (which is launched
	// via elfLoad rather than elfSpawn).
	pid uint32

	// LastCpuID is the CPU index this process was most recently
	// resumed on. Updated from gooosOnResume (nosplit, unlocked
	// aligned u32 store). Consumed by sys_listprocs (feature 2.5,
	// impldoc/shell_ps_command.md §2.3).
	LastCpuID uint32

	// --- Signal-delivery state (feature 2.2 user preemption) -----------
	// Populated by sys_sigaction #35; consumed by maybeDeliverSignal
	// called from handlePreemptIPI when the interrupted context is
	// Ring 3 (CS.RPL == 3). See impldoc/preempt_user_goroutines.md.

	// SigAlrmHandler is the user-space address of the SIGALRM handler
	// registered by sys_sigaction(SIGALRM, handler). Zero = no handler
	// installed (no signal delivered even when UserPreemptPending is
	// set).
	SigAlrmHandler uintptr

	// UserPreemptPending is set to 1 by maybeSignalUserPreempt on the
	// BSP LAPIC timer tick when this process has accumulated
	// UserQuantumTicks ticks as the currently-running process. Cleared
	// when the kernel delivers the signal via maybeDeliverSignal.
	UserPreemptPending uint32

	// UserQuantumTicks is the number of BSP ticks between preempt
	// deliveries for this process. Default 10 (100ms at 100Hz).
	UserQuantumTicks uint32

	// UserQuantumCounter accumulates BSP ticks while this process is
	// the currently-running ring3 process on any CPU. Reset on
	// delivery.
	UserQuantumCounter uint32

	// SigInProgress is 1 while the user's SIGALRM handler is running.
	// Cleared by sys_sigreturn. maybeDeliverSignal early-returns when
	// this is set (no nested signal delivery).
	SigInProgress uint32

	// SigSavedRSP is the user RSP at the moment the kernel pushed the
	// sigFrame and redirected RIP to the SIGALRM handler. sys_sigreturn
	// uses THIS — not the user's current RSP — to locate the sigFrame,
	// because the Go-coded handler pushes its own locals/call-frames on
	// top during execution and cannot reliably restore RSP to the
	// sigFrame position before issuing the Sigreturn syscall.
	SigSavedRSP uintptr
}

// procLock protects procByTask, procByPID, nextPID, and
// foregroundProc for SMP safety. Lock ordering rank 2.
var procLock Spinlock

var (
	// procByTask maps a goroutine's *task.Task (as uintptr) to its
	// *Process. Populated by ring3Wrapper; consulted by any syscall
	// handler or kernel helper that needs the current process.
	procByTask map[uintptr]*Process

	// procByPID maps a PID to its *Process. Populated by elfSpawn,
	// removed by processWait (after the parent has reaped). Lets
	// sys_wait(pid) find the right child.
	procByPID map[uint32]*Process

	// nextPID is the monotonic PID allocator. Wraps at 2^32 which
	// is irrelevant for shell workloads. PID 0 is reserved as
	// "invalid".
	nextPID uint32 = 1
)

func ensureProcMaps() {
	if procByTask == nil {
		procByTask = make(map[uintptr]*Process)
	}
	if procByPID == nil {
		procByPID = make(map[uint32]*Process)
	}
}

// allocPID returns a fresh PID and bumps the counter.
// Caller must hold procLock.
func allocPID() uint32 {
	pid := nextPID
	nextPID++
	return pid
}

// foregroundProc is the process that owns the keyboard right
// now. consoleStdin.Read returns EOF to any other process,
// preventing two Ring-3 processes from racing on keyboardCh.
//
// Set initially by elfLoad (boot shell). Switched in
// processWait — the about-to-block parent transfers ownership
// to the child, then takes it back when the child exits.
var foregroundProc *Process

// setForegroundProc installs p as the keyboard owner.
// Protected by procLock.
func setForegroundProc(p *Process) {
	flags := procLock.Acquire()
	foregroundProc = p
	procLock.Release(flags)
}

// getForegroundProc returns the current foreground (or nil).
// Protected by procLock.
func getForegroundProc() *Process {
	flags := procLock.Acquire()
	p := foregroundProc
	procLock.Release(flags)
	return p
}

// Argument page virtual address: kernel writes arg string here
// before exec.
const argPageVaddr = uintptr(0x40300000)

// User stack base virtual address (2 pages = 8 KiB).
const userStackBase = uintptr(0x7FFF0000)

// currentProc returns the Process for the currently running
// goroutine, or nil if this is not a Ring-3-hosting goroutine.
// Protected by procLock.
func currentProc() *Process {
	flags := procLock.Acquire()
	ensureProcMaps()
	p := procByTask[taskCurrent()]
	procLock.Release(flags)
	return p
}

// setCurrentProc records proc as the Process for the current
// goroutine. Called once by ring3Wrapper per goroutine.
// Protected by procLock.
func setCurrentProc(proc *Process) {
	flags := procLock.Acquire()
	ensureProcMaps()
	procByTask[taskCurrent()] = proc
	procLock.Release(flags)
}

// clearCurrentProc removes the current goroutine's Process mapping.
// Called by processExit before the child goroutine halts.
// Protected by procLock.
func clearCurrentProc() {
	flags := procLock.Acquire()
	ensureProcMaps()
	delete(procByTask, taskCurrent())
	procLock.Release(flags)
}

// processRecordPage records a virtual-to-physical mapping in the
// process table for cleanup on exit / save on exec.
func processRecordPage(proc *Process, vaddr, paddr uintptr) {
	if proc.UserPageCnt < maxUserPages {
		proc.UserPages[proc.UserPageCnt] = vaddr
		proc.UserPaddrs[proc.UserPageCnt] = paddr
		proc.UserPageCnt++
	} else {
		serialPrintln("processRecordPage: OVERFLOW")
	}
}

// elfExecTrampolineAddr returns the address of elfExecTrampoline.
// Retained as a safety-net landing pad; ring3Wrapper does not use it
// but switch.S still exports the stub so the asm file keeps its
// single survivor symbol.
//
//go:linkname elfExecTrampolineAddr elfExecTrampolineAddr
func elfExecTrampolineAddr() uintptr

// elfExecTrampoline is a no-op under Phase B; the goroutine path
// (ring3Wrapper) replaces it. Keep the symbol so the asm
// elfExecTrampolineAddr stays resolvable until B10 drops it.
//
//export elfExecTrampoline
func elfExecTrampoline() {
	for {
		hlt()
	}
}

// ring3Wrapper is the goroutine entry for every Ring-3 process. It
// registers TSS.RSP0 for the pool-owned kernel stack and jumps to
// Ring 3. Never returns: the Ring-3 program exits via sys_exit →
// processExit, which sends on proc.exitCh and halts this goroutine.
//
// The pool slot is acquired here and released by processExit. See
// impldoc/deferred_stack_reclaim.md.
func ring3Wrapper(proc *Process) {
	serialPrint("ring3Wrapper: cpuID=")
	serialPrintln(utoa(uint64(cpuID())))
	ring3WrapperHandle = taskCurrent()
	idx, kernelStackTop := ring3StackAcquire()
	serialPrintln("ring3Wrapper: stackAcquired")
	proc.poolIdx = idx
	setProcByPoolSlot(idx, proc)                      // feature 2.2 ISR-safe lookup
	perCPUBlocks[cpuID()].CurrentPoolIdx = int32(idx) // feature 2.2 tick accounting
	setCurrentProc(proc)
	registerRing3GWithStack(kernelStackTop, proc)
	tssSetRSP0ForCurrentG()
	serialPrintln("ring3Wrapper: jumping to Ring 3")
	// Allow Ring 3 to trigger int 0x80 each time a Ring-3 goroutine
	// enters; safe to call repeatedly.
	setGateDPL3(0x80)
	// Switch into this process's PML4 before entering Ring 3.
	// gooosOnResume covers every subsequent goroutine resume, but
	// the very first scheduler dispatch fired before we registered
	// ourselves in gInfoByTask, so the hook short-circuited and the
	// boot PML4 is still active. Install the per-process PML4 now.
	if proc.pml4 != 0 {
		writeCR3(proc.pml4)
	}
	jumpToRing3(proc.EntryPoint, proc.StackTop)
	// unreachable
}

// elfSpawn loads an ELF binary, allocates a fresh PML4,
// populates the child's user pages via paddr-only writes, and
// spawns a ring3Wrapper goroutine for it. Returns the *Process
// immediately; the caller invokes processWait to block on the
// child's exit code.
//
// Per impldoc/shell_io_multiprocess.md §3, the kernel does NOT
// touch the parent's address space here. With per-process PML4
// the parent and child are in separate address spaces; no
// save/restore dance is needed.
//
// Hard rule: the kernel writes child page contents through the
// physical address returned by allocPage (identity-mapped in
// the boot kernel half). It never dereferences a vaddr that is
// only mapped in the child's PML4.
func elfSpawn(filename, args string, parent *Process) (*Process, bool) {
	data := fsSendRead(filename)
	if data == nil {
		serialPrintln("elfSpawn: file not found: " + filename)
		return nil, false
	}

	entry, phdrs, ok := elfParse(data)
	if !ok {
		serialPrintln("elfSpawn: invalid ELF: " + filename)
		return nil, false
	}

	child := &Process{
		parent:  parent,
		exitCh:  make(chan uintptr, 1),
		poolIdx: -1,
	}
	child.pml4 = newProcPML4()
	{
		fl := procLock.Acquire()
		ensureProcMaps()
		ensurePSMaps()
		child.pid = allocPID()
		procByPID[child.pid] = child
		setProcName(child.pid, filename) // feature 2.5: ps-command name column
		processStartTick[child.pid] = pitTicks
		procLock.Release(fl)
	}

	// fd inheritance — shallow copy of parent's table with a
	// refcount bump for each pipe end so the pipe survives
	// until the child closes on processExit.
	//
	// *socketFd slots are NOT inherited: the socket owns a
	// receive channel registered with udpBindWithChannel, and
	// whichever process exits first would call socketFd.Close →
	// udpUnbind and pull the binding out from under the other.
	// See impldoc/net_socket_api.md §12.4 and the Phase-5
	// reviewer pass. The child gets an empty slot instead.
	// Parent may be nil when an auto-launch hook (e.g. feature 2.2's
	// runUserPreemptProbe) spawns a child before any Ring-3 process
	// exists. In that case the child gets a fresh console-stdio set
	// instead of inheriting.
	if parent != nil {
		for i := 0; i < procMaxFDs; i++ {
			if _, isSock := parent.fds[i].(*socketFd); isSock {
				child.fds[i] = nil
				continue
			}
			child.fds[i] = parent.fds[i]
			fdAddRef(parent.fds[i])
		}
	} else {
		procInitStdio(child)
	}

	// Copy arguments into the Process struct (not user vaddrs
	// yet — that page-write happens via paddr below).
	child.ArgLen = len(args)
	if child.ArgLen > 256 {
		child.ArgLen = 256
	}
	for i := 0; i < child.ArgLen; i++ {
		child.ArgString[i] = args[i]
	}

	userFlags := uintptr(pagePresent | pageWrite | pageUser)

	// Map and load each PT_LOAD segment into the child's PML4.
	// Page contents are written through the paddr (identity-
	// mapped in the kernel half), never via the child vaddr.
	for i := 0; i < len(phdrs); i++ {
		ph := &phdrs[i]
		startPage := ph.Vaddr &^ (pageSize - 1)
		endAddr := ph.Vaddr + uintptr(ph.Memsz)

		for addr := startPage; addr < endAddr; addr += pageSize {
			if walkAndGetPaddrIn(child.pml4, addr) != 0 {
				continue // already mapped by an earlier segment
			}
			paddr := allocPage()
			mapPageInto(child.pml4, addr, paddr, userFlags)
			processRecordPage(child, addr, paddr)
		}

		for j := uint64(0); j < ph.Filesz; j++ {
			vaddr := ph.Vaddr + uintptr(j)
			paddr := walkAndGetPaddrIn(child.pml4, vaddr)
			off := paddr + (vaddr & (pageSize - 1))
			*(*byte)(unsafe.Pointer(off)) = data[ph.Offset+j]
		}
	}

	// Argument page.
	argPaddr := allocPage()
	mapPageInto(child.pml4, argPageVaddr, argPaddr, userFlags)
	processRecordPage(child, argPageVaddr, argPaddr)
	for i := 0; i < child.ArgLen; i++ {
		*(*byte)(unsafe.Pointer(argPaddr + uintptr(i))) = child.ArgString[i]
	}

	// User stack (4 pages). Keep initial RSP one page below mapped top
	// to tolerate boundary accesses near process start.
	for i := uintptr(0); i < 4; i++ {
		paddr := allocPage()
		mapPageInto(child.pml4, userStackBase+i*pageSize, paddr, userFlags)
		processRecordPage(child, userStackBase+i*pageSize, paddr)
	}

	child.EntryPoint = entry
	child.StackTop = userStackBase + 3*pageSize - 8

	if len(phdrs) > 0 {
		lastPh := &phdrs[len(phdrs)-1]
		child.HeapBreak = (lastPh.Vaddr + uintptr(lastPh.Memsz) + pageSize - 1) &^ (pageSize - 1)
		child.HeapLimit = child.HeapBreak + userHeapLimit
	}

	serialPrintln("elfSpawn: loaded " + filename)
	go ring3Wrapper(child)
	return child, true
}

// processWait blocks the caller until proc exits and returns
// the child's exit code. Reaps the entry from procByPID so a
// future sys_wait(pid) can't find it again.
//
// Foreground transfer (4h): the parent yields keyboard
// ownership to proc on entry, takes it back when proc exits.
// Background processes (those whose parent is not waiting on
// them) see EOF on stdin reads.
func processWait(proc *Process) uintptr {
	prevForeground := getForegroundProc()
	if runSMPProbeShellTest {
		prevPID := uint32(0)
		if prevForeground != nil {
			prevPID = prevForeground.pid
		}
		waitPID := uint32(0)
		if proc != nil {
			waitPID = proc.pid
		}
		serialPrintln("SHELLPROBE: fg_before_wait prev=" + utoa(uint64(prevPID)) +
			" wait=" + utoa(uint64(waitPID)))
	}
	setForegroundProc(proc)
	for proc.Exited == 0 {
		gooosSchedulerYield()
	}
	exitCode := proc.ExitCode
	setForegroundProc(prevForeground)
	serialPrintln("MARKER: M6 processWait post-exitCh-recv")
	if runSMPProbeShellTest {
		after := getForegroundProc()
		afterPID := uint32(0)
		if after != nil {
			afterPID = after.pid
		}
		restoredPID := uint32(0)
		if prevForeground != nil {
			restoredPID = prevForeground.pid
		}
		serialPrintln("SHELLPROBE: fg_after_wait restored=" + utoa(uint64(restoredPID)) +
			" current=" + utoa(uint64(afterPID)))
	}
	{
		fl := procLock.Acquire()
		delete(procByPID, proc.pid)
		clearProcName(proc.pid) // feature 2.5: ps-command name table cleanup
		delete(processStartTick, proc.pid)
		procLock.Release(fl)
	}
	if !firstExecAudited {
		firstExecAudited = true
		stackSizeAudit()
	}
	return exitCode
}

// elfExec is preserved as a thin spawn+wait wrapper so existing
// callers (sysExecHandler, the boot shell launcher) keep their
// synchronous semantics without change.
func elfExec(filename, args string, parent *Process) (uintptr, bool) {
	child, ok := elfSpawn(filename, args, parent)
	if !ok {
		return 0, false
	}
	return processWait(child), true
}

// firstExecAudited gates the post-exec stack-size audit so it
// fires exactly once. See impldoc/deferred_gc_and_stacks.md §4.3.
var firstExecAudited bool

// processExit terminates the current Ring-3 goroutine. Unmaps the
// child's pages, restores the parent's, wakes the parent via
// exitCh, then halts this goroutine forever.
func processExit(exitCode uintptr) {
	proc := currentProc()
	if proc == nil {
		serialPrintln("processExit: no current process, halting")
		for {
			hlt()
		}
	}

	serialPrintln("MARKER: M2 processExit pre-freePage")
	if runSMPShellPreemptProbe {
		for i := uint32(0); i < uint32(numCoresOnline); i++ {
			serialPrintln("APIDSTAT cpu=" + utoa(uint64(i)) +
				" apicid=" + utoa(uint64(perCPUBlocks[i].APICID)))
		}
		dumpPreemptCounters()
	}
	// Free the user physical pages. With per-process PML4 the
	// child's mappings live only in proc.pml4, so we don't have
	// to unmap from the active PML4 (which is also proc.pml4 at
	// this moment — about to be swapped + freed below).
	for i := 0; i < proc.UserPageCnt; i++ {
		freePage(proc.UserPaddrs[i])
	}
	proc.UserPageCnt = 0
	proc.ExitCode = exitCode
	proc.Exited = 1
	serialPrintln("MARKER: M3 processExit post-freePage")

	if proc.parent != nil {
		serialPrintln("processExit: child exit code " + utoa(uint64(exitCode)) +
			", resuming parent")
		serialPrintln("MARKER: M4 processExit pre-exitCh-send")
		proc.exitCh <- exitCode
		serialPrintln("MARKER: M5 processExit post-exitCh-send")
	} else {
		serialPrintln("processExit: no parent, halting")
	}

	// Switch CR3 back to the boot PML4 before freeing the
	// per-process PML4 — otherwise the kernel would be running
	// on freed pages once freeProcPML4 returned them to the
	// allocator. The kernel half is identity-mapped in both
	// PML4s so this swap is observationally a no-op for kernel
	// code, but it makes the freed pages safe to reuse.
	if proc.pml4 != 0 && bootPML4 != 0 {
		writeCR3(bootPML4)
		freeProcPML4(proc.pml4)
		proc.pml4 = 0
	}

	// Clear this goroutine's mapping and park forever. processExit
	// is reached from an int 0x80 ISR path, so in_interrupt_depth is
	// non-zero; task.Pause() would refuse with "blocked inside
	// interrupt". Decrement the counter first to simulate leaving
	// ISR context — safe because this goroutine will never return
	// to the ISR epilogue or Ring 3 anyway.
	unregisterRing3G()
	clearCurrentProc()
	procCloseAll(proc)
	if proc.poolIdx >= 0 {
		clearProcByPoolSlot(proc.poolIdx) // feature 2.2
		ring3StackRelease(proc.poolIdx)
		proc.poolIdx = -1
	}
	// This goroutine was entered from an int 0x80 ISR, so the ISR
	// prologue bumped %gs:4 (InterruptDepth) and %gs:44 (SyscallDepth).
	// The ISR epilogue on this goroutine's kernel stack will never
	// run (taskPause below parks forever). Decrement both per-CPU
	// counters now. The legacy global gooos_in_interrupt_depth was
	// retired in M2 (impldoc/smp_m2_ap_lapic_timer.md).
	idx := cpuID()
	if perCPUBlocks[idx].InterruptDepth > 0 {
		perCPUBlocks[idx].InterruptDepth--
	}
	if perCPUBlocks[idx].SyscallDepth > 0 {
		perCPUBlocks[idx].SyscallDepth--
	}
	taskPause() // never returns for this goroutine
	for {
		hlt()
	}
}
