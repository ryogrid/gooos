// src/process.go -- Process lifecycle management.
//
// Manages parent-child relationships for sys_exec / sys_exit.
// Tracks per-process user page mappings for cleanup on exit and
// save/restore when a parent executes a child.

package main

import "unsafe"

// Maximum user pages tracked per process.
const maxUserPages = 64

// Process holds per-process metadata beyond what Task provides.
type Process struct {
	TaskID       uint32
	ParentTaskID uint32      // 0xFFFFFFFF = no parent
	ExitCode     uintptr
	ArgString    [256]byte   // command-line arguments (fixed buffer)
	ArgLen       int
	UserPages    [maxUserPages]uintptr // virtual addresses of mapped user pages
	UserPaddrs   [maxUserPages]uintptr // corresponding physical addresses
	UserPageCnt  int
	HeapBreak    uintptr // current heap break (for sys_sbrk)
	EntryPoint   uintptr // ELF entry point (for trampoline)
	StackTop     uintptr // user stack top (for trampoline)
	Used         bool
}

// SavedMapping stores a parent's page mappings during child exec.
type SavedMapping struct {
	Vaddrs [maxUserPages]uintptr
	Paddrs [maxUserPages]uintptr
	Count  int
}

// Process table and parent save area.
var (
	processes   [maxTasks]Process
	savedParent SavedMapping // only one level of exec nesting
)

// noParent is the sentinel value for ParentTaskID indicating no parent.
const noParent = uint32(0xFFFFFFFF)

// Argument page virtual address: kernel writes arg string here before exec.
const argPageVaddr = uintptr(0x40300000)

// User stack base virtual address (2 pages = 8 KiB).
const userStackBase = uintptr(0x7FFF0000)

// processRecordPage records a virtual-to-physical mapping in the process table.
func processRecordPage(proc *Process, vaddr, paddr uintptr) {
	if proc.UserPageCnt < maxUserPages {
		proc.UserPages[proc.UserPageCnt] = vaddr
		proc.UserPaddrs[proc.UserPageCnt] = paddr
		proc.UserPageCnt++
	}
}

// elfExecTrampolineAddr returns the address of elfExecTrampoline.
// Implemented in switch.S.
//
//go:linkname elfExecTrampolineAddr elfExecTrampolineAddr
func elfExecTrampolineAddr() uintptr

// elfExecTrampoline is the Ring 0 entry point for a child task created
// by elfExec. It enables interrupts and jumps to Ring 3 at the child's
// ELF entry point.
//
//export elfExecTrampoline
func elfExecTrampoline() {
	sti()
	proc := &processes[currentTask]
	jumpToRing3(proc.EntryPoint, proc.StackTop)
}

// elfExec loads an ELF binary from the filesystem, saves the parent's
// page mappings, maps the child's segments, and creates a new task.
// The parent is blocked until the child exits. Returns the child task ID
// and true on success.
func elfExec(filename, args string, parentTaskID uint32) (uint32, bool) {
	// Read ELF binary from filesystem.
	data := fsSendRead(filename)
	if data == nil {
		serialPrintln("elfExec: file not found: " + filename)
		return 0, false
	}

	// Parse ELF headers.
	entry, phdrs, ok := elfParse(data)
	if !ok {
		serialPrintln("elfExec: invalid ELF: " + filename)
		return 0, false
	}

	// Save the parent's user page mappings.
	parentProc := &processes[parentTaskID]
	savedParent.Count = parentProc.UserPageCnt
	for i := 0; i < parentProc.UserPageCnt; i++ {
		savedParent.Vaddrs[i] = parentProc.UserPages[i]
		savedParent.Paddrs[i] = parentProc.UserPaddrs[i]
	}

	// Unmap the parent's user pages (but do NOT free physical pages).
	for i := 0; i < parentProc.UserPageCnt; i++ {
		unmapPage(parentProc.UserPages[i])
	}

	// Create a new task for the child.
	childID := createTask(elfExecTrampolineAddr())

	// Initialize the child's process entry.
	childProc := &processes[childID]
	childProc.TaskID = childID
	childProc.ParentTaskID = parentTaskID
	childProc.UserPageCnt = 0
	childProc.Used = true

	// Copy arguments.
	childProc.ArgLen = len(args)
	if childProc.ArgLen > 256 {
		childProc.ArgLen = 256
	}
	for i := 0; i < childProc.ArgLen; i++ {
		childProc.ArgString[i] = args[i]
	}

	userFlags := uintptr(pagePresent | pageWrite | pageUser)

	// Map and load each PT_LOAD segment.
	for i := 0; i < len(phdrs); i++ {
		ph := &phdrs[i]
		startPage := ph.Vaddr &^ (pageSize - 1)
		endAddr := ph.Vaddr + uintptr(ph.Memsz)

		for addr := startPage; addr < endAddr; addr += pageSize {
			paddr := allocPage()
			mapPage(addr, paddr, userFlags)
			processRecordPage(childProc, addr, paddr)
		}

		// Copy segment data.
		for j := uint64(0); j < ph.Filesz; j++ {
			*(*byte)(unsafe.Pointer(ph.Vaddr + uintptr(j))) = data[ph.Offset+j]
		}
	}

	// Allocate argument page and copy args.
	argPaddr := allocPage()
	mapPage(argPageVaddr, argPaddr, userFlags)
	processRecordPage(childProc, argPageVaddr, argPaddr)
	for i := 0; i < childProc.ArgLen; i++ {
		*(*byte)(unsafe.Pointer(argPageVaddr + uintptr(i))) = childProc.ArgString[i]
	}

	// Allocate user stack (2 pages at userStackBase).
	for i := uintptr(0); i < 2; i++ {
		paddr := allocPage()
		mapPage(userStackBase+i*pageSize, paddr, userFlags)
		processRecordPage(childProc, userStackBase+i*pageSize, paddr)
	}

	// Set process entry point and stack for the trampoline.
	childProc.EntryPoint = entry
	childProc.StackTop = userStackBase + 2*pageSize

	// Compute initial heap break (end of last PT_LOAD, page-aligned up).
	if len(phdrs) > 0 {
		lastPh := &phdrs[len(phdrs)-1]
		childProc.HeapBreak = (lastPh.Vaddr + uintptr(lastPh.Memsz) + pageSize - 1) &^ (pageSize - 1)
	}

	// Allow Ring 3 to trigger int 0x80.
	setGateDPL3(0x80)

	serialPrintln("elfExec: loaded " + filename + ", child task " + utoa(uint64(childID)))

	// Block the parent and schedule the child.
	tasks[parentTaskID].State = taskBlocked
	return childID, true
}

// processExit handles the termination of the current user process.
// Unmaps and frees child pages, restores parent mappings, and unblocks
// the parent. If there is no parent, halts the CPU.
func processExit(exitCode uintptr) {
	proc := &processes[currentTask]

	// Unmap and free all child user pages.
	for i := 0; i < proc.UserPageCnt; i++ {
		unmapPage(proc.UserPages[i])
		freePage(proc.UserPaddrs[i])
	}
	proc.UserPageCnt = 0
	proc.ExitCode = exitCode

	if proc.ParentTaskID != noParent {
		// Restore the parent's saved page mappings.
		parentProc := &processes[proc.ParentTaskID]
		userFlags := uintptr(pagePresent | pageWrite | pageUser)
		for i := 0; i < savedParent.Count; i++ {
			mapPage(savedParent.Vaddrs[i], savedParent.Paddrs[i], userFlags)
		}
		parentProc.UserPageCnt = savedParent.Count
		for i := 0; i < savedParent.Count; i++ {
			parentProc.UserPages[i] = savedParent.Vaddrs[i]
			parentProc.UserPaddrs[i] = savedParent.Paddrs[i]
		}

		// Unblock the parent and deliver the exit code.
		tasks[proc.ParentTaskID].State = taskReady

		serialPrintln("processExit: child " + utoa(uint64(currentTask)) +
			" exit code " + utoa(uint64(exitCode)) +
			", resuming parent " + utoa(uint64(proc.ParentTaskID)))
	} else {
		// No parent — halt the system.
		serialPrintln("processExit: no parent, halting")
		for {
			hlt()
		}
	}

	// Clean up the child's process entry.
	proc.Used = false

	// Reclaim the task slot and schedule.
	tasks[currentTask].State = taskExited
	taskReclaim(currentTask)
	schedule()
}
