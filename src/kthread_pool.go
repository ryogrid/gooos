// src/kthread_pool.go -- fixed-slot allocator for kernel-thread stacks.
//
// Route C's kernel threads are drawn from a bounded pool so the
// scheduler's alloc path is ISR-safe (no heap grow, no GC pause)
// and the collector's stack-range scan is a single slab walk.
//
// 32 × 16 KiB = 512 KiB reserved. Sized for the services enumerated
// in no_goroutine_kernel_design/06_service_migration.md (6 long-
// lived services + 8 concurrent ring3Wrapper hosts + headroom).

package main

import "unsafe"

// kthreadPoolCap is the number of slots. Conservative for M0 (the
// smoke test uses 2; full Route C needs ~15+). Sized per §02.
const kthreadPoolCap = 32

// kthreadPool is the .bss-resident slab. Each slot is one full
// KernelThread including its embedded 16 KiB KernelStack. The slab
// sits inside _globals_start..end by construction so the
// conservative GC scans it.
var kthreadPool [kthreadPoolCap]KernelThread

// kthreadPoolLock guards the Used bits and the free-slot scan.
// Unranked (higher than all other ranks in the Route C design; §03).
var kthreadPoolLock Spinlock

// kthreadPoolNextHint skips the leading occupied slots on the common
// path so we usually do O(1) allocation. Racey by intent; the scan
// re-verifies Used under the lock.
var kthreadPoolNextHint uint32

// kthreadPoolAlloc reserves a free slot and returns a pointer to the
// zero-initialised KernelThread. The embedded KernelStack.Top is
// set to the byte just past Pad; Canary is seeded with the sentinel.
// Returns nil on exhaustion (kernel halts — see kschedSpawn).
func kthreadPoolAlloc() *KernelThread {
	flags := kthreadPoolLock.Acquire()
	defer kthreadPoolLock.Release(flags)

	start := kthreadPoolNextHint
	for i := uint32(0); i < kthreadPoolCap; i++ {
		idx := (start + i) % kthreadPoolCap
		if kthreadPool[idx].Used != 0 {
			continue
		}
		t := &kthreadPool[idx]
		// Zero out the management fields. The Stack body is left
		// unzeroed — a newly spawned thread overwrites what it
		// reads first, and zeroing 16 KiB every spawn is wasteful.
		t.SavedRSP = 0
		t.State = 0
		t.OwnerCPU = 0
		t.Entry = nil
		t.WakeLink = nil
		t.ParkLock = nil
		t.Quantum = 0
		t.ExitCode = 0
		for j := range t.Name {
			t.Name[j] = 0
		}
		t.Slot = int32(idx)
		t.Used = 1
		// Seed the stack canary + compute Top.
		t.Stack.Canary = kernelStackCanary
		t.Stack.Top = uintptr(unsafe.Pointer(&t.Stack.Top))
		kthreadPoolNextHint = (idx + 1) % kthreadPoolCap
		return t
	}
	return nil
}

// kthreadPoolFree returns a slot to the pool. Caller must have
// already transitioned the thread to KStateExiting and must not
// reference the thread after this returns.
func kthreadPoolFree(t *KernelThread) {
	if t == nil || t.Slot < 0 {
		return
	}
	// Canary check: if the payload overran, halt rather than hand
	// a silently-corrupted slot back to the pool.
	if t.Stack.Canary != kernelStackCanary {
		kthreadCanaryPanic(t)
	}
	flags := kthreadPoolLock.Acquire()
	t.Used = 0
	slot := t.Slot
	t.Slot = -1
	if uint32(slot) < kthreadPoolNextHint {
		kthreadPoolNextHint = uint32(slot)
	}
	kthreadPoolLock.Release(flags)
}

// kthreadCanaryPanic is allocation-free; mirrors gooosStackOverflow.
//
//go:nosplit
func kthreadCanaryPanic(t *KernelThread) {
	off := 0
	off = appendStr(panicHexBuf[:], off, "KTHREAD CANARY CORRUPT: slot=")
	off = appendDec(panicHexBuf[:], off, uint64(uint32(t.Slot)))
	off = appendStr(panicHexBuf[:], off, " canary=")
	off = appendHex(panicHexBuf[:], off, uint64(t.Stack.Canary))
	vgaWriteLine(15, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')
	for {
		hlt()
	}
}
