// src/kthread.go -- Kernel-thread types for Route C.
//
// Route C replaces TinyGo goroutines in Ring 0 with gooos-owned
// kernel threads. Each service / Ring-3 process host runs as one
// KernelThread scheduled by src/kthread_sched.go; park/wake on §03
// sync primitives; context-switched by src/kthread_switch.S.
//
// This file defines the three core types (KState, KernelThread,
// KernelStack) and a boot-time safety check
// (checkKernelThreadOffset) that verifies the Go struct layout
// matches the hard-coded offsets in the asm stub.
//
// Design: no_goroutine_kernel_design/02_kernel_thread_runtime.md.

package main

import "unsafe"

// KState is the kernel-thread lifecycle state. uint32 so the asm
// stub can CAS it without a type alias.
type KState uint32

const (
	KStateNew       KState = 0 // spawned, not yet enqueued
	KStateRunnable  KState = 1 // on a ready queue
	KStateRunning   KState = 2 // currently executing on some CPU
	KStateParked    KState = 3 // waiting on a sync primitive (§03)
	KStatePreempted KState = 4 // timer ISR demanded a switch; see §04
	KStateExiting   KState = 5 // terminal; slot to be reclaimed
)

// KernelThread is one gooos-owned kernel thread. Pool-allocated via
// src/kthread_pool.go — never heap-allocated because the embedded
// KernelStack is backing storage the asm stub writes into.
//
// Asm-visible offsets (kthread_switch.S reads these):
//
//	offset 0:  SavedRSP uintptr
//	offset 8:  State    uint32
//	offset 12: OwnerCPU uint32
//
// checkKernelThreadOffset() asserts these at boot.
type KernelThread struct {
	SavedRSP uintptr // offset 0:  parked RSP
	State    uint32  // offset 8:  KState
	OwnerCPU uint32  // offset 12: last / current CPU index

	// ---- Go-only fields ----

	// Stack is the backing kernel stack for this thread. 16 KiB.
	// Layout: canary + pad + top. The thread runs with RSP inside
	// the pad region.
	Stack KernelStack

	// Name is a null-padded identifier for debug dumps (kps equivalent).
	Name [16]byte

	// Entry is the thread body function. Retained for debug; the
	// asm stub loads the entry pointer from %r12 (written into the
	// initial frame at spawn).
	Entry func()

	// WakeLink is an intrusive link used by §03 wait queues and by
	// kschedReadyQueue. A thread is on at most one list at a time.
	WakeLink *KernelThread

	// ParkLock is the spinlock held by the primitive this thread
	// is parked on (§03). nil when Runnable or Running.
	ParkLock *Spinlock

	// Quantum is remaining ticks before preempt (§04). Reset to
	// kschedDefaultQuantum on switch-in.
	Quantum uint32

	// ExitCode is the value passed to kschedExit; read by joiners
	// (e.g. processWait in §07).
	ExitCode uintptr

	// Slot is the pool index in kthreadPool; -1 for non-pool
	// entries (currently only the bootstrap / idle threads).
	Slot int32

	// Used marks the pool slot as live. Protected by
	// kthreadPoolLock in src/kthread_pool.go.
	Used uint32
}

// kernelStackBytes is the KernelStack payload region size. 16 KiB
// total minus the Canary (8 B) and Top (8 B) bookkeeping words.
const kernelStackBytes = 16*1024 - 16

// kernelStackWords is kernelStackBytes / 8; used as the Pad array
// length. Must satisfy 8*kernelStackWords == kernelStackBytes.
const kernelStackWords = kernelStackBytes / 8

// KernelStack is the fixed-size stack backing for one KernelThread.
// Canary at the low end catches overrun before it corrupts the next
// slot in the pool. Top is set at Init time to the address just
// past Pad.
type KernelStack struct {
	Canary uintptr                   // sentinel; checked by kschedExit
	Pad    [kernelStackWords]uintptr // actual stack bytes
	Top    uintptr                   // &Pad[len-1]+8 after Init
}

// kernelStackCanary is the sentinel value written into
// KernelStack.Canary at Init. A mismatch at exit means a stack
// overflow corrupted the word.
const kernelStackCanary uintptr = 0xC0DE57AC4CA11ADE

// Asm-visible byte offsets (must match the KernelThread layout
// above). kthread_switch.S references them as numeric constants;
// keeping them named Go constants lets checkKernelThreadOffset()
// verify them.
const (
	kthreadOffSavedRSP = 0
	kthreadOffState    = 8
	kthreadOffOwnerCPU = 12
)

// checkKernelThreadOffset panics if the Go struct layout drifts
// from the asm stub's expectations. Called once from main() before
// the scheduler loop runs.
func checkKernelThreadOffset() {
	var kt KernelThread
	base := uintptr(unsafe.Pointer(&kt))
	if uintptr(unsafe.Pointer(&kt.SavedRSP))-base != kthreadOffSavedRSP {
		kthreadOffsetPanic("SavedRSP")
	}
	if uintptr(unsafe.Pointer(&kt.State))-base != kthreadOffState {
		kthreadOffsetPanic("State")
	}
	if uintptr(unsafe.Pointer(&kt.OwnerCPU))-base != kthreadOffOwnerCPU {
		kthreadOffsetPanic("OwnerCPU")
	}
}

// kthreadOffsetPanic prints the offending field name and halts.
// Allocation-free; uses the panicHexBuf formatting helpers.
//
//go:nosplit
func kthreadOffsetPanic(field string) {
	off := 0
	off = appendStr(panicHexBuf[:], off, "KTHREAD OFFSET MISMATCH: ")
	off = appendStr(panicHexBuf[:], off, field)
	vgaWriteLine(15, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')
	for {
		hlt()
	}
}
