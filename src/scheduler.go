// src/scheduler.go -- Preemptive round-robin scheduler.
//
// Defines the Task struct (ID, stack pointer, state), a fixed-size task table,
// and schedule() which performs round-robin selection and context switching.
// Context switch is done in assembly (switch.S) by saving/restoring
// callee-saved registers and RSP.

package main

import "unsafe"

// Task states.
const (
	taskRunning = 0
	taskReady   = 1
	taskBlocked = 2
)

// Maximum number of tasks and per-task stack size.
const (
	maxTasks      = 16
	taskStackSize = 4096 // 4 KiB per task stack
)

// Task holds the state for a schedulable unit of execution.
type Task struct {
	SP    uintptr // saved stack pointer (set by switchContext)
	State uint8   // taskRunning, taskReady, or taskBlocked
	ID    uint32  // task identifier
}

var (
	tasks       [maxTasks]Task
	taskCount   uint32
	currentTask uint32
	schedReady  bool
)

// switchContext saves callee-saved registers and RSP to *oldSP,
// loads newSP into RSP, and restores callee-saved registers.
// Implemented in switch.S.
//
//go:linkname switchContext switchContext
func switchContext(oldSP *uintptr, newSP uintptr)

// taskReturnHaltAddr returns the address of the taskReturnHalt stub.
// Implemented in switch.S.
//
//go:linkname taskReturnHaltAddr taskReturnHaltAddr
func taskReturnHaltAddr() uintptr

// initScheduler sets up task 0 as the currently running (main/boot) task.
func initScheduler() {
	tasks[0] = Task{State: taskRunning, ID: 0}
	taskCount = 1
	currentTask = 0
}

// createTask allocates a stack and initializes a new task that will
// begin execution at entryAddr when first scheduled.
//
// The initial stack layout (growing downward):
//
//	stackTop - 8:  taskReturnHalt  (safety net if entry returns)
//	stackTop - 16: entryAddr       (return address for switchContext's ret)
//	stackTop - 24: 0               (rbx)
//	stackTop - 32: 0               (rbp)
//	stackTop - 40: 0               (r12)
//	stackTop - 48: 0               (r13)
//	stackTop - 56: 0               (r14)
//	stackTop - 64: 0               (r15)
//	<-- SP saved here
func createTask(entryAddr uintptr) uint32 {
	id := taskCount
	taskCount++

	// Allocate a 4 KiB page for the task's stack.
	stackBase := allocPage()
	stackTop := stackBase + taskStackSize

	sp := stackTop

	// Safety return address: if the task function ever returns, halt.
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = taskReturnHaltAddr()

	// Entry point: switchContext's ret will jump here.
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = entryAddr

	// Callee-saved registers: rbx, rbp, r12, r13, r14, r15 (all zero).
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // rbx
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // rbp
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // r12
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // r13
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // r14
	sp -= 8
	*(*uintptr)(unsafe.Pointer(sp)) = 0 // r15

	tasks[id] = Task{SP: sp, State: taskReady, ID: id}
	return id
}

// schedule performs a round-robin context switch to the next ready task.
// Called from the PIT timer handler with interrupts disabled.
func schedule() {
	if !schedReady {
		return
	}
	if taskCount <= 1 {
		return
	}

	// Find next ready task (round-robin).
	old := currentTask
	next := (currentTask + 1) % taskCount
	for next != currentTask {
		if tasks[next].State == taskReady {
			break
		}
		next = (next + 1) % taskCount
	}

	if next == currentTask {
		return // No other ready task found.
	}

	// Update task states.
	tasks[old].State = taskReady
	tasks[next].State = taskRunning
	currentTask = next

	serialPrint("Switch: ")
	serialPrint(utoa(uint64(old)))
	serialPrint(" -> ")
	serialPrintln(utoa(uint64(next)))

	// Perform the context switch.
	switchContext(&tasks[old].SP, tasks[next].SP)
}

// demoTaskAAddr returns the address of demoTaskA. Implemented in switch.S.
//
//go:linkname demoTaskAAddr demoTaskAAddr
func demoTaskAAddr() uintptr

// demoTaskBAddr returns the address of demoTaskB. Implemented in switch.S.
//
//go:linkname demoTaskBAddr demoTaskBAddr
func demoTaskBAddr() uintptr

// demoTaskCAddr returns the address of demoTaskC. Implemented in switch.S.
//
//go:linkname demoTaskCAddr demoTaskCAddr
func demoTaskCAddr() uintptr

// demoTaskA writes an incrementing counter to VGA line 14.
//
//export demoTaskA
func demoTaskA() {
	sti() // Re-enable interrupts (we start from inside a timer ISR).
	var counter uint64
	for {
		counter++
		vgaWriteLine(14, "Task A: count="+utoa(counter))
		// Spin-wait ~500ms using PIT ticks.
		target := pitTicks + 50
		for pitTicks < target {
			hlt()
		}
	}
}

// demoTaskB writes an incrementing counter to VGA line 15.
//
//export demoTaskB
func demoTaskB() {
	sti()
	var counter uint64
	for {
		counter++
		vgaWriteLine(15, "Task B: count="+utoa(counter))
		target := pitTicks + 75
		for pitTicks < target {
			hlt()
		}
	}
}

// demoTaskC writes an incrementing counter to VGA line 16.
//
//export demoTaskC
func demoTaskC() {
	sti()
	var counter uint64
	for {
		counter++
		vgaWriteLine(16, "Task C: count="+utoa(counter))
		target := pitTicks + 100
		for pitTicks < target {
			hlt()
		}
	}
}
