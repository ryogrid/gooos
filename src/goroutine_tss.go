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
type gInfo struct {
	stackTop uintptr
}

// gInfoByTask maps task pointers to Ring-3 mapping entries. Only
// goroutines that execute Ring-3 code (i.e., ring3Wrapper) register
// here. Single-CPU v1 has no locking requirement.
var gInfoByTask = make(map[uintptr]*gInfo)

// taskCurrent bridges to internal/task.Current(). The TinyGo scheduler
// exposes the current task pointer through this function.
//
//go:linkname taskCurrent internal/task.Current
func taskCurrent() uintptr

// taskStackTop reads state.stackTop from a TinyGo Task pointer.
//
// Task struct layout under gc=conservative && !tinygo.wasm (amd64):
//   Next       *Task            offset  0 (size 8)
//   Ptr        unsafe.Pointer   offset  8 (size 8)
//   Data       uint64           offset 16 (size 8)
//   gcData     struct{}         offset 24 (size 0)
//   state.sp   uintptr          offset 24 (size 8)
//   state.canaryPtr *uintptr    offset 32 (size 8)
//   state.stackTop uintptr      offset 40 (size 8)  <- this
//
// Verified against internal/task/task.go + task_stack.go
// (patched) + gc_stack_noop.go. If TinyGo's layout shifts this must
// be updated.
func taskStackTop(t uintptr) uintptr {
	const stackTopOffset = 40
	return *(*uintptr)(unsafe.Pointer(t + stackTopOffset))
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
	gInfoByTask[t] = &gInfo{stackTop: taskStackTop(t)}
}

func unregisterRing3G() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	delete(gInfoByTask, t)
}

// tssSetRSP0ForCurrentG installs the current goroutine's stack top as
// TSS.RSP0. Only meaningful for Ring-3-bound goroutines; kernel-only
// goroutines skip this.
func tssSetRSP0ForCurrentG() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	gi := gInfoByTask[t]
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

// gooosOnResume is called from the patched internal/task.resume() on
// every goroutine switch. If the goroutine being resumed has a Ring-3
// mapping, TSS.RSP0 is pointed at its stack top before the CPU can
// dispatch any trap to it.
//
//go:linkname gooosOnResume runtime.gooosOnResume
//go:nosplit
func gooosOnResume() {
	t := taskCurrent()
	if t == 0 {
		return
	}
	gi := gInfoByTask[t]
	if gi == nil {
		return
	}
	tssSetRSP0(gi.stackTop)
}
