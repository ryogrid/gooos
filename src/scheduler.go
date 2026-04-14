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
	taskExited  = 3
	taskFree    = 4
)

// Maximum number of tasks and per-task stack size.
const (
	maxTasks      = 32
	taskStackSize = 4096 // 4 KiB per task stack
)

// Task holds the state for a schedulable unit of execution.
type Task struct {
	SP         uintptr // saved stack pointer (set by switchContext)
	State      uint8   // taskRunning, taskReady, taskBlocked, taskExited, or taskFree
	ID         uint32  // task identifier
	StackBase  uintptr // base address of allocated stack page (for reclamation)
	WakeupTick uint64  // PIT tick at which a sleeping task should be woken
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
	// Try to reuse a free slot first.
	var id uint32
	reuse := false
	for i := uint32(1); i < taskCount; i++ {
		if tasks[i].State == taskFree {
			id = i
			reuse = true
			break
		}
	}
	if !reuse {
		id = taskCount
		taskCount++
	}

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

	tasks[id] = Task{SP: sp, State: taskReady, ID: id, StackBase: stackBase}
	return id
}

// taskReclaim frees the resources of an exited task and marks its slot
// as available for reuse. taskCount is a high-water mark and is never
// decremented.
func taskReclaim(id uint32) {
	if tasks[id].State != taskExited {
		return
	}
	if tasks[id].StackBase != 0 {
		freePage(tasks[id].StackBase)
	}
	tasks[id] = Task{State: taskFree}
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
	// Only move old task to ready if it was running (not if it blocked itself).
	if tasks[old].State == taskRunning {
		tasks[old].State = taskReady
	}
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

// demoTaskA writes an incrementing counter to VGA line 15.
//
//export demoTaskA
func demoTaskA() {
	sti() // Re-enable interrupts (we start from inside a timer ISR).
	var counter uint64
	for {
		counter++
		vgaWriteLine(15, "Task A: count="+utoa(counter))
		taskSleep(50) // ~500ms at 100 Hz
	}
}

// demoTaskB writes an incrementing counter to VGA line 16.
//
//export demoTaskB
func demoTaskB() {
	sti()
	var counter uint64
	for {
		counter++
		vgaWriteLine(16, "Task B: count="+utoa(counter))
		taskSleep(75) // ~750ms at 100 Hz
	}
}

// yield voluntarily relinquishes the CPU to the next ready task.
// The current task is set to taskReady and schedule() picks the next one.
// Callable from any kernel task context.
func yield() {
	tasks[currentTask].State = taskReady
	schedule()
}

// ---------- Sleep Queue ----------

// Maximum number of tasks that can be in the sleep queue simultaneously.
const sleepQueueMax = 16

// sleepQueue holds task IDs sorted by ascending WakeupTick.
var (
	sleepQueue      [sleepQueueMax]uint32
	sleepQueueCount int
)

// taskSleep blocks the current task for the given number of PIT ticks.
// Sets WakeupTick, inserts into the sorted sleep queue, and calls schedule().
func taskSleep(ticks uint64) {
	tid := currentTask
	tasks[tid].WakeupTick = pitTicks + ticks
	tasks[tid].State = taskBlocked

	// Insert into sleep queue in sorted order (ascending by WakeupTick).
	wt := tasks[tid].WakeupTick
	pos := sleepQueueCount
	for i := 0; i < sleepQueueCount; i++ {
		if tasks[sleepQueue[i]].WakeupTick > wt {
			pos = i
			break
		}
	}
	// Shift elements right to make room.
	for i := sleepQueueCount; i > pos; i-- {
		sleepQueue[i] = sleepQueue[i-1]
	}
	sleepQueue[pos] = tid
	sleepQueueCount++

	schedule()
}

// sleepQueueWakeExpired wakes all tasks whose WakeupTick <= now.
// Called from the timer IRQ handler. Safe from interrupt context.
func sleepQueueWakeExpired(now uint64) {
	woken := 0
	for woken < sleepQueueCount {
		tid := sleepQueue[woken]
		if tasks[tid].WakeupTick > now {
			break
		}
		tasks[tid].State = taskReady
		woken++
	}
	if woken > 0 {
		// Shift remaining entries forward.
		remaining := sleepQueueCount - woken
		for i := 0; i < remaining; i++ {
			sleepQueue[i] = sleepQueue[i+woken]
		}
		sleepQueueCount = remaining
	}
}

// ---------- WaitQueue ----------

// Maximum number of tasks that can be waiting in a single WaitQueue.
const wqMax = 16

// WaitQueue holds a FIFO list of task IDs waiting on a condition.
type WaitQueue struct {
	ids   [wqMax]uint32
	count int
}

// waitQueueSleep blocks the current task on wq and calls schedule().
// Must be called from task context (not interrupt context).
func waitQueueSleep(wq *WaitQueue) {
	if wq.count >= wqMax {
		return // queue full — drop (should not happen with 16 tasks max)
	}
	tid := currentTask
	wq.ids[wq.count] = tid
	wq.count++
	tasks[tid].State = taskBlocked
	schedule()
}

// waitQueueWakeOne dequeues the first waiting task and sets it to taskReady.
// Safe to call from interrupt context (no blocking, no allocation).
func waitQueueWakeOne(wq *WaitQueue) {
	if wq.count == 0 {
		return
	}
	tid := wq.ids[0]
	// Shift remaining entries forward.
	wq.count--
	for i := 0; i < wq.count; i++ {
		wq.ids[i] = wq.ids[i+1]
	}
	tasks[tid].State = taskReady
}

// waitQueueAppend adds a task ID to the wait queue without changing task state
// or calling schedule(). Used by selectWait to register on multiple queues.
func waitQueueAppend(wq *WaitQueue, tid uint32) {
	if wq.count >= wqMax {
		return
	}
	wq.ids[wq.count] = tid
	wq.count++
}

// waitQueueRemove removes a specific task ID from the wait queue.
// Used by selectWait to deregister from non-ready queues after wakeup.
func waitQueueRemove(wq *WaitQueue, tid uint32) {
	for i := 0; i < wq.count; i++ {
		if wq.ids[i] == tid {
			wq.count--
			for j := i; j < wq.count; j++ {
				wq.ids[j] = wq.ids[j+1]
			}
			return
		}
	}
}

// waitQueueWakeAll sets all queued tasks to taskReady and resets the queue.
// Safe to call from interrupt context (no blocking, no allocation).
func waitQueueWakeAll(wq *WaitQueue) {
	for i := 0; i < wq.count; i++ {
		tasks[wq.ids[i]].State = taskReady
	}
	wq.count = 0
}

// demoTaskC writes an incrementing counter to VGA line 17.
//
//export demoTaskC
func demoTaskC() {
	sti()
	var counter uint64
	for {
		counter++
		vgaWriteLine(17, "Task C: count="+utoa(counter))
		taskSleep(100) // ~1000ms at 100 Hz
	}
}
