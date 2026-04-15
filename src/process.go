// src/process.go -- Process lifecycle on goroutines (Phase B).
//
// Every Ring-3 user process runs inside a dedicated goroutine spawned
// by ring3Wrapper. TSS.RSP0 is updated per-goroutine via
// src/goroutine_tss.go. The parent goroutine blocks on an `exitCh`
// native channel until the child's processExit wakes it.
//
// Single-CPU v1 invariant: only one exec is in flight at a time, so
// the global `savedParent` and `procByTask` map have one live entry
// per process without locking.

package main

import "unsafe"

// maxUserPages bounds the per-process page tracking. Large enough to
// hold every PT_LOAD + stack + arg + heap page TinyGo's mmap may
// request (up to 256 pages for a 1 MiB initial heap).
const maxUserPages = 512

// Process is a Ring-3 process descriptor. Under Phase B it is no
// longer indexed by TaskID; instead procByTask maps a goroutine's
// task.Task pointer to its *Process.
type Process struct {
	ExitCode    uintptr
	ArgString   [256]byte
	ArgLen      int
	UserPages   [maxUserPages]uintptr
	UserPaddrs  [maxUserPages]uintptr
	UserPageCnt int
	HeapBreak   uintptr
	EntryPoint  uintptr
	StackTop    uintptr

	parent  *Process     // nil for the boot shell
	exitCh  chan uintptr // parent waits here; child sends exit code
	poolIdx int          // ring3StackPool slot index, -1 if none
}

// SavedMapping caches a parent's page mappings during child exec.
type SavedMapping struct {
	Vaddrs [maxUserPages]uintptr
	Paddrs [maxUserPages]uintptr
	Count  int
}

var (
	// procByTask maps a goroutine's *task.Task (as uintptr) to its
	// *Process. Populated by ring3Wrapper; consulted by any syscall
	// handler or kernel helper that needs the current process.
	procByTask = make(map[uintptr]*Process)

	// savedParent caches the parent's page mappings across exec.
	// Single-global because v1 supports one level of exec nesting.
	savedParent SavedMapping
)

// Argument page virtual address: kernel writes arg string here
// before exec.
const argPageVaddr = uintptr(0x40300000)

// User stack base virtual address (2 pages = 8 KiB).
const userStackBase = uintptr(0x7FFF0000)

// currentProc returns the Process for the currently running
// goroutine, or nil if this is not a Ring-3-hosting goroutine.
func currentProc() *Process {
	return procByTask[taskCurrent()]
}

// setCurrentProc records proc as the Process for the current
// goroutine. Called once by ring3Wrapper per goroutine.
func setCurrentProc(proc *Process) {
	procByTask[taskCurrent()] = proc
}

// clearCurrentProc removes the current goroutine's Process mapping.
// Called by processExit before the child goroutine halts.
func clearCurrentProc() {
	delete(procByTask, taskCurrent())
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
	ring3WrapperHandle = taskCurrent()
	idx, kernelStackTop := ring3StackAcquire()
	proc.poolIdx = idx
	setCurrentProc(proc)
	registerRing3GWithStack(kernelStackTop)
	tssSetRSP0ForCurrentG()
	// Allow Ring 3 to trigger int 0x80 each time a Ring-3 goroutine
	// enters; safe to call repeatedly.
	setGateDPL3(0x80)
	jumpToRing3(proc.EntryPoint, proc.StackTop)
	// unreachable
}

// elfExec loads an ELF binary, maps its segments, spawns a
// ring3Wrapper goroutine for it, and blocks until the child exits.
// Returns the child's exit code and true on success.
func elfExec(filename, args string, parent *Process) (uintptr, bool) {
	data := fsSendRead(filename)
	if data == nil {
		serialPrintln("elfExec: file not found: " + filename)
		return 0, false
	}

	entry, phdrs, ok := elfParse(data)
	if !ok {
		serialPrintln("elfExec: invalid ELF: " + filename)
		return 0, false
	}

	// Save the parent's user page mappings.
	savedParent.Count = parent.UserPageCnt
	for i := 0; i < parent.UserPageCnt; i++ {
		savedParent.Vaddrs[i] = parent.UserPages[i]
		savedParent.Paddrs[i] = parent.UserPaddrs[i]
	}

	// Unmap parent's user pages (physical pages survive — they will
	// be re-mapped on processExit).
	for i := 0; i < parent.UserPageCnt; i++ {
		unmapPage(parent.UserPages[i])
	}

	child := &Process{
		parent:  parent,
		exitCh:  make(chan uintptr, 1),
		poolIdx: -1, // populated by ring3Wrapper from ring3StackPool
	}

	// Copy arguments.
	child.ArgLen = len(args)
	if child.ArgLen > 256 {
		child.ArgLen = 256
	}
	for i := 0; i < child.ArgLen; i++ {
		child.ArgString[i] = args[i]
	}

	userFlags := uintptr(pagePresent | pageWrite | pageUser)

	// Map and load each PT_LOAD segment.
	for i := 0; i < len(phdrs); i++ {
		ph := &phdrs[i]
		startPage := ph.Vaddr &^ (pageSize - 1)
		endAddr := ph.Vaddr + uintptr(ph.Memsz)

		for addr := startPage; addr < endAddr; addr += pageSize {
			if walkAndGetPaddr(addr) != 0 {
				continue
			}
			paddr := allocPage()
			mapPage(addr, paddr, userFlags)
			processRecordPage(child, addr, paddr)
		}

		for j := uint64(0); j < ph.Filesz; j++ {
			*(*byte)(unsafe.Pointer(ph.Vaddr + uintptr(j))) = data[ph.Offset+j]
		}
	}

	// Argument page.
	argPaddr := allocPage()
	mapPage(argPageVaddr, argPaddr, userFlags)
	processRecordPage(child, argPageVaddr, argPaddr)
	for i := 0; i < child.ArgLen; i++ {
		*(*byte)(unsafe.Pointer(argPageVaddr + uintptr(i))) = child.ArgString[i]
	}

	// User stack (2 pages).
	for i := uintptr(0); i < 2; i++ {
		paddr := allocPage()
		mapPage(userStackBase+i*pageSize, paddr, userFlags)
		processRecordPage(child, userStackBase+i*pageSize, paddr)
	}

	child.EntryPoint = entry
	child.StackTop = userStackBase + 2*pageSize

	if len(phdrs) > 0 {
		lastPh := &phdrs[len(phdrs)-1]
		child.HeapBreak = (lastPh.Vaddr + uintptr(lastPh.Memsz) + pageSize - 1) &^ (pageSize - 1)
	}

	serialPrintln("elfExec: loaded " + filename)

	// Spawn the Ring-3 goroutine and wait for it to send on exitCh.
	go ring3Wrapper(child)
	exitCode := <-child.exitCh
	if !firstExecAudited {
		firstExecAudited = true
		stackSizeAudit()
	}
	return exitCode, true
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

	for i := 0; i < proc.UserPageCnt; i++ {
		unmapPage(proc.UserPages[i])
		freePage(proc.UserPaddrs[i])
	}
	proc.UserPageCnt = 0
	proc.ExitCode = exitCode

	if proc.parent != nil {
		userFlags := uintptr(pagePresent | pageWrite | pageUser)
		for i := 0; i < savedParent.Count; i++ {
			mapPage(savedParent.Vaddrs[i], savedParent.Paddrs[i], userFlags)
		}
		proc.parent.UserPageCnt = savedParent.Count
		for i := 0; i < savedParent.Count; i++ {
			proc.parent.UserPages[i] = savedParent.Vaddrs[i]
			proc.parent.UserPaddrs[i] = savedParent.Paddrs[i]
		}
		serialPrintln("processExit: child exit code " + utoa(uint64(exitCode)) +
			", resuming parent")
		proc.exitCh <- exitCode
	} else {
		serialPrintln("processExit: no parent, halting")
	}

	// Clear this goroutine's mapping and park forever. processExit
	// is reached from an int 0x80 ISR path, so in_interrupt_depth is
	// non-zero; task.Pause() would refuse with "blocked inside
	// interrupt". Decrement the counter first to simulate leaving
	// ISR context — safe because this goroutine will never return
	// to the ISR epilogue or Ring 3 anyway.
	unregisterRing3G()
	clearCurrentProc()
	if proc.poolIdx >= 0 {
		ring3StackRelease(proc.poolIdx)
		proc.poolIdx = -1
	}
	// This goroutine was entered from an int 0x80 ISR, so the ISR
	// prologue bumped gooos_in_interrupt_depth. The ISR epilogue on
	// this goroutine's kernel stack will never run (taskPause below
	// parks forever). Decrement by 1 to represent leaving THIS
	// ISR frame without underflowing any outer nesting level.
	if gooosInInterruptDepth > 0 {
		gooosInInterruptDepth--
	}
	taskPause() // never returns for this goroutine
	for {
		hlt()
	}
}
