// src/user_signal.go — feature 2.2 user goroutine preemption via
// kernel-delivered SIGALRM (mechanism B).
//
// Three entry points from the kernel:
//
//   - sysSigactionHandler  (syscall #35): user registers a SIGALRM
//     handler function pointer.
//
//   - sysSigreturnHandler  (syscall #36): user's SIGALRM handler
//     tail-calls this to restore the interrupted context.
//
//   - maybeSignalUserPreempt(cpuIdx): called from handleLAPICTimer
//     under preemptEnabled — bumps the currently-running user
//     process's quantum counter and flags UserPreemptPending when
//     the quantum expires.
//
//   - maybeDeliverSignal(frame): called from handlePreemptIPI when
//     the interrupted context is Ring 3. Pushes a sigFrame onto the
//     user stack, rewrites the trap frame's RIP to the user's
//     SIGALRM handler. When iretq returns, user code starts at the
//     handler; the handler eventually calls sys_sigreturn to resume.
//
// See impldoc/preempt_user_goroutines.md for the full design.

package main

import "unsafe"

const (
	sigAlrmNumber = 14         // POSIX SIGALRM
	sigMagic      = 0xDEADBEEF // stamp on sigFrame for sys_sigreturn integrity check
)

// sigFrame is the 13-word (104-byte) block the kernel pushes onto
// the user stack before jumping into the SIGALRM handler.
// sys_sigreturn pops it in reverse order.
//
// Layout (low → high address, matching push order by the kernel:
// the LAST push is at the lowest address = current user_rsp):
//
//	offset +0   Magic (0xDEADBEEF)
//	offset +8   R11
//	+16 R10
//	+24 R9
//	+32 R8
//	+40 RDI
//	+48 RSI
//	+56 RDX
//	+64 RCX
//	+72 RAX
//	+80 RFLAGS
//	+88 RSP  (original user RSP before sigFrame push)
//	+96 RIP  (original user RIP)
//	total: 104 bytes
type sigFrame struct {
	Magic  uint64
	R11    uint64
	R10    uint64
	R9     uint64
	R8     uint64
	RDI    uint64
	RSI    uint64
	RDX    uint64
	RCX    uint64
	RAX    uint64
	RFLAGS uint64
	RSP    uint64
	RIP    uint64
}

const sigFrameSize = 104

// --- Syscall 35: sys_sigaction ---
// RDI = signum, RSI = handler (user-space function pointer), RDX = flags (reserved, must be 0).
// Returns 0 on success, negative errno on failure.

func sysSigactionHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	signum := uint32(frame.RDI)
	handler := uintptr(frame.RSI)
	flags := uint32(frame.RDX)
	if signum != sigAlrmNumber || flags != 0 {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	fl := procLock.Acquire()
	proc.SigAlrmHandler = handler
	proc.UserQuantumCounter = 0
	if proc.UserQuantumTicks == 0 {
		proc.UserQuantumTicks = 10 // default 100ms at 100Hz
	}
	proc.SigInProgress = 0
	proc.UserPreemptPending = 0
	procLock.Release(fl)
	frame.RAX = 0
}

// --- Syscall 36: sys_sigreturn ---
// No args. Reads the sigFrame at the current user RSP (the kernel
// pushed it there before redirecting RIP to the user handler); the
// handler's epilogue has adjusted user RSP back to point at sigFrame
// top. Restores RIP/RSP/RFLAGS/caller-saved GPRs, clears SigInProgress.
// Does not return to the calling sys_sigreturn invocation — instead
// the syscall iretq lands at the RESTORED RIP.

func sysSigreturnHandler(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil {
		frame.RAX = sysFail(fdErrBad)
		return
	}
	userRSP := uintptr(frame.RSP)
	pml4 := activePML4ForProc(proc)

	// Read 13 u64 fields off user stack.
	magic, ok1 := readU64Through(pml4, userRSP+0)
	r11, ok2 := readU64Through(pml4, userRSP+8)
	r10, ok3 := readU64Through(pml4, userRSP+16)
	r9, ok4 := readU64Through(pml4, userRSP+24)
	r8, ok5 := readU64Through(pml4, userRSP+32)
	rdi, ok6 := readU64Through(pml4, userRSP+40)
	rsi, ok7 := readU64Through(pml4, userRSP+48)
	rdx, ok8 := readU64Through(pml4, userRSP+56)
	rcx, ok9 := readU64Through(pml4, userRSP+64)
	rax, ok10 := readU64Through(pml4, userRSP+72)
	rflags, ok11 := readU64Through(pml4, userRSP+80)
	origRSP, ok12 := readU64Through(pml4, userRSP+88)
	origRIP, ok13 := readU64Through(pml4, userRSP+96)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8 &&
		ok9 && ok10 && ok11 && ok12 && ok13) || magic != sigMagic {
		// Corrupted / tampered sigFrame — kill the process.
		processExit(^uintptr(0))
		return
	}

	frame.R11 = uintptr(r11)
	frame.R10 = uintptr(r10)
	frame.R9 = uintptr(r9)
	frame.R8 = uintptr(r8)
	frame.RDI = uintptr(rdi)
	frame.RSI = uintptr(rsi)
	frame.RDX = uintptr(rdx)
	frame.RCX = uintptr(rcx)
	frame.RAX = uintptr(rax)
	frame.RFLAGS = uintptr(rflags)
	frame.RSP = uintptr(origRSP)
	frame.RIP = uintptr(origRIP)

	fl := procLock.Acquire()
	proc.SigInProgress = 0
	procLock.Release(fl)
}

// readU64Through reads 8 bytes from the user vaddr through the
// given PML4. Returns (value, ok). Unmapped ⇒ (0, false).
//
//go:nosplit
func readU64Through(pml4, vaddr uintptr) (uint64, bool) {
	var v uint64
	for i := uintptr(0); i < 8; i++ {
		paddr := walkAndGetPaddrIn(pml4, vaddr+i)
		if paddr == 0 {
			return 0, false
		}
		off := paddr + ((vaddr + i) & (pageSize - 1))
		v |= uint64(*(*byte)(unsafe.Pointer(off))) << (8 * i)
	}
	return v, true
}

// writeU64Through writes 8 bytes to the user vaddr through the given
// PML4. Page-boundary safe (walks per byte). Silent no-op on
// unmapped vaddr.
//
//go:nosplit
func writeU64Through(pml4, vaddr uintptr, val uint64) bool {
	for i := uintptr(0); i < 8; i++ {
		paddr := walkAndGetPaddrIn(pml4, vaddr+i)
		if paddr == 0 {
			return false
		}
		off := paddr + ((vaddr + i) & (pageSize - 1))
		*(*byte)(unsafe.Pointer(off)) = byte(val >> (8 * i))
	}
	return true
}

// pushU64Through decrements *userRSP by 8 and writes val at the new
// RSP position. Mirrors how x86 pushq mutates %rsp.
//
//go:nosplit
func pushU64Through(pml4 uintptr, userRSP *uintptr, val uint64) bool {
	*userRSP -= 8
	return writeU64Through(pml4, *userRSP, val)
}

// --- Tick-driven quantum accounting ---

// maybeSignalUserPreempt is called from handleLAPICTimer on every
// BSP tick when preemptEnabled. It identifies the currently-running
// user process on this CPU (if any), bumps its UserQuantumCounter,
// and raises UserPreemptPending when the quantum expires.
//
//go:nosplit
func maybeSignalUserPreempt(cpuIdx uint32) {
	poolIdx := perCPUBlocks[cpuIdx].CurrentPoolIdx
	if poolIdx < 0 {
		return // no ring3 process active on this CPU
	}
	// procByPool is maintained by ring3StackAcquire / ring3StackRelease;
	// look up the process owning this pool slot.
	proc := procByPoolIdx(int(poolIdx))
	if proc == nil || proc.SigAlrmHandler == 0 {
		return
	}
	proc.UserQuantumCounter++
	if proc.UserQuantumCounter < proc.UserQuantumTicks {
		return
	}
	proc.UserQuantumCounter = 0
	proc.UserPreemptPending = 1
}

// procByPoolIdx returns the Process owning the given ring3 pool
// slot, or nil if no process occupies that slot. Caller need not
// hold procLock — the poolIdx field on Process is immutable for
// the lifetime of the slot.
//
// Linear scan of procByPID (bounded by maxRing3Procs=32).
//
//go:nosplit
func procByPoolIdx(idx int) *Process {
	fl := procLock.Acquire()
	defer procLock.Release(fl)
	for _, p := range procByPID {
		if p.poolIdx == idx {
			return p
		}
	}
	return nil
}

// --- iretq-frame rewrite for SIGALRM delivery ---

// maybeDeliverSignal is called from handlePreemptIPI when the
// interrupted context is Ring 3. If the current process has a
// handler registered AND UserPreemptPending is set AND
// SigInProgress is 0, it pushes a sigFrame onto the user stack,
// rewrites frame.RIP to the user handler, updates frame.RSP,
// marks SigInProgress, and clears UserPreemptPending.
//
// Called with interrupt.Disable already in effect (we're mid-ISR).
//
//go:nosplit
func maybeDeliverSignal(frame *SyscallFrame) {
	proc := currentProc()
	if proc == nil || proc.SigAlrmHandler == 0 {
		return
	}
	if proc.UserPreemptPending == 0 || proc.SigInProgress != 0 {
		return
	}

	pml4 := activePML4ForProc(proc)
	userRSP := uintptr(frame.RSP)

	// Push sigFrame (13 words) in REVERSE order of layout so the
	// final user_rsp lands at Magic, making &sigFrame[0] = userRSP.
	if !pushU64Through(pml4, &userRSP, uint64(frame.RIP)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RSP)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RFLAGS)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RAX)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RCX)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RDX)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RSI)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.RDI)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.R8)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.R9)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.R10)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, uint64(frame.R11)) {
		return
	}
	if !pushU64Through(pml4, &userRSP, sigMagic) {
		return
	}

	// Rewrite iretq frame.
	frame.RIP = proc.SigAlrmHandler
	frame.RSP = userRSP

	proc.SigInProgress = 1
	proc.UserPreemptPending = 0
}
