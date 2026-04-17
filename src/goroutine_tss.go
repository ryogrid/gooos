// src/goroutine_tss.go — TSS.RSP0 management for Ring-3 goroutines.
//
// Phase B replaces the custom scheduler's per-Task kernel stacks with
// per-goroutine stacks allocated by TinyGo's runtime. A Ring-3
// goroutine (spawned by ring3Wrapper in process.go) needs TSS.RSP0 to
// point at its own stack top so that int 0x80 / timer ISR transitions
// from Ring 3 land on the correct kernel stack.
//
// Two hooks:
//   - registerRing3G() records the current goroutine's stack top in a
//     side table at ring3Wrapper entry.
//   - gooosOnResume() is called from the patched
//     internal/task.resume() before every goroutine switch. If the
//     goroutine has a Ring-3 mapping, it sets TSS.RSP0 accordingly.
//
// See impldoc/phase_b_ring3_and_exec.md §4 for the full rationale.

package main

import (
	"unsafe"
)

// gInfo is the side-table entry keyed on a *task.Task pointer.
// stackTop is the high address of the goroutine's stack (lazy-read
// from the patched state struct in TinyGo's task_stack.go).
//
// proc is cached here so gooosOnResume (//go:nosplit) can swap
// CR3 without a second map lookup; map access from a nosplit
// hook is unsafe (TinyGo's hash path can call into the runtime
// allocator). See impldoc/shell_io_multiprocess.md §3.3.
type gInfo struct {
	stackTop uintptr
	proc     *Process
}

// gInfoLock protects gInfoByTask for SMP safety. Lock ordering rank 3.
var gInfoLock Spinlock

// gInfoByTask maps task pointers to Ring-3 mapping entries. Only
// goroutines that execute Ring-3 code (i.e., ring3Wrapper) register
// here. Protected by gInfoLock under SMP.
var gInfoByTask = make(map[uintptr]*gInfo)

// taskCurrent bridges to internal/task.Current(). The TinyGo scheduler
// exposes the current task pointer through this function.
//
//go:linkname taskCurrent internal/task.Current
func taskCurrent() uintptr

// stackTopOffset is the byte offset of state.stackTop inside a
// TinyGo Task struct on amd64 under gc=conservative +
// !tinygo.wasm (gcData is zero-sized via gc_stack_noop.go):
//
//	Next            *Task           offset  0 (size 8)
//	Ptr             unsafe.Pointer  offset  8 (size 8)
//	Data            uint64          offset 16 (size 8)
//	gcData          struct{}        offset 24 (size 0)
//	state.sp        uintptr         offset 24 (size 8)
//	state.canaryPtr *uintptr        offset 32 (size 8)
//	state.stackTop  uintptr         offset 40 (size 8)  <-- this
//
// If this ever shifts, `checkTaskOffset` below halts at boot with
// a clear message before anything else tries to dereference a bad
// pointer.
const stackTopOffset = 40

// taskStackTop reads state.stackTop from a TinyGo Task pointer.
func taskStackTop(t uintptr) uintptr {
	return *(*uintptr)(unsafe.Pointer(t + stackTopOffset))
}

// checkTaskOffset is a cheap self-test called at boot (from
// main.go) that traps immediately if the Task layout assumption
// above is wrong — e.g., if a TinyGo upgrade changes the struct.
// The check works by spawning a throwaway goroutine that writes
// its own canaryPtr into a local var; the layout is consistent
// iff taskStackTop of that goroutine points strictly above its
// canary.
func checkTaskOffset() {
	done := make(chan struct{}, 1)
	go func() {
		t := taskCurrent()
		top := taskStackTop(t)
		// canaryPtr field is at offset stackTopOffset - 8.
		canary := *(**uintptr)(unsafe.Pointer(t + stackTopOffset - 8))
		if top == 0 || canary == nil || top <= uintptr(unsafe.Pointer(canary)) {
			serialPrintln("FATAL: TinyGo Task layout mismatch (stackTop offset)")
			for {
				hlt()
			}
		}
		done <- struct{}{}
	}()
	<-done
}

// registerRing3G records the current goroutine as Ring-3-bound. Must
// be called from ring3Wrapper before jumpToRing3 and before the first
// goroutine yield. unregisterRing3G undoes the record on process
// exit.
func registerRing3G() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	fl := gInfoLock.Acquire()
	gInfoByTask[t] = &gInfo{stackTop: taskStackTop(t)}
	gInfoLock.Release(fl)
}

// registerRing3GWithStack is like registerRing3G but uses a
// caller-supplied kernel stack top and a *Process pointer.
// Cached on gInfo so the nosplit gooosOnResume hook can swap
// CR3 without consulting procByTask. Used by ring3Wrapper
// when the kernel stack comes from ring3StackPool — see
// impldoc/deferred_stack_reclaim.md §4.2 and
// impldoc/shell_io_multiprocess.md §3.3.
func registerRing3GWithStack(stackTop uintptr, proc *Process) {
	t := taskCurrent()
	if t == 0 {
		return
	}
	fl := gInfoLock.Acquire()
	gInfoByTask[t] = &gInfo{stackTop: stackTop, proc: proc}
	gInfoLock.Release(fl)
}

func unregisterRing3G() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	fl := gInfoLock.Acquire()
	delete(gInfoByTask, t)
	gInfoLock.Release(fl)
}

// tssSetRSP0ForCurrentG installs the current goroutine's stack top as
// TSS.RSP0. Only meaningful for Ring-3-bound goroutines; kernel-only
// goroutines skip this.
func tssSetRSP0ForCurrentG() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	fl := gInfoLock.Acquire()
	gi := gInfoByTask[t]
	gInfoLock.Release(fl)
	if gi == nil {
		return
	}
	tssSetRSP0(gi.stackTop)
}

// taskPause bridges internal/task.Pause(). Used by processExit to
// park the child goroutine after it has delivered its exit code.
//
//go:linkname taskPause internal/task.Pause
func taskPause()

// gooosOnResume is called from the patched internal/task.resume()
// on every goroutine switch. If the goroutine being resumed is
// Ring-3-bound, TSS.RSP0 is pointed at its kernel stack top and
// (post-4d) CR3 is swapped to its per-process PML4 before the
// CPU can dispatch any trap to it.
//
// nosplit: must not allocate, must not park, must not call into
// any function that might grow the goroutine stack. The single
// `gInfoByTask[t]` lookup matches the pre-4d cost; the gi.proc
// access that follows is a plain pointer field load, and
// writeCR3 is one asm instruction.
//
// First-resume invariant: ring3Wrapper calls
// registerRing3GWithStack BEFORE its own first writeCR3, but
// the very first scheduler resume of the wrapper goroutine
// happens before that (the wrapper hasn't run yet). On that
// first fire, gInfoByTask[t] is nil; the gi == nil short-
// circuit returns without touching TSS.RSP0 or CR3, leaving
// the boot PML4 active. Safe because the wrapper prologue
// only touches kernel-half memory until it explicitly
// writeCR3's into the proc's PML4.
//
//go:linkname gooosOnResume runtime.gooosOnResume
//go:nosplit
func gooosOnResume() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	fl := gInfoLock.Acquire()
	gi := gInfoByTask[t]
	gInfoLock.Release(fl)
	if gi == nil {
		return
	}
	tssSetRSP0(gi.stackTop)
	// Install the correct PML4 for this Ring-3 goroutine. A
	// non-zero proc.pml4 is a per-process PML4 from newProcPML4
	// (elfSpawn path). The boot shell (launched via elfLoad
	// before per-process PML4 landed) has pml4=0; fall back to
	// bootPML4 in that case so CR3 doesn't stay pointing at
	// some OTHER process's PML4 that the scheduler just
	// switched out of.
	if gi.proc != nil {
		pml4 := gi.proc.pml4
		if pml4 == 0 {
			pml4 = bootPML4
		}
		if pml4 != 0 {
			writeCR3(pml4)
		}
	}
}
