// src/ps.go — sys_listprocs (syscall 37) backing the `ps` user command.
//
// Enumerates the kernel process table into a user-supplied buffer of
// fixed-layout ProcInfo structs. Lock ordering: procLock (rank 2) is
// held only for the snapshot; user-PML4 writes happen after release
// so a concurrent user page fault cannot deadlock against the lock.
//
// See impldoc/shell_ps_command.md §2 for the full ABI and struct
// layout.

package main

import "unsafe"

// Process-state enum for ProcInfo.State. Matches user/gooos/ps.go.
const (
	psRunning  uint8 = 0
	psSleeping uint8 = 1
	psExited   uint8 = 2
	psUnknown  uint8 = 3
)

// ProcInfo is the wire-format struct handed back by sys_listprocs.
// Layout is ABI-stable: 64 bytes, cache-line aligned, explicit
// padding. MUST mirror user/gooos/ps.go:ProcInfo byte-for-byte.
//
//	offset  field         size
//	  0     PID           4
//	  4     PPID          4
//	  8     State         1
//	  9     _pad1         3   (explicitly zeroed on snapshot)
//	 12     LastCpuID     4
//	 16     Ticks         8
//	 24     StartTick     8
//	 32     Name          32
//	total: 64 bytes
type ProcInfo struct {
	PID       uint32
	PPID      uint32
	State     uint8
	_pad1     [3]byte
	LastCpuID uint32
	Ticks     uint64
	StartTick uint64
	Name      [32]byte
}

// Build-time assertion: ProcInfo must be exactly 64 bytes.
// If this ever fires, reviewer-MINOR pattern documented in
// impldoc/shell_ps_command.md §2.1.
var _ [1]byte = [unsafe.Sizeof(ProcInfo{}) - 63]byte{}

// Per-process Name tracking. Process doesn't currently store the ELF
// name; we stash it on elfSpawn via setProcName. Lookup map keyed on
// PID for cheap access from fillProcInfo without adding a Name field
// to Process (which would bloat the struct for a single-consumer
// feature).
var procNameByPID = make(map[uint32][32]byte)

// setProcName records the ELF name for pid. Called from elfSpawn
// under procLock. Truncates to 31 chars plus NUL.
//
// Caller must hold procLock.
func setProcName(pid uint32, name string) {
	var buf [32]byte
	n := len(name)
	if n > 31 {
		n = 31
	}
	for i := 0; i < n; i++ {
		buf[i] = name[i]
	}
	procNameByPID[pid] = buf
}

// clearProcName removes the PID entry. Called from processWait and
// processExit cleanup paths. Caller must hold procLock.
func clearProcName(pid uint32) {
	delete(procNameByPID, pid)
}

// processStartTick is populated by elfSpawn under procLock. Cleared
// alongside procNameByPID.
var processStartTick = make(map[uint32]uint64)

// fillProcInfo populates dst from proc. Zero-initialises dst first
// so padding bytes never leak uninitialised kernel memory to user.
func fillProcInfo(dst *ProcInfo, proc *Process, now uint64) {
	*dst = ProcInfo{} // zero _pad1 and every other field
	dst.PID = proc.pid
	if proc.parent != nil {
		dst.PPID = proc.parent.pid
	}
	dst.State = psSleeping // default; refined below
	// Any process currently assigned to a per-CPU CurrentPoolIdx is
	// classified as Running. Cheap and approximate; see
	// impldoc/shell_ps_command.md §2.2.
	for i := uint32(0); i < maxCPUs; i++ {
		if perCPUBlocks[i].CurrentPoolIdx >= 0 &&
			perCPUBlocks[i].CurrentPoolIdx == int32(proc.poolIdx) {
			dst.State = psRunning
			break
		}
	}
	dst.LastCpuID = proc.LastCpuID
	start, ok := processStartTick[proc.pid]
	if !ok {
		start = 0
	}
	dst.StartTick = start
	if now >= start {
		dst.Ticks = now - start
	}
	if nm, ok := procNameByPID[proc.pid]; ok {
		dst.Name = nm
	} else if proc.pid == 0 {
		// Boot shell launched via elfLoad rather than elfSpawn — name
		// was never recorded. Synthesize a readable label.
		copy(dst.Name[:], "sh.elf")
	}
}

// --- Syscall 37: sys_listprocs ---
// RDI = buf vaddr, RSI = max entries.
// Returns number of entries filled, or -fdErrBad on error.
//
// The snapshot runs under procLock; user-PML4 writes happen AFTER
// release so a page walk through a user address that faults does not
// wedge the lock.
func sysListprocsHandler(frame *SyscallFrame) {
	caller := currentProc()
	if caller == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	bufVaddr := uintptr(frame.RDI)
	max := uint32(frame.RSI)
	if bufVaddr == 0 || max == 0 {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	if max > maxRing3Procs {
		max = maxRing3Procs
	}

	var staging [maxRing3Procs]ProcInfo
	n := uint32(0)
	now := pitTicks
	fl := procLock.Acquire()
	for _, proc := range procByPID {
		if n >= max {
			break
		}
		fillProcInfo(&staging[n], proc, now)
		n++
	}
	procLock.Release(fl)

	for i := uint32(0); i < n; i++ {
		if !writeProcInfoThrough(caller.pml4, bufVaddr+uintptr(i)*64, &staging[i]) {
			// Bad user pointer; return count so far (user gets a partial buffer).
			frame.RAX = uintptr(i)
			return
		}
	}
	frame.RAX = uintptr(n)
}

// writeProcInfoThrough copies a single 64-byte ProcInfo from kernel
// memory to the user vaddr, walking the user's PML4 to find the
// backing paddr(s). The struct may straddle a 4 KiB boundary —
// handled by walking per-page. Returns false if any page is
// unmapped.
//
//go:nosplit
func writeProcInfoThrough(pml4, vaddr uintptr, src *ProcInfo) bool {
	bytes := (*[64]byte)(unsafe.Pointer(src))
	for i := 0; i < 64; i++ {
		paddr := walkAndGetPaddrIn(pml4, vaddr+uintptr(i))
		if paddr == 0 {
			return false
		}
		off := paddr + ((vaddr + uintptr(i)) & (pageSize - 1))
		*(*byte)(unsafe.Pointer(off)) = bytes[i]
	}
	return true
}
